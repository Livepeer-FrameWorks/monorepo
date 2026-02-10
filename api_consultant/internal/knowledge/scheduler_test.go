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

func TestSchedulerSkipsFreshSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request to %s — should have been skipped by TTL", r.URL.Path)
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

	recent := time.Now().Add(-5 * time.Minute)
	rows := sqlmock.NewRows([]string{"max"}).AddRow(recent)
	mock.ExpectQuery("SELECT MAX").WithArgs("tenant", sitemapURL).WillReturnRows(rows)

	scheduler := NewCrawlScheduler(SchedulerConfig{
		Crawler:   crawler,
		PageCache: cache,
		Interval:  24 * time.Hour,
		TenantID:  "tenant",
		Sitemaps:  []string{sitemapURL},
		Logger:    logging.NewLoggerWithService("test"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler.runCycle(ctx, true)

	if embedder.calls != 0 {
		t.Fatalf("expected 0 embed calls (TTL skip), got %d", embedder.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSchedulerCrawlsStaleSource(t *testing.T) {
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

	// LastFetchedForSource returns stale time
	stale := time.Now().Add(-48 * time.Hour)
	mock.ExpectQuery("SELECT MAX").WithArgs("tenant", sitemapURL).WillReturnRows(
		sqlmock.NewRows([]string{"max"}).AddRow(stale),
	)

	// CrawlAndEmbed will call pageCache.Get for the page URL — return no rows
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}),
	)

	// CrawlAndEmbed will call pageCache.Upsert after embedding
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	scheduler := NewCrawlScheduler(SchedulerConfig{
		Crawler:   crawler,
		PageCache: cache,
		Interval:  24 * time.Hour,
		TenantID:  "tenant",
		Sitemaps:  []string{sitemapURL},
		Logger:    logging.NewLoggerWithService("test"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler.runCycle(ctx, true)

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call (stale source), got %d", embedder.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestSchedulerNeverCrawledSource(t *testing.T) {
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

	// LastFetchedForSource returns NULL (never crawled)
	mock.ExpectQuery("SELECT MAX").WithArgs("tenant", sitemapURL).WillReturnRows(
		sqlmock.NewRows([]string{"max"}).AddRow(nil),
	)

	// CrawlAndEmbed will call pageCache.Get — no rows
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", sqlmock.AnyArg()).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}),
	)

	// Upsert after embedding
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	scheduler := NewCrawlScheduler(SchedulerConfig{
		Crawler:   crawler,
		PageCache: cache,
		Interval:  24 * time.Hour,
		TenantID:  "tenant",
		Sitemaps:  []string{sitemapURL},
		Logger:    logging.NewLoggerWithService("test"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler.runCycle(ctx, true)

	if embedder.calls != 1 {
		t.Fatalf("expected 1 embed call (never crawled), got %d", embedder.calls)
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

func TestSchedulerLoadSourcesNoEnvExpansion(t *testing.T) {
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
	// Env vars are NOT expanded (security hardening) — literal string preserved.
	if result[0].url != "${TEST_SITEMAP_HOST}/sitemap.xml" {
		t.Fatalf("expected literal URL (no env expansion), got %q", result[0].url)
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

func TestSchedulerLoadSourcesPagePrefixNoEnvExpansion(t *testing.T) {
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
	// Env vars are NOT expanded (security hardening) — literal string preserved.
	if result[0].url != "${TEST_PAGE_HOST}/guide" || !result[0].direct {
		t.Fatalf("expected literal direct page (no env expansion), got %+v", result[0])
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
	// First should be a sitemap source.
	if result[0].url != "https://example.com/sitemap.xml" || result[0].localPath != "" {
		t.Fatalf("expected sitemap source, got %+v", result[0])
	}
	// Second and third should be local file sources.
	if result[1].localPath == "" {
		t.Fatalf("expected local source with localPath set, got %+v", result[1])
	}
	// Path should be resolved relative to sitemapsDir.
	expectedPath := filepath.Clean(filepath.Join(dir, "../faq/bitrate.md"))
	if result[1].localPath != expectedPath {
		t.Fatalf("expected localPath %q, got %q", expectedPath, result[1].localPath)
	}
	// URL should use local:// scheme.
	if !strings.HasPrefix(result[1].url, "local://") {
		t.Fatalf("expected local:// URL scheme, got %q", result[1].url)
	}
	// Should not be marked as direct or render.
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
