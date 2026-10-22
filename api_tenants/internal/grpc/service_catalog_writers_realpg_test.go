//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"frameworks/api_tenants/internal/bootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

func TestServiceCatalogWritersConverge_RealPG(t *testing.T) {
	name := fmt.Sprintf("fw-qm-service-catalog-writers-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	applyQuartermasterBaseline(t, db)
	verifyServiceCatalogWritersConverge(t, db)
}

func TestServiceCatalogWritersConverge_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_service_catalog_writers", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyServiceCatalogWritersConverge(t, db)
}

// A service's catalog row has two writers that run at the same time: the
// service's own BootstrapService call and the provisioning reconciler's
// service_registry entry for it (purser is both). Whichever inserts second,
// while the first is still uncommitted, must end on the one row instead of a
// unique-key failure.
func verifyServiceCatalogWritersConverge(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url)
		VALUES ('catalog-writers', 'Catalog writers', 'central', 'https://catalog-writers.example');
		INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type, wireguard_ip)
		VALUES ('catalog-writers-1', 'catalog-writers', 'catalog-writers-1', 'core', '10.88.0.1');
	`); err != nil {
		t.Fatal(err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	entry := func(service string) []bootstrap.ServiceRegistryEntry {
		return []bootstrap.ServiceRegistryEntry{{
			ServiceName: service, Type: service, Protocol: "http",
			ClusterID: "catalog-writers", NodeID: "catalog-writers-1", Port: 18003,
		}}
	}
	reconcile := func(service string) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := bootstrap.ReconcileServiceRegistry(ctx, tx, entry(service)); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}

	t.Run("BootstrapService inserts while the reconciler's row is uncommitted", func(t *testing.T) {
		const service = "catalog-writer-a"
		holder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := bootstrap.ReconcileServiceRegistry(ctx, holder, entry(service)); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			serviceID, ensureErr := server.ensureServiceExists(ctx, service, "http")
			if ensureErr == nil && serviceID != service {
				ensureErr = fmt.Errorf("service id %q, want %q", serviceID, service)
			}
			done <- ensureErr
		}()
		waitForCatalogInsert(t, db, done)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("BootstrapService catalog ensure after the reconciler committed the same row: %v", err)
		}
		assertOneCatalogRow(t, db, service)
	})

	t.Run("the reconciler inserts while BootstrapService's row is uncommitted", func(t *testing.T) {
		const service = "catalog-writer-b"
		holder, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Rollback() }()
		if _, err := server.ensureServiceExistsTx(ctx, holder, service, "grpc"); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- reconcile(service) }()
		waitForCatalogInsert(t, db, done)
		if err := holder.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("service_registry reconcile after BootstrapService committed the same row: %v", err)
		}
		assertOneCatalogRow(t, db, service)
		// The reconciler's declared protocol wins over the row BootstrapService created.
		var protocol string
		if err := db.QueryRowContext(ctx, `SELECT protocol FROM quartermaster.services WHERE service_id = $1`, service).Scan(&protocol); err != nil {
			t.Fatal(err)
		}
		if protocol != "http" {
			t.Fatalf("protocol %q after the reconciler converged the row, want http", protocol)
		}
	})
}

// waitForCatalogInsert returns once the second writer is blocked in its insert
// on the first writer's uncommitted row, or has already finished.
func waitForCatalogInsert(t *testing.T, db *sql.DB, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(done) > 0 {
			return
		}
		var waiting int
		if err := db.QueryRow(`
			SELECT count(*) FROM pg_stat_activity
			WHERE state = 'active' AND pid <> pg_backend_pid()
			  AND query ILIKE '%INSERT INTO quartermaster.services%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			time.Sleep(200 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the second catalog writer never reached its insert")
}

func assertOneCatalogRow(t *testing.T, db *sql.DB, service string) {
	t.Helper()
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM quartermaster.services WHERE service_id = $1 OR name = $1`, service).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d catalog rows for %s, want 1", rows, service)
	}
}
