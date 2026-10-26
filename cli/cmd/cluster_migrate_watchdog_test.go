package cmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/watchdog"
)

// paceMigrator stands in for an engine's migrate role. The postgres role reports progress every few milliseconds
// for busyFor, then either returns or, with stallAfter set, stops reporting and blocks until its context ends. The
// clickhouse role records whether its context was still live when it started.
type paceMigrator struct {
	engine     string
	busyFor    time.Duration
	stallAfter bool
	mu         *sync.Mutex
	started    map[string]error
}

func (m paceMigrator) ApplyMigrations(ctx context.Context, _ inventory.Host, _ provisioner.ServiceConfig, _ bool) error {
	m.mu.Lock()
	m.started[m.engine] = ctx.Err()
	m.mu.Unlock()
	if m.engine != "postgres" {
		return ctx.Err()
	}
	deadline := time.Now().Add(m.busyFor)
	for i := 1; time.Now().Before(deadline); i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		watchdog.Reportf(ctx, "TASK [Apply migration (Yugabyte)] item %d", i)
		time.Sleep(5 * time.Millisecond)
	}
	if m.stallAfter {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func installPaceMigrator(t *testing.T, busyFor time.Duration, stallAfter bool) map[string]error {
	t.Helper()
	orig := migratorForFn
	t.Cleanup(func() { migratorForFn = orig })
	started := map[string]error{}
	var mu sync.Mutex
	migratorForFn = func(serviceName string, _ *ssh.Pool) (provisioner.Migrator, error) {
		return paceMigrator{engine: serviceName, busyFor: busyFor, stallAfter: stallAfter, mu: &mu, started: started}, nil
	}
	return started
}

func shortMigrateStallWindow(t *testing.T, d time.Duration) {
	t.Helper()
	orig := migrateStallWindow
	t.Cleanup(func() { migrateStallWindow = orig })
	migrateStallWindow = d
}

// A Postgres branch that runs far longer than the stall window while it keeps reporting progress completes, and the
// ClickHouse branch after it starts with a live context.
func TestMigrateRunsPastStallWindowWhileBranchReportsProgress(t *testing.T) {
	rc := pendingExpandReleaseCluster(t)
	shortMigrateStallWindow(t, 80*time.Millisecond)
	started := installPaceMigrator(t, 400*time.Millisecond, false)
	cmd, _ := commandWithOutput()

	if err := runMigrate(cmd, rc, false, "expand", true, newDatabaseReleaseTarget, true, false); err != nil {
		t.Fatalf("migrate with a branch reporting progress throughout failed: %v", err)
	}
	if err, ok := started["clickhouse"]; !ok || err != nil {
		t.Fatalf("clickhouse branch started=%t with context error %v, want it started with a live context", ok, err)
	}
}

// A Postgres branch that stops reporting progress is cancelled after the stall window with an error naming the
// stalled branch and its last progress, and the ClickHouse branch still runs on its own context.
func TestMigrateCancelsStalledBranchWithoutStarvingTheNext(t *testing.T) {
	rc := pendingExpandReleaseCluster(t)
	shortMigrateStallWindow(t, 80*time.Millisecond)
	started := installPaceMigrator(t, 30*time.Millisecond, true)
	cmd, _ := commandWithOutput()

	err := runMigrate(cmd, rc, false, "expand", true, newDatabaseReleaseTarget, true, false)
	if err == nil {
		t.Fatal("migrate passed with a stalled postgres branch")
	}
	for _, want := range []string{"postgres migrations stalled", "no progress for 80ms", "TASK [Apply migration (Yugabyte)] item"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("migrate error %q does not contain %q", err, want)
		}
	}
	if err, ok := started["clickhouse"]; !ok || err != nil {
		t.Fatalf("clickhouse branch started=%t with context error %v after the postgres stall, want its own live context", ok, err)
	}
}
