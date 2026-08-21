//go:build schema_verify

package diagnostics

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestBaselineRepository_RealPG(t *testing.T) {
	db := startSkipperBaselinesRealPG(t)
	store := NewSQLBaselineStore(db)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"

	initial := map[string]BaselineMetric{
		"fps":     {Avg: 30, M2: 4, SampleCount: 10},
		"bitrate": {Avg: 5_000_000, M2: 100, SampleCount: 10},
	}
	if err := store.Upsert(ctx, tenantA, "stream-a", initial); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(ctx, tenantB, "stream-a", map[string]BaselineMetric{"fps": {Avg: 60, SampleCount: 1}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, tenantA, "stream-a")
	if err != nil || len(got) != 2 || got["fps"].Avg != 30 || got["bitrate"].SampleCount != 10 {
		t.Fatalf("baselines = %#v, err = %v", got, err)
	}
	if err := store.Upsert(ctx, tenantA, "stream-a", map[string]BaselineMetric{"fps": {Avg: 31, M2: 5, SampleCount: 11}}); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, tenantA, "stream-a")
	if err != nil || got["fps"].Avg != 31 || len(got) != 2 {
		t.Fatalf("updated baselines = %#v, err = %v", got, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE skipper.skipper_baselines SET updated_at = NOW() - INTERVAL '48 hours' WHERE tenant_id = $1::uuid AND metric_name = 'bitrate'", tenantA); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.CleanupStale(ctx, tenantA, 24*time.Hour)
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted = %d, err = %v", deleted, err)
	}
	other, err := store.Get(ctx, tenantB, "stream-a")
	if err != nil || len(other) != 1 || other["fps"].Avg != 60 {
		t.Fatalf("other tenant baselines = %#v, err = %v", other, err)
	}
}

func startSkipperBaselinesRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-baselines-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
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
	schema, err := dbsql.Content.ReadFile("schema/skipper.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}
