package ssh

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// countSSHProcesses replaces the ssh/scp spawner for the test, failing the first failFirst processes the way a reset
// connection does, and returns the spawned program names.
func countSSHProcesses(t *testing.T, failFirst int) *[]string {
	t.Helper()
	var spawned []string
	oldExec := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		spawned = append(spawned, name)
		if len(spawned) <= failFirst {
			return testFailingExecCommandContext(255, "Connection reset by peer")(ctx, name, args...)
		}
		return testExecCommandContext(ctx, name, args...)
	}
	t.Cleanup(func() { execCommandContext = oldExec })
	return &spawned
}

func newTestPool(created *int) *Pool {
	pool := NewPool(2*time.Second, "")
	pool.newClient = func(config *ConnectionConfig) (*Client, error) {
		*created++
		return &Client{config: config, resolution: Resolution{Target: "root@1.2.3.4"}}, nil
	}
	return pool
}

// TestPoolRunSpawnsOneSSHProcess pins one ssh handshake per command: every ssh process opens its own connection
// (ControlPath=none), so a liveness ping before the command would double the handshakes and prove nothing about the
// connection the command then opens.
func TestPoolRunSpawnsOneSSHProcess(t *testing.T) {
	spawned := countSSHProcesses(t, 0)
	created := 0
	pool := newTestPool(&created)
	config := &ConnectionConfig{Address: "1.2.3.4", Port: 22, User: "root"}

	if _, err := pool.Run(context.Background(), config, "true"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(*spawned) != 1 {
		t.Fatalf("Pool.Run spawned %v, want exactly one ssh process", *spawned)
	}
	if err := pool.Upload(context.Background(), config, UploadOptions{LocalPath: "/local/file", RemotePath: "/remote/file"}); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if want := []string{"ssh", "ssh", "scp"}; strings.Join(*spawned, " ") != strings.Join(want, " ") {
		t.Fatalf("Run then Upload spawned %v, want %v", *spawned, want)
	}
}

// TestPoolRunRetriesOnceAfterConnectionError keeps the recovery the ping used to provide: a command whose connection
// is reset is retried once on a freshly resolved client.
func TestPoolRunRetriesOnceAfterConnectionError(t *testing.T) {
	spawned := countSSHProcesses(t, 1)
	created := 0
	pool := newTestPool(&created)
	config := &ConnectionConfig{Address: "1.2.3.4", Port: 22, User: "root"}

	result, err := pool.Run(context.Background(), config, "true")
	if err != nil {
		t.Fatalf("Run after a reset connection: %v", err)
	}
	if !strings.Contains(result.Stdout, "sh -c 'true'") {
		t.Fatalf("retried Run stdout = %q, want the command's output", result.Stdout)
	}
	if len(*spawned) != 2 || created != 2 {
		t.Fatalf("spawned %v with %d clients, want the failed attempt and one retry on a new client", *spawned, created)
	}
}

// TestPoolLimitPerHostSharesSlotsAcrossClientsOfOneAddress proves the per-host bound covers every client of one
// address, whatever user or key it connects with, and leaves other addresses alone.
func TestPoolLimitPerHostSharesSlotsAcrossClientsOfOneAddress(t *testing.T) {
	countSSHProcesses(t, 0)
	created := 0
	pool := newTestPool(&created)
	pool.LimitPerHost(1)
	first, err := pool.Get(&ConnectionConfig{Address: "1.2.3.4", Port: 22, User: "root"})
	if err != nil {
		t.Fatal(err)
	}
	sameHost, err := pool.Get(&ConnectionConfig{Address: "1.2.3.4", Port: 22, User: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	otherHost, err := pool.Get(&ConnectionConfig{Address: "5.6.7.8", Port: 22, User: "root"})
	if err != nil {
		t.Fatal(err)
	}

	first.slots <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The stand-in ssh echoes its argv, so empty stdout means no process ran.
	if result, err := sameHost.Run(ctx, "true"); !errors.Is(err, context.DeadlineExceeded) || result.Stdout != "" {
		t.Fatalf("Run on a saturated host = %v with stdout %q, want it to wait for a slot until its deadline", err, result.Stdout)
	}
	if result, err := otherHost.Run(context.Background(), "true"); err != nil || result.Stdout == "" {
		t.Fatalf("Run on another host = %v with stdout %q, want it unaffected", err, result.Stdout)
	}
	<-first.slots
	if result, err := sameHost.Run(context.Background(), "true"); err != nil || result.Stdout == "" {
		t.Fatalf("Run after the slot freed = %v with stdout %q", err, result.Stdout)
	}
}
