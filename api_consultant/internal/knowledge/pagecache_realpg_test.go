//go:build schema_verify

package knowledge

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

func TestPageCacheRepository_RealPG(t *testing.T) {
	db := startSkipperPageCacheRealPG(t)
	store := NewPageCacheStore(db)
	ctx := context.Background()
	tenantA := "10000000-0000-0000-0000-000000000001"
	tenantB := "20000000-0000-0000-0000-000000000002"
	now := time.Now().UTC().Truncate(time.Second)

	if err := store.BulkUpsert(ctx, []PageCache{
		{TenantID: tenantA, SourceRoot: "root-a", PageURL: "https://example.test/a", LastFetchedAt: now.Add(-48 * time.Hour), SitemapPriority: 0.8, SitemapChangeFreq: "daily"},
		{TenantID: tenantA, SourceRoot: "root-a", PageURL: "https://example.test/b", LastFetchedAt: now.Add(-time.Hour), SitemapPriority: 0.2},
		{TenantID: tenantB, SourceRoot: "root-b", PageURL: "https://example.test/a", LastFetchedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListForTenant(ctx, tenantA)
	if err != nil || len(rows) != 2 || rows[0].PageURL != "https://example.test/a" || rows[0].SitemapPriority != 0.8 {
		t.Fatalf("page cache = %#v, err = %v", rows, err)
	}
	if latest, err := store.LastFetchedForSource(ctx, tenantA, "root-a"); err != nil || latest == nil || !latest.Equal(now.Add(-time.Hour)) {
		t.Fatalf("last fetched = %v, err = %v", latest, err)
	}
	if latest, err := store.LastFetchedForSource(ctx, tenantA, "missing"); err != nil || latest != nil {
		t.Fatalf("missing last fetched = %v, err = %v", latest, err)
	}

	updated := PageCache{
		TenantID: tenantA, SourceRoot: "root-renamed", PageURL: "https://example.test/a",
		ContentHash: "hash", ETag: "etag", LastModified: "yesterday", RawSize: 123,
		LastFetchedAt: now, SourceType: "direct",
	}
	if err := store.Upsert(ctx, updated); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, tenantA, updated.PageURL)
	if err != nil || got.ContentHash != "hash" || got.RawSize != 123 || got.SourceRoot != "root-renamed" || got.SourceType != "sitemap" {
		t.Fatalf("updated page = %#v, err = %v", got, err)
	}
	if err := store.UpdateCrawlOutcome(ctx, tenantA, updated.PageURL, false, true); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, tenantA, updated.PageURL)
	if err != nil || got.ConsecutiveUnchanged != 1 || got.ConsecutiveFailures != 1 {
		t.Fatalf("failed outcome = %#v, err = %v", got, err)
	}
	if err := store.UpdateCrawlOutcome(ctx, tenantA, updated.PageURL, true, false); err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, tenantA, updated.PageURL)
	if err != nil || got.ConsecutiveUnchanged != 0 || got.ConsecutiveFailures != 0 {
		t.Fatalf("changed outcome = %#v, err = %v", got, err)
	}

	deleted, err := store.CleanupStale(ctx, tenantA, 24*time.Hour)
	if err != nil || deleted != 0 {
		t.Fatalf("cleanup deleted = %d, err = %v", deleted, err)
	}
	if err := store.DeleteBySource(ctx, tenantA, "root-a"); err != nil {
		t.Fatal(err)
	}
	if other, err := store.Get(ctx, tenantB, "https://example.test/a"); err != nil || other == nil {
		t.Fatalf("other tenant page = %#v, err = %v", other, err)
	}
}

func startSkipperPageCacheRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-skipper-pagecache-realpg-%d", time.Now().UnixNano())
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
