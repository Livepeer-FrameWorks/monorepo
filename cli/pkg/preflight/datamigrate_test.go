package preflight

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func TestDataMigrationLedgerAbsent(t *testing.T) {
	t.Parallel()

	for _, output := range []string{
		`ERROR: relation "_data_migrations" does not exist (SQLSTATE 42P01)`,
		`pq: relation "_data_migrations" does not exist`,
		`{"error":"relation \"_data_migrations\" does not exist"}`,
	} {
		if !dataMigrationLedgerAbsent(output) {
			t.Fatalf("expected absent ledger classification for %q", output)
		}
	}
	for _, output := range []string{
		`ERROR: relation "quartermaster.tenants" does not exist`,
		`connection refused`,
		``,
	} {
		if dataMigrationLedgerAbsent(output) {
			t.Fatalf("unexpected absent ledger classification for %q", output)
		}
	}
}

// TestSSHStateSourceNamesHostOnFetchError pins that an unreadable state carries the host it was read from, so a gate
// refusal can name where the read failed. The host has no address, so the SSH client fails before any process starts.
func TestSSHStateSourceNamesHostOnFetchError(t *testing.T) {
	pool := ssh.NewPool(time.Second, "")
	defer pool.Close()
	hostFor := func(string) (inventory.Host, bool) { return inventory.Host{Name: "ctrl-1", User: "root"}, true }
	live := SSHStateSource(pool, hostFor, nil)(context.Background(), "quartermaster", "m1")
	if live.FetchError == nil {
		t.Fatalf("live = %+v, want a fetch error for a host without an address", live)
	}
	if live.Host != "ctrl-1" {
		t.Fatalf("live.Host = %q, want ctrl-1 (err: %v)", live.Host, live.FetchError)
	}
}

// blockingDetector answers only when its context ends, like an SSH session that never returns.
type blockingDetector struct{}

func (blockingDetector) Detect(ctx context.Context, _ string) (*detect.ServiceState, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestSSHStateSourceBoundsDetection pins that a deployment-mode detection that never answers ends at detectTimeout as
// a fetch error naming the service and host, instead of stalling the gate.
func TestSSHStateSourceBoundsDetection(t *testing.T) {
	prevMarker, prevDetector, prevTimeout := adoptionMarkerPresentFn, newServiceDetectorFn, detectTimeout
	t.Cleanup(func() {
		adoptionMarkerPresentFn, newServiceDetectorFn, detectTimeout = prevMarker, prevDetector, prevTimeout
	})
	adoptionMarkerPresentFn = func(context.Context, *ssh.Pool, inventory.Host, string) (bool, error) { return true, nil }
	newServiceDetectorFn = func(*ssh.Pool, inventory.Host) serviceDetector { return blockingDetector{} }
	detectTimeout = 50 * time.Millisecond

	hostFor := func(string) (inventory.Host, bool) {
		return inventory.Host{Name: "ctrl-1", ExternalIP: "192.0.2.10", User: "root"}, true
	}
	done := make(chan struct{})
	var live struct {
		err  error
		host string
	}
	go func() {
		defer close(done)
		got := SSHStateSource(nil, hostFor, nil)(context.Background(), "quartermaster", "m1")
		live.err, live.host = got.FetchError, got.Host
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SSHStateSource did not return while detection never answered")
	}
	if !errors.Is(live.err, context.DeadlineExceeded) || !strings.Contains(live.err.Error(), "detect quartermaster on ctrl-1") {
		t.Fatalf("FetchError = %v; want a deadline naming quartermaster on ctrl-1", live.err)
	}
	if live.host != "ctrl-1" {
		t.Fatalf("Host = %q, want ctrl-1", live.host)
	}
}
