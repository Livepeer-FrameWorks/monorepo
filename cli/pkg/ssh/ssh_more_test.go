package ssh

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestConnectTimeoutSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   time.Duration
		want int
	}{
		{"zero defaults to 10", 0, 10},
		{"negative defaults to 10", -5 * time.Second, 10},
		{"sub-second rounds up to 1", 500 * time.Millisecond, 1},
		{"exactly 1s", 1 * time.Second, 1},
		{"exact seconds", 7 * time.Second, 7},
		{"truncates fractional", 7500 * time.Millisecond, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := connectTimeoutSeconds(&ConnectionConfig{Timeout: tc.in})
			if got != tc.want {
				t.Fatalf("connectTimeoutSeconds(%v)=%d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildSSHArgs_ConnectTimeoutValue(t *testing.T) {
	t.Parallel()
	cfg := &ConnectionConfig{Address: "1.2.3.4", User: "root", Timeout: 8 * time.Second}
	args := BuildSSHArgs(cfg, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(args, "-o", "ConnectTimeout=8") {
		t.Fatalf("expected ConnectTimeout=8; got %v", args)
	}

	def := BuildSSHArgs(&ConnectionConfig{Address: "1.2.3.4"}, Resolution{Target: "root@1.2.3.4"})
	if !containsPair(def, "-o", "ConnectTimeout=10") {
		t.Fatalf("expected default ConnectTimeout=10; got %v", def)
	}
}

func TestBuildSCPArgs_KeyPath(t *testing.T) {
	t.Parallel()
	with := BuildSCPArgs(&ConnectionConfig{Address: "1.2.3.4", KeyPath: "/k"}, Resolution{Target: "root@1.2.3.4"}, "/l", "/r")
	if !containsPair(with, "-i", "/k") {
		t.Fatalf("expected -i /k; got %v", with)
	}
	without := BuildSCPArgs(&ConnectionConfig{Address: "1.2.3.4"}, Resolution{Target: "root@1.2.3.4"}, "/l", "/r")
	if containsFlag(without, "-i") {
		t.Fatalf("expected no -i when KeyPath empty; got %v", without)
	}
}

func TestPingTimeout(t *testing.T) {
	t.Parallel()
	if got := pingTimeout(&ConnectionConfig{}); got != 5*time.Second {
		t.Fatalf("default pingTimeout=%v, want 5s", got)
	}
	if got := pingTimeout(&ConnectionConfig{Timeout: 12 * time.Second}); got != 12*time.Second {
		t.Fatalf("pingTimeout with Timeout set=%v, want 12s", got)
	}
}

func TestResolveTimeout(t *testing.T) {
	t.Parallel()
	if got := resolveTimeout(&ConnectionConfig{}); got != 5*time.Second {
		t.Fatalf("default resolveTimeout=%v, want 5s", got)
	}
	if got := resolveTimeout(&ConnectionConfig{Timeout: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("resolveTimeout with Timeout set=%v, want 3s", got)
	}
}

func TestWrapRunError_ExitCodeBoundary(t *testing.T) {
	t.Parallel()
	// exitCode > 0 path: "exited N"
	pos := wrapRunError("t", "cmd", 1, "boom", errors.New("x")).Error()
	if !strings.Contains(pos, "exited 1") {
		t.Fatalf("exit=1 should say exited 1; got %s", pos)
	}
	// exitCode == 0 path: no "exited" phrasing
	zero := wrapRunError("t", "cmd", 0, "boom", errors.New("x")).Error()
	if strings.Contains(zero, "exited") {
		t.Fatalf("exit=0 must not say exited; got %s", zero)
	}
	// exitCode == -1 (spawn failure) also takes the non-exit path
	neg := wrapRunError("t", "cmd", -1, "boom", errors.New("x")).Error()
	if strings.Contains(neg, "exited") {
		t.Fatalf("exit=-1 must not say exited; got %s", neg)
	}
}

func TestWrapRunError_StderrCapBoundary(t *testing.T) {
	t.Parallel()
	const cap = 2048
	atCap := strings.Repeat("y", cap)
	if msg := wrapRunError("t", "cmd", 1, atCap, errors.New("x")).Error(); strings.Contains(msg, "truncated") {
		t.Fatalf("len==cap must NOT truncate; got truncated marker")
	}
	overCap := strings.Repeat("y", cap+1)
	if msg := wrapRunError("t", "cmd", 1, overCap, errors.New("x")).Error(); !strings.Contains(msg, "truncated") {
		t.Fatalf("len>cap must truncate")
	}
}

func TestWrapScpError_ExitAndCapBoundary(t *testing.T) {
	t.Parallel()
	pos := wrapScpError("t", "/l", "/r", 1, "boom", errors.New("x")).Error()
	if !strings.Contains(pos, "exited 1") {
		t.Fatalf("scp exit=1 should say exited 1; got %s", pos)
	}
	zero := wrapScpError("t", "/l", "/r", 0, "boom", errors.New("x")).Error()
	if strings.Contains(zero, "exited") {
		t.Fatalf("scp exit=0 must not say exited; got %s", zero)
	}
	const cap = 2048
	atCap := strings.Repeat("y", cap)
	if msg := wrapScpError("t", "/l", "/r", 1, atCap, errors.New("x")).Error(); strings.Contains(msg, "truncated") {
		t.Fatalf("scp len==cap must NOT truncate")
	}
	overCap := strings.Repeat("y", cap+1)
	if msg := wrapScpError("t", "/l", "/r", 1, overCap, errors.New("x")).Error(); !strings.Contains(msg, "truncated") {
		t.Fatalf("scp len>cap must truncate")
	}
}

func TestNewPool_DefaultTimeout(t *testing.T) {
	t.Parallel()
	p := NewPool(0, "")
	if got := p.Stats()["timeout"].(time.Duration); got != 30*time.Second {
		t.Fatalf("zero timeout should default to 30s; got %v", got)
	}
	p2 := NewPool(15*time.Second, "")
	if got := p2.Stats()["timeout"].(time.Duration); got != 15*time.Second {
		t.Fatalf("explicit timeout must be preserved; got %v", got)
	}
}

func TestPool_EffectiveConfig(t *testing.T) {
	t.Parallel()
	p := NewPool(42*time.Second, "/default/key")
	// Timeout==0 and KeyPath=="" → defaults applied
	got := p.effectiveConfig(&ConnectionConfig{Address: "1.2.3.4"})
	if got.Timeout != 42*time.Second {
		t.Fatalf("expected pool timeout applied; got %v", got.Timeout)
	}
	if got.KeyPath != "/default/key" {
		t.Fatalf("expected pool key applied; got %q", got.KeyPath)
	}
	// Caller-set values must NOT be overridden
	got2 := p.effectiveConfig(&ConnectionConfig{Address: "1.2.3.4", Timeout: 9 * time.Second, KeyPath: "/own"})
	if got2.Timeout != 9*time.Second {
		t.Fatalf("caller timeout must win; got %v", got2.Timeout)
	}
	if got2.KeyPath != "/own" {
		t.Fatalf("caller key must win; got %q", got2.KeyPath)
	}
}

func TestPool_EffectiveConfigDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	p := NewPool(42*time.Second, "/default/key")
	in := &ConnectionConfig{Address: "1.2.3.4"}
	_ = p.effectiveConfig(in)
	if in.Timeout != 0 || in.KeyPath != "" {
		t.Fatalf("input config mutated: %+v", in)
	}
}

func TestTunnelClose_NilCmdAndCancel(t *testing.T) {
	t.Parallel()
	// cmd==nil and cancel==nil: Close must not panic and return nil.
	tun := &Tunnel{}
	if err := tun.Close(); err != nil {
		t.Fatalf("Close on empty tunnel: %v", err)
	}
	// Second close is a no-op via closed flag.
	if err := tun.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestTunnelClose_CancelInvoked(t *testing.T) {
	t.Parallel()
	called := false
	tun := &Tunnel{cancel: func() { called = true }}
	if err := tun.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !called {
		t.Fatal("cancel must be invoked when non-nil")
	}
}

func TestLocalForward_RemotePortBoundary(t *testing.T) {
	t.Parallel()
	for _, port := range []int{-1, 0, 65536, 100000} {
		_, err := LocalForward(context.Background(), LocalForwardOptions{
			Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
			Resolution: Resolution{Target: "root@1.2.3.4"},
			RemotePort: port,
		})
		if err == nil || !strings.Contains(err.Error(), "RemotePort") {
			t.Fatalf("port %d should be rejected; got %v", port, err)
		}
	}
}

func TestLocalForward_MaxRemotePortAccepted(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	// 65535 is the highest valid port and must be accepted (gate is > 65535).
	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "root"},
		Resolution: Resolution{Target: "root@1.2.3.4"},
		RemotePort: 65535,
		LocalPort:  54388,
	})
	if err != nil {
		t.Fatalf("port 65535 must be accepted; got %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })
	if !containsPair(tun.cmd.Args, "-L", "127.0.0.1:54388:127.0.0.1:65535") {
		t.Fatalf("expected -L spec with port 65535; got %v", tun.cmd.Args)
	}
}

func TestLocalForward_ResolutionDerivedWhenEmpty(t *testing.T) {
	oldExec := execCommandContext
	oldReady := tunnelReady
	execCommandContext = testExecCommandContext
	tunnelReady = func(_ context.Context, _ string, _ time.Time) error { return nil }
	defer func() {
		execCommandContext = oldExec
		tunnelReady = oldReady
	}()

	// Resolution.Target empty → derived from Config (User@Address).
	tun, err := LocalForward(context.Background(), LocalForwardOptions{
		Config:     &ConnectionConfig{Address: "1.2.3.4", User: "deploy"},
		RemotePort: 19002,
		LocalPort:  54399,
	})
	if err != nil {
		t.Fatalf("LocalForward: %v", err)
	}
	t.Cleanup(func() { _ = tun.Close() })
	if last := tun.cmd.Args[len(tun.cmd.Args)-1]; last != "deploy@1.2.3.4" {
		t.Fatalf("derived target=%q, want deploy@1.2.3.4", last)
	}
}
