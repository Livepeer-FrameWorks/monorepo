package knowledge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/pkg/logging"

	"github.com/DATA-DOG/go-sqlmock"
)

// pageCacheSelectColumns matches the 13 columns returned by scanRow.
var pageCacheSelectColumns = []string{
	"tenant_id", "source_root", "page_url", "content_hash", "etag",
	"last_modified", "raw_size", "last_fetched_at", "sitemap_priority",
	"sitemap_changefreq", "consecutive_unchanged", "consecutive_failures", "source_type",
}

func TestSchedulerProcessesNewPage(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset>
				<url><loc>` + server.URL + `/page.html</loc></url></urlset>`))
		case "/page.html":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>T</title></head><body><p>Body.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)

	crawler, err := NewCrawler(server.Client(), embedder, store, WithPageCache(cache), withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	sitemapURL := server.URL + "/sitemap.xml"

	// 1. ListForTenant — no cached pages
	mock.ExpectQuery("SELECT .* FROM skipper\\.skipper_page_cache WHERE tenant_id").
		WithArgs("tenant").
		WillReturnRows(sqlmock.NewRows(pageCacheSelectColumns))

	// 2. BulkUpsert — persist metadata for the new page from sitemap
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// 3. processPage's Get — no cached entry for this page
	mock.ExpectQuery("SELECT .* FROM skipper\\.skipper_page_cache WHERE tenant_id.*AND page_url").
		WillReturnRows(sqlmock.NewRows(pageCacheSelectColumns))

	// 4. processPage's Upsert after embedding
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// 5. UpdateCrawlOutcome
	mock.ExpectExec("UPDATE skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 6. CleanupStale
	mock.ExpectExec("DELETE FROM skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 7. Crawl jobs cleanup
	mock.ExpectExec("DELETE FROM skipper\\.skipper_crawl_jobs").
		WillReturnResult(sqlmock.NewResult(0, 0))

	scheduler := NewCrawlScheduler(SchedulerConfig{
		Crawler:   crawler,
		DB:        db,
		PageCache: cache,
		Interval:  24 * time.Hour,
		TenantID:  "tenant",
		Sitemaps:  []string{sitemapURL},
		Logger:    logging.NewLoggerWithService("test"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler.runCycle(ctx)

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call (new page), got %d", embedder.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSchedulerProcessesStalePage(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset>
				<url><loc>` + server.URL + `/page.html</loc></url></urlset>`))
		case "/page.html":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>T</title></head><body><p>New content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)

	crawler, err := NewCrawler(server.Client(), embedder, store, WithPageCache(cache), withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	sitemapURL := server.URL + "/sitemap.xml"
	pageURL := server.URL + "/page.html"
	staleTime := time.Now().Add(-48 * time.Hour)

	// 1. ListForTenant — returns the stale cached page
	mock.ExpectQuery("SELECT .* FROM skipper\\.skipper_page_cache WHERE tenant_id").
		WithArgs("tenant").
		WillReturnRows(sqlmock.NewRows(pageCacheSelectColumns).AddRow(
			"tenant", sitemapURL, pageURL, "oldhash", nil, nil, nil, staleTime,
			0.5, nil, 0, 0, "sitemap",
		))

	// 2. No BulkUpsert needed — page already in cache

	// 3. processPage's Get — returns the stale cache entry
	mock.ExpectQuery("SELECT .* FROM skipper\\.skipper_page_cache WHERE tenant_id.*AND page_url").
		WillReturnRows(sqlmock.NewRows(pageCacheSelectColumns).AddRow(
			"tenant", sitemapURL, pageURL, "oldhash", nil, nil, nil, staleTime,
			0.5, nil, 0, 0, "sitemap",
		))

	// 4. processPage's Upsert after embedding (content changed)
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(1, 1))

	// 5. UpdateCrawlOutcome
	mock.ExpectExec("UPDATE skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(0, 1))

	// 6. CleanupStale
	mock.ExpectExec("DELETE FROM skipper\\.skipper_page_cache").
		WillReturnResult(sqlmock.NewResult(0, 0))

	// 7. Crawl jobs cleanup
	mock.ExpectExec("DELETE FROM skipper\\.skipper_crawl_jobs").
		WillReturnResult(sqlmock.NewResult(0, 0))

	scheduler := NewCrawlScheduler(SchedulerConfig{
		Crawler:   crawler,
		DB:        db,
		PageCache: cache,
		Interval:  24 * time.Hour,
		TenantID:  "tenant",
		Sitemaps:  []string{sitemapURL},
		Logger:    logging.NewLoggerWithService("test"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler.runCycle(ctx)

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call (stale page), got %d", embedder.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSchedulerLoadSourcesFromDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docs.txt"), []byte("https://docs.example.com/sitemap.xml\nhttps://guides.example.com/sitemap.xml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(result))
	}
	if result[0].url != "https://docs.example.com/sitemap.xml" || result[0].direct {
		t.Fatalf("unexpected first source: %+v", result[0])
	}
}

func TestSchedulerLoadSourcesDedup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("https://example.com/sitemap.xml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemaps:    []string{"https://example.com/sitemap.xml"},
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source (dedup), got %d", len(result))
	}
}

func TestSchedulerLoadSourcesCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	content := "# This is a comment\n\nhttps://example.com/sitemap.xml\n\n# Another comment\nhttps://other.com/sitemap.xml\n"
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(result))
	}
}

