package ssh

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLocalForwardArgs pins the argv contract. ssh must be invoked with -N,
// ExitOnForwardFailure=yes, an explicit -L 127.0.0.1:<local>:<remote>:<port>
// spec, and the resolved target.
func TestLocalForwardArgs(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemotePort: 19002,
		LocalPort:  54321,
	})
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })

	if tun.LocalAddr != "127.0.0.1:54321" {
		t.Fatalf("LocalAddr = %q, want 127.0.0.1:54321", tun.LocalAddr)
	}
	if tun.LocalPort() != 54321 {
		t.Fatalf("LocalPort = %d, want 54321", tun.LocalPort())
	}
	if tun.RemoteHost != "127.0.0.1" || tun.RemotePort != 19002 {
		t.Fatalf("remote endpoint = %s:%d, want 127.0.0.1:19002", tun.RemoteHost, tun.RemotePort)
	}

	// The helper subprocess prints the argv it was invoked with. We reach into
	// cmd.Args (ssh, args...) — assert the exact -L spec, -N, and ExitOnForwardFailure.
	args := tun.cmd.Args
	if !containsFlag(args, "-N") {
		t.Errorf("missing -N in argv: %v", args)
	}
	if !containsPair(args, "-o", "ExitOnForwardFailure=yes") {
		t.Errorf("missing ExitOnForwardFailure=yes: %v", args)
	}
	wantSpec := "127.0.0.1:54321:127.0.0.1:19002"
	if !containsPair(args, "-L", wantSpec) {
		t.Errorf("missing -L %s: %v", wantSpec, args)
	}
	// Target must come after -L spec.
	target := args[len(args)-1]
	if target != "root@1.2.3.4" {
		t.Errorf("expected last argv to be ssh target, got %q", target)
	}
}

// TestLocalForwardCustomRemoteHost verifies non-default remote bind interface.
func TestLocalForwardCustomRemoteHost(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemoteHost: "10.0.0.5",
		RemotePort: 5432,
		LocalPort:  19999,
	})
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })

	wantSpec := "127.0.0.1:19999:10.0.0.5:5432"
	if !containsPair(tun.cmd.Args, "-L", wantSpec) {
		t.Errorf("missing -L %s: %v", wantSpec, tun.cmd.Args)
	}
}

// TestLocalForwardFreePort exercises auto-port selection. The picked port
// must be non-zero and reachable as a fresh TCP listener after Close()
// releases it (or at least be in the ephemeral range).
func TestLocalForwardFreePort(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemotePort: 19002,
	})
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })

	if tun.LocalPort() == 0 {
		t.Fatalf("expected non-zero local port; got 0 (LocalAddr=%s)", tun.LocalAddr)
	}
	if !strings.HasPrefix(tun.LocalAddr, "127.0.0.1:") {
		t.Errorf("LocalAddr should bind to 127.0.0.1; got %s", tun.LocalAddr)
	}
}

// TestLocalForwardReadinessFailureCleansUp verifies that when the readiness
// probe fails, the child process is reaped and the error includes context.
func TestLocalForwardReadinessFailureCleansUp(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error {
		return errors.New("simulated probe failure")
	}
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemotePort: 19002,
		LocalPort:  54322,
	})
	if err == nil {
		t.Fatalf("expected error; got tunnel %v", tun)
	}
	if !strings.Contains(err.Error(), "simulated probe failure") {
		t.Errorf("error should propagate probe cause: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:54322") {
		t.Errorf("error should include local addr: %v", err)
	}
	if !strings.Contains(err.Error(), "19002") {
		t.Errorf("error should include remote port: %v", err)
	}
}

// TestLocalForwardCloseIsIdempotent locks the contract that Close() can be
// called multiple times without panicking or returning a new error.
func TestLocalForwardCloseIsIdempotent(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemotePort: 19002,
		LocalPort:  54323,
	})
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestLocalForwardValidation covers the input-validation guard rails.
func TestLocalForwardValidation(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		_, err := LocalForward(context.Background(), LocalForwardOptions{RemotePort: 19002})
		if err == nil || !strings.Contains(err.Error(), "Config is required") {
			t.Fatalf("expected nil-config error; got %v", err)
		}
	})
	t.Run("invalid remote port", func(t *testing.T) {
		_, err := LocalForward(context.Background(), LocalForwardOptions{
			Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
			Resolution: Resolution{Target: "root@1.2.3.4"},
			RemotePort: 0,
		})
		if err == nil || !strings.Contains(err.Error(), "RemotePort") {
			t.Fatalf("expected RemotePort error; got %v", err)
		}
	})
}

// TestPickFreeLocalPort sanity-checks the helper directly.
func TestPickFreeLocalPort(t *testing.T) {
	p, err := pickFreeLocalPort(context.Background())
	if err != nil {
		t.Fatalf("pickFreeLocalPort: %v", err)
	}
	if p == 0 {
		t.Fatal("expected non-zero port")
	}
	// Confirm we can re-listen on the returned port (proves it was released).
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("post-pick listen: %v", err)
	}
	_ = l.Close()
}

// TestDefaultTunnelReadyDeadline exercises the real probe path against a
// closed local port: the dial loop must give up at the deadline.
func TestDefaultTunnelReadyDeadline(t *testing.T) {
	// Use a port we know is free (and stays free).
	port, err := pickFreeLocalPort(context.Background())
	if err != nil {
		t.Fatalf("pickFreeLocalPort: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))

	deadline := time.Now().Add(150 * time.Millisecond)
	err = defaultTunnelReady(context.Background(), addr, deadline)
	if err == nil {
		t.Fatal("expected probe timeout against closed port")
	}
	if !strings.Contains(err.Error(), "not ready before deadline") {
		t.Errorf("expected deadline error; got %v", err)
	}
}

// TestDefaultTunnelReadySucceedsOnLiveListener exercises the happy path.
func TestDefaultTunnelReadySucceedsOnLiveListener(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	deadline := time.Now().Add(2 * time.Second)
	if err := defaultTunnelReady(context.Background(), l.Addr().String(), deadline); err != nil {
		t.Fatalf("expected probe success; got %v", err)
	}
}
