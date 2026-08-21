//go:build schema_verify

package heartbeat

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

func TestReportRepository_RealPG(t *testing.T) {
	db := startSkipperReportsRealPG(t)
	store := NewReportStore(db)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"

	first, err := store.Save(ctx, ReportRecord{
		ID: "report-a", TenantID: tenantA, Trigger: "heartbeat", Summary: "summary-a",
		MetricsReviewed: []string{"cpu", "latency"}, RootCause: "capacity",
		Recommendations: []Recommendation{{Text: "scale", Confidence: "high"}},
	})
	if err != nil || first.CreatedAt.IsZero() {
		t.Fatalf("first report = %#v, err = %v", first, err)
	}
	second, err := store.Save(ctx, ReportRecord{ID: "report-b", TenantID: tenantA, Trigger: "manual", Summary: "summary-b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(ctx, ReportRecord{ID: "report-other", TenantID: tenantB, Trigger: "manual", Summary: "other"}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetByID(ctx, tenantA, first.ID)
	if err != nil || len(got.MetricsReviewed) != 2 || len(got.Recommendations) != 1 || got.ReadAt != nil {
		t.Fatalf("report = %#v, err = %v", got, err)
	}
	if _, err := store.GetByID(ctx, tenantB, first.ID); err == nil {
		t.Fatal("cross-tenant report lookup succeeded")
	}
	if rows, err := store.ListByTenant(ctx, tenantA, 1); err != nil || len(rows) != 1 {
		t.Fatalf("limited reports = %#v, err = %v", rows, err)
	}
	if rows, total, err := store.ListByTenantPaginated(ctx, tenantA, 1, 1); err != nil || total != 2 || len(rows) != 1 {
		t.Fatalf("paged reports = %#v total=%d err=%v", rows, total, err)
	}
	if unread, err := store.UnreadCount(ctx, tenantA); err != nil || unread != 2 {
		t.Fatalf("unread = %d, err = %v", unread, err)
	}
	if count, err := store.MarkRead(ctx, tenantA, []string{first.ID, "report-other"}); err != nil || count != 1 {
		t.Fatalf("selected mark read count = %d, err = %v", count, err)
	}
	read, err := store.GetByID(ctx, tenantA, first.ID)
	if err != nil || read.ReadAt == nil {
		t.Fatalf("read report = %#v, err = %v", read, err)
	}
	if count, err := store.MarkRead(ctx, tenantA, nil); err != nil || count != 1 {
		t.Fatalf("all mark read count = %d, err = %v", count, err)
	}
	if unread, err := store.UnreadCount(ctx, tenantA); err != nil || unread != 0 {
		t.Fatalf("final unread = %d, err = %v", unread, err)
	}
	_ = second
}

func startSkipperReportsRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-reports-realpg-%d", time.Now().UnixNano())
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