func TestSchedulerLoadSourcesMissingDir(t *testing.T) {
	s := &CrawlScheduler{
		sitemaps:    []string{"https://fallback.com/sitemap.xml"},
		sitemapsDir: "/nonexistent/path",
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source (env fallback), got %d", len(result))
	}
	if result[0].url != "https://fallback.com/sitemap.xml" {
		t.Fatalf("unexpected source: %s", result[0].url)
	}
}

func TestSchedulerLoadSourcesEnvExpansion(t *testing.T) {
	t.Setenv("TEST_SITEMAP_HOST", "https://docs.example.com")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("${TEST_SITEMAP_HOST}/sitemap.xml\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result))
	}
	if result[0].url != "https://docs.example.com/sitemap.xml" {
		t.Fatalf("expected expanded URL, got %q", result[0].url)
	}
}

func TestSchedulerLoadSourcesPagePrefix(t *testing.T) {
	dir := t.TempDir()
	content := "https://example.com/sitemap.xml\npage:https://obsproject.com/kb/quick-start-guide\npage:https://obsproject.com/kb/sources-guide\n"
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(result))
	}
	if result[0].url != "https://example.com/sitemap.xml" || result[0].direct {
		t.Fatalf("expected sitemap source, got %+v", result[0])
	}
	if result[1].url != "https://obsproject.com/kb/quick-start-guide" || !result[1].direct {
		t.Fatalf("expected direct page source, got %+v", result[1])
	}
	if result[2].url != "https://obsproject.com/kb/sources-guide" || !result[2].direct {
		t.Fatalf("expected direct page source, got %+v", result[2])
	}
}

func TestSchedulerLoadSourcesPageDedup(t *testing.T) {
	dir := t.TempDir()
	content := "page:https://example.com/page\npage:https://example.com/page\n"
	if err := os.WriteFile(filepath.Join(dir, "dup.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source (dedup), got %d", len(result))
	}
	if !result[0].direct {
		t.Fatal("expected direct page source")
	}
}

func TestSchedulerLoadSourcesPagePrefixEnvExpansion(t *testing.T) {
	t.Setenv("TEST_PAGE_HOST", "https://docs.example.com")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "test.txt"), []byte("page:${TEST_PAGE_HOST}/guide\n"), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result))
	}
	if result[0].url != "https://docs.example.com/guide" || !result[0].direct {
		t.Fatalf("expected expanded direct page, got %+v", result[0])
	}
}

func TestSchedulerLoadSourcesLocalPrefix(t *testing.T) {
	dir := t.TempDir()
	content := "https://example.com/sitemap.xml\nlocal:../faq/bitrate.md\nlocal:../faq/codec.md\n"
	if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 3 {
		t.Fatalf("expected 3 sources, got %d", len(result))
	}
	if result[0].url != "https://example.com/sitemap.xml" || result[0].localPath != "" {
		t.Fatalf("expected sitemap source, got %+v", result[0])
	}
	if result[1].localPath == "" {
		t.Fatalf("expected local source with localPath set, got %+v", result[1])
	}
	expectedPath := filepath.Clean(filepath.Join(dir, "../faq/bitrate.md"))
	if result[1].localPath != expectedPath {
		t.Fatalf("expected localPath %q, got %q", expectedPath, result[1].localPath)
	}
	if !strings.HasPrefix(result[1].url, "local://") {
		t.Fatalf("expected local:// URL scheme, got %q", result[1].url)
	}
	if result[1].direct || result[1].render {
		t.Fatalf("local source should not be direct or render: %+v", result[1])
	}
}

func TestSchedulerLoadSourcesLocalDedup(t *testing.T) {
	dir := t.TempDir()
	content := "local:../faq/file.md\nlocal:../faq/file.md\n"
	if err := os.WriteFile(filepath.Join(dir, "dup.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	s := &CrawlScheduler{
		sitemapsDir: dir,
		logger:      logging.NewLoggerWithService("test"),
	}
	result := s.loadSources()
	if len(result) != 1 {
		t.Fatalf("expected 1 source (dedup), got %d", len(result))
	}
}
