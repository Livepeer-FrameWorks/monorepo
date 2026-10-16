package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// freePort returns a TCP port that is free on loopback, so one port can be
// bound on several addresses as a provisioned service binds it.
func freePort(t *testing.T) string {
	t.Helper()
	lis := listenLocal(t)
	_, port, err := net.SplitHostPort(lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = lis.Close()
	return port
}

// loopbackAddrs is 127.0.0.1 and, when the host has IPv6, ::1.
func loopbackAddrs(t *testing.T) []string {
	t.Helper()
	addrs := []string{"127.0.0.1"}
	if probe, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp6", "[::1]:0"); err == nil {
		_ = probe.Close()
		addrs = append(addrs, "::1")
	}
	return addrs
}

// nonLoopbackAddr is an address of this host outside loopback, or "" when it
// has none.
func nonLoopbackAddr(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil && !ipNet.IP.IsLinkLocalUnicast() {
			return ipNet.IP.String()
		}
	}
	return ""
}

func boundHosts(t *testing.T, lis net.Listener) []string {
	t.Helper()
	var addrs []net.Addr
	if m, ok := lis.(*multiListener); ok {
		addrs = m.Addrs()
	} else {
		addrs = []net.Addr{lis.Addr()}
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, host)
	}
	return out
}

// BIND_ADDRS binds every listener without its own bind address once per
// configured address and nowhere else, so a provisioned service answers on
// loopback and its WireGuard address but not on the LAN or public interfaces.
func TestBindListenersBindAddrsBindOnlyConfiguredAddresses(t *testing.T) {
	want := loopbackAddrs(t)
	httpRunning, grpcRunning, err := bindListeners(RunSpec{
		BindAddrs: append([]string{" "}, want...),
		HTTP:      []HTTPListener{{Name: "http", Port: freePort(t), Handler: http.NotFoundHandler()}},
		GRPC:      []GRPCListener{{Name: "grpc", Port: freePort(t), Server: grpc.NewServer()}},
	})
	if err != nil {
		t.Fatalf("bindListeners: %v", err)
	}
	defer func() {
		_ = httpRunning[0].lis.Close()
		_ = grpcRunning[0].lis.Close()
	}()
	for name, lis := range map[string]net.Listener{"http": httpRunning[0].lis, "grpc": grpcRunning[0].lis} {
		if got := boundHosts(t, lis); !slices.Equal(got, want) {
			t.Fatalf("%s listener bound %v, want exactly %v", name, got, want)
		}
	}
}

// Without BIND_ADDRS a listener keeps binding every interface.
func TestBindListenersWithoutBindAddrsBindEveryInterface(t *testing.T) {
	httpRunning, _, err := bindListeners(RunSpec{HTTP: []HTTPListener{{Name: "http", Port: "0", Handler: http.NotFoundHandler()}}})
	if err != nil {
		t.Fatalf("bindListeners: %v", err)
	}
	defer func() { _ = httpRunning[0].lis.Close() }()
	if got := boundHosts(t, httpRunning[0].lis); len(got) != 1 || !net.ParseIP(got[0]).IsUnspecified() {
		t.Fatalf("listener bound %v, want one all-interface socket", got)
	}
}

// A listener that clients outside the mesh dial, and one with its own bind
// address, keep their bind when BIND_ADDRS is set.
func TestBindListenersBindAddrsSkipPublicAndExplicitListeners(t *testing.T) {
	httpRunning, grpcRunning, err := bindListeners(RunSpec{
		BindAddrs: []string{"127.0.0.1"},
		HTTP:      []HTTPListener{{Name: "own-bind", BindAddr: "localhost", Port: "0", Handler: http.NotFoundHandler()}},
		GRPC:      []GRPCListener{{Name: "external", Port: "0", AllInterfaces: true, Server: grpc.NewServer()}},
	})
	if err != nil {
		t.Fatalf("bindListeners: %v", err)
	}
	defer func() {
		_ = httpRunning[0].lis.Close()
		_ = grpcRunning[0].lis.Close()
	}()
	if got := boundHosts(t, grpcRunning[0].lis); len(got) != 1 || !net.ParseIP(got[0]).IsUnspecified() {
		t.Fatalf("AllInterfaces listener bound %v, want every interface", got)
	}
	if got := boundHosts(t, httpRunning[0].lis); len(got) != 1 || !net.ParseIP(got[0]).IsLoopback() {
		t.Fatalf("explicit BindAddr listener bound %v, want its own loopback bind", got)
	}
}

