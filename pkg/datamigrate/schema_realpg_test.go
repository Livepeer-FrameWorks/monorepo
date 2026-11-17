//go:build schema_verify

package datamigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/yugabyte/pgx/v5/stdlib"
)

func TestConcurrentLedgerInitialization_RealPG(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	name := fmt.Sprintf("fw-datamigrate-schema-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("postgres: %v %s", runErr, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if readyErr := dockerpg.WaitReady(admin, name); readyErr != nil {
		t.Fatal(readyErr)
	}
	follower, err := sql.Open("pgx", dsn+"&application_name=ledger-follower")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = follower.Close() })
	follower.SetMaxOpenConns(1)
	waitBlocked := func(t *testing.T) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var blocked bool
			if queryErr := admin.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE application_name='ledger-follower' AND (wait_event_type='Lock' OR (state='idle' AND query LIKE '%pg_try_advisory_lock%')))`).Scan(&blocked); queryErr != nil {
				t.Fatal(queryErr)
			}
			if blocked {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("concurrent initializer never waited for the in-flight schema installation")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// An uncommitted installation is invisible to IF NOT EXISTS. A second
	// startup must wait for its commit instead of racing PostgreSQL's type catalog.
	first, err := admin.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback() //nolint:errcheck // the successful path commits
	if _, lockErr := first.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('frameworks_session_lock'),hashtext('datamigrate.schema'))`); lockErr != nil {
		t.Fatal(lockErr)
	}
	if _, schemaErr := first.ExecContext(ctx, SchemaSQL); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	done := make(chan error, 1)
	go func() { done <- EnsureSchema(ctx, follower) }()
	waitBlocked(t)
	if commitErr := first.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if secondErr := <-done; secondErr != nil {
		t.Fatalf("concurrent startup failed after the first installation committed: %v", secondErr)
	}
	if repeatErr := EnsureSchema(ctx, admin); repeatErr != nil {
		t.Fatal(repeatErr)
	}
	var tables int
	if readErr := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_tables WHERE schemaname=current_schema() AND tablename IN ('_data_migrations','_data_migration_runs')`).Scan(&tables); readErr != nil || tables != 2 {
		t.Fatalf("ledger incomplete: tables=%d err=%v", tables, readErr)
	}

	t.Run("waiting startup respects cancellation", func(t *testing.T) {
		holder, beginErr := admin.BeginTx(ctx, nil)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		defer holder.Rollback() //nolint:errcheck // release fixture lock
		if _, lockErr := holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('frameworks_session_lock'),hashtext('datamigrate.schema'))`); lockErr != nil {
			t.Fatal(lockErr)
		}
		waiting, stop := context.WithCancel(ctx)
		defer stop()
		go func() { done <- EnsureSchema(waiting, follower) }()
		waitBlocked(t)
		stop()
		if cancelled := <-done; !errors.Is(cancelled, context.Canceled) {
			t.Fatalf("cancelled schema wait=%v", cancelled)
		}
	})
}

func TestConcurrentLedgerInitialization_RealYugabyte(t *testing.T) {
	db, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "data_migration_schema", "bosun")
	if !ok {
		t.Fatal("requires the shared Yugabyte fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	const replicas = 8
	start := make(chan struct{})
	failures := make(chan error, replicas)
	var workers sync.WaitGroup
	for range replicas {
		workers.Add(1)
		go func() { defer workers.Done(); <-start; failures <- EnsureSchema(ctx, db) }()
	}
	close(start)
	workers.Wait()
	close(failures)
	for failure := range failures {
		if failure != nil {
			t.Errorf("concurrent ledger initialization: %v", failure)
		}
	}
	if err := EnsureSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
}
