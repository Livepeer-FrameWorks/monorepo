package control

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"
	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/grpc"
)

func TestParseControlAddrs(t *testing.T) {
	t.Parallel()
	got := parseControlAddrs(" 192.168.10.17:18029, 192.168.10.18:18029,,192.168.10.17:18029 ,foghorn.cell.example.com:18029 ")
	want := []string{"192.168.10.17:18029", "192.168.10.18:18029", "foghorn.cell.example.com:18029"}
	if !slices.Equal(got, want) {
		t.Fatalf("parseControlAddrs = %v, want %v", got, want)
	}
}

// A name that resolves to several instances becomes one endpoint per instance,
// and each endpoint keeps the configured name for its TLS identity.
func TestResolveControlEndpointsExpandsEveryInstance(t *testing.T) {
	t.Parallel()
	lookups := 0
	lookup := func(_ context.Context, host string) ([]string, error) {
		lookups++
		switch host {
		case "foghorn.cell.example.com":
			return []string{"10.0.0.2", "10.0.0.1"}, nil
		default:
			return nil, errors.New("no such host")
		}
	}
	got := resolveControlEndpoints(context.Background(), []string{
		"foghorn.cell.example.com:18029",
		"192.168.10.17:18029",
		"missing.example.com:18029",
		"10.0.0.1:18029",
	}, lookup)
	want := []controlEndpoint{
		{addr: "foghorn.cell.example.com:18029", dial: "10.0.0.1:18029"},
		{addr: "foghorn.cell.example.com:18029", dial: "10.0.0.2:18029"},
		{addr: "192.168.10.17:18029", dial: "192.168.10.17:18029"},
		{addr: "missing.example.com:18029", dial: "missing.example.com:18029"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("resolveControlEndpoints = %v, want %v", got, want)
	}
	if lookups != 2 {
		t.Fatalf("looked up %d names, want only the two host names", lookups)
	}
	if name := foghornControlServerName(got[0].addr, ""); name != "foghorn.cell.example.com" {
		t.Fatalf("server name for a resolved instance = %q, want the configured name", name)
	}
}

func TestNextControlEndpointMovesToTheNextInstance(t *testing.T) {
	t.Parallel()
	endpoints := []controlEndpoint{{addr: "a", dial: "a"}, {addr: "b", dial: "b"}, {addr: "c", dial: "c"}}
	for last, want := range map[string]string{"a": "b", "b": "c", "c": "a"} {
		if got := nextControlEndpoint(endpoints, last); got.dial != want {
			t.Fatalf("after %s dialed %s, want %s", last, got.dial, want)
		}
	}
	if got := nextControlEndpoint(endpoints, "gone"); !slices.Contains(endpoints, got) {
		t.Fatalf("first dial picked %v, not one of the endpoints", got)
	}
}

// fakeFoghorn is a control-stream server that counts registrations and, when
// goingAway is set, answers each one with a going-away notice.
type fakeFoghorn struct {
	ipcpb.UnimplementedHelmsmanControlServer
	goingAway bool
	addr      string
	server    *grpc.Server

	mu            sync.Mutex
	registrations int
	registered    chan time.Time
}

func (f *fakeFoghorn) Connect(stream ipcpb.HelmsmanControl_ConnectServer) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	if msg.GetRegister() == nil {
		return errors.New("first message is not Register")
	}
	f.mu.Lock()
	f.registrations++
	f.mu.Unlock()
	select {
	case f.registered <- time.Now():
	default:
	}
	if f.goingAway {
		if err := stream.Send(&ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_GoingAway{GoingAway: &ipcpb.GoingAway{Reason: "shutdown"}}}); err != nil {
			return err
		}
	}
	// Hold the stream until the client leaves it.
	for {
		if _, err := stream.Recv(); err != nil {
			return err
		}
	}
}

func (f *fakeFoghorn) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registrations
}

func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return lis
}

func startFakeFoghorn(t *testing.T, goingAway bool) *fakeFoghorn {
	t.Helper()
	lis := listenLoopback(t)
	f := &fakeFoghorn{goingAway: goingAway, addr: lis.Addr().String(), server: grpc.NewServer(), registered: make(chan time.Time, 16)}
	ipcpb.RegisterHelmsmanControlServer(f.server, f)
	go func() { _ = f.server.Serve(lis) }()
	t.Cleanup(f.server.Stop)
	return f
}

// refusedAddr is a loopback address nothing listens on.
func refusedAddr(t *testing.T) string {
	t.Helper()
	lis := listenLoopback(t)
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

// runControlDialer runs the real control client against addrs, starting at the
// endpoint after lastDial, until the test ends.
func runControlDialer(t *testing.T, addrs []string, lastDial string, servers ...*fakeFoghorn) {
	t.Helper()
	resetControlState(t)
	stateDir := t.TempDir()
	appconfigtest.Setenv(t, "HELMSMAN_STATE_DIR", stateDir)
	prevConfig := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{
		NodeID:            "edge-failover-test",
		StateDir:          stateDir,
		StorageLocalPath:  t.TempDir(),
		GRPCAllowInsecure: true,
	}
	logger := logging.NewLogger()
	stop := make(chan struct{})
	done := make(chan struct{})
	dialer := &controlDialer{
		addrs:    addrs,
		lookup:   net.DefaultResolver.LookupHost,
		connect:  func(endpoint controlEndpoint) error { return runClient(endpoint, logger) },
		logger:   logger,
		stop:     stop,
		lastDial: lastDial,
	}
	go func() {
		defer close(done)
		dialer.run()
	}()
	t.Cleanup(func() {
		close(stop)
		// Ends the connection the client holds, so run sees stop.
		for _, f := range servers {
			f.server.Stop()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("control dialer did not stop")
		}
		currentConfig = prevConfig
	})
}

func awaitRegistration(t *testing.T, f *fakeFoghorn, within time.Duration) time.Time {
	t.Helper()
	select {
	case at := <-f.registered:
		return at
	case <-time.After(within):
		t.Fatalf("no registration at %s within %s", f.addr, within)
		return time.Time{}
	}
}

// An instance that announces it is going away is left for another instance of
// the cell at once, not redialed.
func TestControlClientFailsOverWhenFoghornGoesAway(t *testing.T) {
	leaving := startFakeFoghorn(t, true)
	staying := startFakeFoghorn(t, false)
	runControlDialer(t, []string{leaving.addr, staying.addr}, staying.addr, leaving, staying)

	noticeAt := awaitRegistration(t, leaving, 10*time.Second)
	registeredAt := awaitRegistration(t, staying, 2*time.Second)
	if gap := registeredAt.Sub(noticeAt); gap > time.Second {
		t.Fatalf("registered with the next instance %s after the notice", gap)
	}
	if n := leaving.count(); n != 1 {
		t.Fatalf("the instance that went away saw %d registrations, want 1", n)
	}
}

// An instance that refuses the connection is skipped for the next one without
// waiting out the backoff.
func TestControlClientFailsOverWhenFoghornRefuses(t *testing.T) {
	refused := refusedAddr(t)
	staying := startFakeFoghorn(t, false)
	started := time.Now()
	runControlDialer(t, []string{refused, staying.addr}, staying.addr, staying)

	registeredAt := awaitRegistration(t, staying, 5*time.Second)
	if waited := registeredAt.Sub(started); waited > 2*time.Second {
		t.Fatalf("registered with the next instance %s after start", waited)
	}
}
