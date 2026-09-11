//go:build schema_verify

package knowledge

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_consultant/internal/database/skipperdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
)

func crawlReaperAPI(t *testing.T, db *sql.DB, now time.Time) *AdminAPI {
	t.Helper()
	return &AdminAPI{
		db:      db,
		queries: skipperdb.New(db),
		logger:  logging.NewLogger(),
		now:     func() time.Time { return now },
	}
}

func seedCrawlJob(t *testing.T, db *sql.DB, tenantID, sitemap, status string, startedAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO skipper.skipper_crawl_jobs (id, tenant_id, sitemap_url, status, started_at)
		VALUES ($1, $2, $3, $4, $5)`, id, tenantID, sitemap, status, startedAt); err != nil {
		t.Fatalf("seed crawl job: %v", err)
	}
	return id
}

func crawlJobStatus(t *testing.T, db *sql.DB, id string) (status string, finished bool) {
	t.Helper()
	var finishedAt sql.NullTime
	if err := db.QueryRow(`SELECT status, finished_at FROM skipper.skipper_crawl_jobs WHERE id=$1`, id).
		Scan(&status, &finishedAt); err != nil {
		t.Fatalf("read crawl job: %v", err)
	}
	return status, finishedAt.Valid
}

// A crawl is settled only by the tail of the in-memory goroutine running it, so
// a restart or OOM mid-crawl strands its row as 'running'. The retention cleanup
// cannot reap it — it deletes on finished_at, which is NULL — and CreateCrawlJob
// refuses while a crawl for that sitemap is running. So the stuck row rejects
// every later crawl of that sitemap with a conflict, permanently, and the next
// crawl is precisely what is blocked, so it can never self-heal.
func TestReapAbandonedCrawlJobsUnblocksTheSitemap_RealPG(t *testing.T) {
	db := startSkipperPageCacheRealPG(t)
	now := time.Now().UTC()
	tenantID, sitemap := uuid.NewString(), "https://example.test/sitemap.xml"

	// Owner died well past the crawl deadline.
	stranded := seedCrawlJob(t, db, tenantID, sitemap, "running", now.Add(-(maxCrawlDuration + crawlReapGrace + time.Hour)))

	api := crawlReaperAPI(t, db, now)
	// Before the sweep, a new crawl for the same sitemap is refused.
	rows, err := api.queries.CreateCrawlJob(context.Background(), skipperdb.CreateCrawlJobParams{
		ID: uuid.NewString(), TenantID: tenantID, SitemapUrl: sitemap, StartedAt: now,
	})
	if err != nil {
		t.Fatalf("create crawl job: %v", err)
	}
	if rows != 0 {
		t.Fatal("fixture is not blocking: a new crawl was admitted while one was stuck running")
	}

	api.reapAbandonedCrawlsOnce(context.Background())

	status, finished := crawlJobStatus(t, db, stranded)
	if status != "failed" || !finished {
		t.Fatalf("stranded job = status %q finished %v, want failed and finished", status, finished)
	}
	rows, err = api.queries.CreateCrawlJob(context.Background(), skipperdb.CreateCrawlJobParams{
		ID: uuid.NewString(), TenantID: tenantID, SitemapUrl: sitemap, StartedAt: now,
	})
	if err != nil {
		t.Fatalf("create crawl job after reap: %v", err)
	}
	if rows != 1 {
		t.Fatal("the sitemap is still blocked after reaping its abandoned job")
	}
}

// A crawl whose owner is alive settles itself. Reaping it would mark a running
// crawl failed underneath the process still working on it, so the cutoff sits
// past the crawl context's own deadline.
func TestReapAbandonedCrawlJobsLeavesLiveCrawlsAlone_RealPG(t *testing.T) {
	db := startSkipperPageCacheRealPG(t)
	now := time.Now().UTC()
	tenantID := uuid.NewString()

	justStarted := seedCrawlJob(t, db, tenantID, "https://a.test/sitemap.xml", "running", now.Add(-time.Minute))
	// Inside its deadline plus grace: the owner is still entitled to settle it.
	nearDeadline := seedCrawlJob(t, db, tenantID, "https://b.test/sitemap.xml", "running", now.Add(-(maxCrawlDuration - time.Minute)))

	crawlReaperAPI(t, db, now).reapAbandonedCrawlsOnce(context.Background())

	for _, id := range []string{justStarted, nearDeadline} {
		if status, finished := crawlJobStatus(t, db, id); status != "running" || finished {
			t.Fatalf("live crawl %s was reaped: status=%q finished=%v", id, status, finished)
		}
	}
}

// The sweep must not disturb a crawl that already reached a terminal state —
// most importantly a cancelled one, whose status the settling path deliberately
// preserves.
func TestReapAbandonedCrawlJobsLeavesSettledJobsAlone_RealPG(t *testing.T) {
	db := startSkipperPageCacheRealPG(t)
	now := time.Now().UTC()
	tenantID := uuid.NewString()
	old := now.Add(-(maxCrawlDuration + crawlReapGrace + time.Hour))

	cancelled := seedCrawlJob(t, db, tenantID, "https://c.test/sitemap.xml", "cancelled", old)
	completed := seedCrawlJob(t, db, tenantID, "https://d.test/sitemap.xml", "completed", old)

	crawlReaperAPI(t, db, now).reapAbandonedCrawlsOnce(context.Background())

	if status, _ := crawlJobStatus(t, db, cancelled); status != "cancelled" {
		t.Fatalf("cancelled job was overwritten: status=%q", status)
	}
	if status, _ := crawlJobStatus(t, db, completed); status != "completed" {
		t.Fatalf("completed job was overwritten: status=%q", status)
	}
}
