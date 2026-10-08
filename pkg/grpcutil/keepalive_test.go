package grpcutil

import (
	"bytes"
	"context"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
)

// A client that pings more often than the server's enforcement policy admits
// is answered with a too_many_pings GOAWAY, and so is one that pings without
// an open stream, which the policy does not permit.
func TestClientKeepalivesRespectServerEnforcementPolicy(t *testing.T) {
	t.Parallel()
	if serverKeepaliveEnforcement.MinTime != KeepaliveMinTime || serverKeepaliveEnforcement.PermitWithoutStream {
		t.Fatalf("server enforcement policy = %+v, want MinTime %s without PermitWithoutStream", serverKeepaliveEnforcement, KeepaliveMinTime)
	}
	for name, params := range map[string]keepalive.ClientParameters{
		"replica":        replicaClientKeepalive,
		"control stream": ControlStreamKeepalive(),
	} {
		if params.Time < KeepaliveMinTime {
			t.Errorf("%s keepalive pings every %s, below the server minimum %s", name, params.Time, KeepaliveMinTime)
		}
		if params.PermitWithoutStream {
			t.Errorf("%s keepalive pings without a stream; the server policy refuses that", name)
		}
	}
}

// Helmsman's control stream has to leave a Foghorn that died without closing
// its socket well inside a minute.
func TestControlStreamKeepaliveDetectsADeadPeerWithinHalfAMinute(t *testing.T) {
	t.Parallel()
	params := ControlStreamKeepalive()
	if params.Timeout <= 0 || params.Time+params.Timeout > 30*time.Second {
		t.Fatalf("control stream keepalive detects a dead peer after %s+%s, want at most 30s", params.Time, params.Timeout)
	}
}

// serverConstructors are the files that build each service's gRPC server.
var serverConstructors = map[string]string{
	"foghorn":       "api_balancing/internal/control/server.go",
	"commodore":     "api_control/internal/grpc/server.go",
	"purser":        "api_billing/internal/grpc/server.go",
	"periscope":     "api_analytics_query/internal/grpc/server.go",
	"quartermaster": "api_tenants/internal/grpc/server.go",
	"decklog":       "api_firehose/internal/grpc/server.go",
	"skipper":       "api_consultant/cmd/skipper/main.go",
	"deckhand":      "api_ticketing/cmd/deckhand/main.go",
	"lookout":       "api_incidents/cmd/lookout/main.go",
	"navigator":     "api_dns/cmd/navigator/main.go",
	"bosun":         "api_webhooks/cmd/bosun/main.go",
	"signalman":     "api_realtime/cmd/signalman/main.go",
}

// calls reports whether src calls the qualified function call, such as
// "grpc.NewServer(", and not one whose package name merely ends in it, such as
// "deckhandgrpc.NewServer(". A substring scan keeps the walk over every
// generated file in the tree fast under the race detector.
func calls(src []byte, call string) bool {
	needle := []byte(call)
	for i := 0; ; {
		at := bytes.Index(src[i:], needle)
		if at < 0 {
			return false
		}
		at += i
		if at == 0 || !isIdentByte(src[at-1]) {
			return true
		}
		i = at + len(needle)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// Every service builds its gRPC server through NewServer, and nothing else in
// the tree builds one or sets server keepalive options, so no server falls
// back to gRPC's default policy that answers the shared client keepalives
// with too_many_pings.
func TestEveryGRPCServerUsesTheSharedKeepalive(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	for service, path := range serverConstructors {
		src, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatalf("%s: read %s: %v", service, path, err)
		}
		if !calls(src, "grpcutil.NewServer(") {
			t.Errorf("%s: %s does not build its server with grpcutil.NewServer", service, path)
		}
	}

	// replicatest serves health replicas to this package's own tests and
	// cannot import it.
	allowed := map[string]bool{
		"pkg/grpcutil/keepalive.go":               true,
		"pkg/grpcutil/replicatest/replicatest.go": true,
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}
	for _, entry := range entries {
		module := filepath.Join(root, entry.Name())
		if _, statErr := os.Stat(filepath.Join(module, "go.mod")); statErr != nil {
			continue
		}
		if walkErr := filepath.WalkDir(module, func(path string, d fs.DirEntry, err error) error {
			return checkServerSource(t, root, allowed, path, d, err)
		}); walkErr != nil {
			t.Fatalf("walk %s: %v", module, walkErr)
		}
	}
}

func checkServerSource(t *testing.T, root string, allowed map[string]bool, path string, d fs.DirEntry, err error) error {
	t.Helper()
	if err != nil {
		return err
	}
	if d.IsDir() {
		if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "testdata" {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
		return nil
	}
	rel, _ := filepath.Rel(root, path)
	rel = filepath.ToSlash(rel)
	if allowed[rel] {
		return nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if calls(src, "grpc.NewServer(") {
		t.Errorf("%s builds a gRPC server with grpc.NewServer; use grpcutil.NewServer", rel)
	}
	if calls(src, "grpc.KeepaliveParams(") || calls(src, "grpc.KeepaliveEnforcementPolicy(") {
		t.Errorf("%s sets its own server keepalive option; grpcutil.NewServer owns it", rel)
	}
	return nil
}

// A replica client holds a health Watch open on every replica, idle while the
// replica stays healthy. Its pings must not get the connection closed with a
// too_many_pings GOAWAY, which gRPC's default policy (one ping per 5 minutes)
// sends on the fourth ping. gRPC never pings more often than every 10s, so
// the client and the server's minimum are halved from 30s and 15s; four pings
// then take 40s.
func TestServerAdmitsReplicaKeepaliveOnAnIdleStream(t *testing.T) {
	t.Parallel()
	client := replicaClientKeepalive
	client.Time = 10 * time.Second
	enforcement := serverKeepaliveEnforcement
	enforcement.MinTime = 5 * time.Second
	hold := 4*client.Time + 5*time.Second

	lis, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := newServerWithEnforcement(enforcement)
	grpc_health_v1.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(client),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watch, err := grpc_health_v1.NewHealthClient(conn).Watch(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	if _, err := watch.Recv(); err != nil {
		t.Fatalf("first health status: %v", err)
	}
	ended := make(chan error, 1)
	go func() {
		_, err := watch.Recv()
		ended <- err
	}()

	select {
	case err := <-ended:
		t.Fatalf("idle stream ended while the client pinged within the server policy: %v", err)
	case <-time.After(hold):
	}
	if state := conn.GetState(); state != connectivity.Ready {
		t.Fatalf("connection is %s after %s of pings, want READY", state, hold)
	}
}