// One HTTP server and one gRPC server serve every configured address, refuse
// the host's other addresses, and release every socket on shutdown.
func TestRunServesEveryBindAddrAndStopsThem(t *testing.T) {
	resetReloadFnsForTest(t)
	addrs := loopbackAddrs(t)
	httpPort, grpcPort := freePort(t), freePort(t)
	grpcServer := grpc.NewServer()
	healthServer := health.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, RunSpec{
			Service:   "svc",
			Logger:    logging.NewLogger(),
			BindAddrs: addrs,
			HTTP:      []HTTPListener{{Name: "http", Port: httpPort, Handler: handler}},
			GRPC:      []GRPCListener{{Name: "grpc", Port: grpcPort, Server: grpcServer}},
		})
	}()

	for _, addr := range addrs {
		url := "http://" + net.JoinHostPort(addr, httpPort) + "/"
		var resp *http.Response
		var err error
		for range 50 {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
			if resp, err = http.DefaultClient.Do(req); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("GET %s = %d", url, resp.StatusCode)
		}
		callCtx, callCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, err := grpc_health_v1.NewHealthClient(dialGRPC(t, net.JoinHostPort(addr, grpcPort))).Check(callCtx, &grpc_health_v1.HealthCheckRequest{}); err != nil {
			callCancel()
			t.Fatalf("gRPC health on %s: %v", addr, err)
		}
		callCancel()
	}
	if other := nonLoopbackAddr(t); other != "" {
		for _, port := range []string{httpPort, grpcPort} {
			var d net.Dialer
			dialCtx, dialCancel := context.WithTimeout(context.Background(), time.Second)
			conn, err := d.DialContext(dialCtx, "tcp", net.JoinHostPort(other, port))
			dialCancel()
			if err == nil {
				_ = conn.Close()
				t.Fatalf("port %s answers on %s, which BIND_ADDRS does not list", port, other)
			}
		}
	}

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	for _, addr := range addrs {
		for _, port := range []string{httpPort, grpcPort} {
			var d net.Dialer
			if conn, err := d.DialContext(context.Background(), "tcp", net.JoinHostPort(addr, port)); err == nil {
				_ = conn.Close()
				t.Fatalf("%s still accepts connections after shutdown", net.JoinHostPort(addr, port))
			}
		}
	}
}

// A failed bind on a later address releases the sockets already opened, so a
// restart is not refused by its own leftover listener.
func TestListenAddrsReleasesOpenedSocketsOnFailure(t *testing.T) {
	port := freePort(t)
	if _, err := ListenAddrs([]string{"127.0.0.1", "127.0.0.1"}, port); err == nil {
		t.Fatal("ListenAddrs binding one address twice succeeded")
	}
	lis, err := ListenAddrs([]string{"127.0.0.1"}, port)
	if err != nil {
		t.Fatalf("rebind 127.0.0.1:%s after a failed ListenAddrs: %v", port, err)
	}
	_ = lis.Close()
}

// Close unblocks a pending Accept with net.ErrClosed, which net/http and gRPC
// treat as a clean stop.
func TestMultiListenerCloseUnblocksAccept(t *testing.T) {
	addrs := loopbackAddrs(t)
	if len(addrs) < 2 {
		addrs = []string{"127.0.0.1", "localhost"}
	}
	lis, err := ListenAddrs(addrs, freePort(t))
	if err != nil {
		t.Skipf("bind %v: %v", addrs, err)
	}
	accepted := make(chan error, 1)
	go func() {
		conn, acceptErr := lis.Accept()
		if conn != nil {
			_ = conn.Close()
		}
		accepted <- acceptErr
	}()
	time.Sleep(20 * time.Millisecond)
	if err := lis.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-accepted:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still blocked after Close")
	}
	if err := lis.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
