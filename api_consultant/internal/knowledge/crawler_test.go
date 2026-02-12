package knowledge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

type fakeStore struct {
	tenantID string
	upserted [][]Chunk
}

func (f *fakeStore) Upsert(_ context.Context, chunks []Chunk) error {
	f.upserted = append(f.upserted, chunks)
	return nil
}

func (f *fakeStore) DeleteBySource(_ context.Context, _, _ string) error {
	return nil
}

type fakeEmbedder struct {
	calls int32
	err   error // if set, EmbedDocument returns this error
}

func (f *fakeEmbedder) EmbedDocument(_ context.Context, url, title, content string) ([]Chunk, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.err != nil {
		return nil, f.err
	}
	return []Chunk{{SourceURL: url, SourceTitle: title, Text: content, Index: 0, Embedding: []float32{1}}}, nil
}

func (f *fakeEmbedder) callCount() int32 {
	return atomic.LoadInt32(&f.calls)
}

type fakeRenderer struct {
	html string
	err  error
}

func (f *fakeRenderer) Render(_ context.Context, _ string) (string, error) {
	return f.html, f.err
}

func (f *fakeRenderer) Close() {}

func TestCrawlerSitemapAndFetch(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex>
  <sitemap><loc>` + server.URL + `/sitemap-1.xml</loc></sitemap>
</sitemapindex>`))
		case "/sitemap-1.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
  <url><loc>` + server.URL + `/page2.html</loc></url>
</urlset>`))
		case "/page1.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page One</title></head><body><h1>Heading One</h1><p>Body copy.</p></body></html>`))
		case "/page2.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page Two</title></head><body><p>Second page.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pages, err := crawler.CrawlSitemap(context.Background(), server.URL+"/sitemap.xml")
	if err != nil {
		t.Fatalf("crawl sitemap: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}

	title, content, err := crawler.FetchPage(context.Background(), server.URL+"/page1.html")
	if err != nil {
		t.Fatalf("fetch page: %v", err)
	}
	if title != "Page One" {
		t.Fatalf("expected title, got %q", title)
	}
	if content == "" {
		t.Fatalf("expected content")
	}
}

func TestCrawlerCrawlAndEmbed(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
</urlset>`))
		case "/page1.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page</title></head><body><p>Body copy.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if embedder.callCount() != 1 {
		t.Fatalf("expected embedder to be called once, got %d", embedder.callCount())
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected store upserted once, got %d", len(store.upserted))
	}
}

func TestCrawlerCrawlAndEmbed_IdempotentWithCache(t *testing.T) {
	pageHTML := `<!doctype html><html><head><title>Page One</title></head><body><p>Body copy.</p></body></html>`
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
</urlset>`))
		case "/page1.html":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(pageHTML))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	title, content := extractContent([]byte(pageHTML), server.URL+"/page1.html")
	if title == "" || content == "" {
		t.Fatalf("expected extracted content for hash")
	}
	hash := contentHash(content)

	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)
	crawler, err := NewCrawler(server.Client(), embedder, store, WithPageCache(cache), withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pageURL := server.URL + "/page1.html"
	sitemapURL := server.URL + "/sitemap.xml"

	// First run: cache miss, embed once.
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}),
	)
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		"tenant", sitemapURL, pageURL,
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", sitemapURL, false); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}

	// Second run: cached hash match, no embedding.
	now := time.Now().UTC()
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}).
			AddRow("tenant", sitemapURL, pageURL, hash, nil, nil, nil, now),
	)
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		"tenant", sitemapURL, pageURL,
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", sitemapURL, false); err != nil {
		t.Fatalf("crawl and embed (second run): %v", err)
	}

	if embedder.callCount() != 1 {
		t.Fatalf("expected 1 embed call after idempotent re-crawl, got %d", embedder.callCount())
	}
	if len(store.upserted) != 1 {
		t.Fatalf("expected 1 upsert batch, got %d", len(store.upserted))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestFetchRenderedIncludesHeadMetadata(t *testing.T) {
	lastModified := time.Now().UTC().Format(http.TimeFormat)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("ETag", "W/\"etag-1\"")
			w.Header().Set("Last-Modified", lastModified)
			w.Header().Set("Content-Length", "42")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	crawler := &Crawler{
		client:    server.Client(),
		renderer:  &fakeRenderer{html: "<html><head><title>Title</title></head><body>Text</body></html>"},
		userAgent: "SkipperBot/1.0",
	}

	result, err := crawler.fetchRendered(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetch rendered: %v", err)
	}
	if result.ETag != "W/\"etag-1\"" {
		t.Fatalf("expected etag, got %q", result.ETag)
	}
	if result.LastMod != lastModified {
		t.Fatalf("expected last modified, got %q", result.LastMod)
	}
	if result.RawSize != 42 {
		t.Fatalf("expected raw size 42, got %d", result.RawSize)
	}
}

func TestCrawlerSkipsOn304(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset><url><loc>` + server.URL + `/page.html</loc></url></urlset>`))
		case "/page.html":
			if r.Header.Get("If-None-Match") == "\"etag-1\"" {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "\"etag-1\"")
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page</title></head><body><p>Content.</p></body></html>`))
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

	fakeS := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)
	crawler, err := NewCrawler(server.Client(), embedder, fakeS, WithPageCache(cache), withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pageURL := server.URL + "/page.html"

	// Return cached entry with matching ETag
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}).
			AddRow("tenant", server.URL+"/sitemap.xml", pageURL, "oldhash", "\"etag-1\"", nil, nil, time.Now().Add(-1*time.Hour)),
	)

	// After 304, update last_fetched_at
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if embedder.callCount() != 0 {
		t.Fatalf("expected 0 embed calls (304 skip), got %d", embedder.callCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCrawlerSkipsUnchangedContentHash(t *testing.T) {
	pageContent := `<!doctype html><html><head><title>Page</title></head><body><p>Static content.</p></body></html>`

	// Pre-compute the hash by running the same extraction pipeline the crawler uses
	precomputedHash := func() string {
		_, content := extractContent([]byte(pageContent), "https://example.com/page")
		return contentHash(content)
	}()

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset><url><loc>` + server.URL + `/page.html</loc></url></urlset>`))
		case "/page.html":
			_, _ = w.Write([]byte(pageContent))
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

	fakeS := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)
	crawler, err := NewCrawler(server.Client(), embedder, fakeS, WithPageCache(cache), withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pageURL := server.URL + "/page.html"

	// Return cached entry with matching content hash
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}).
			AddRow("tenant", server.URL+"/sitemap.xml", pageURL, precomputedHash, nil, nil, nil, time.Now().Add(-1*time.Hour)),
	)

	// After hash match, update cache
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if embedder.callCount() != 0 {
		t.Fatalf("expected 0 embed calls (hash match), got %d", embedder.callCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCrawlerSitemapLastmod(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page.html</loc><lastmod>2024-01-01T00:00:00Z</lastmod></url>
</urlset>`))
		case "/page.html":
			t.Fatal("should not fetch page — lastmod is older than cached")
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

	fakeS := &fakeStore{}
	embedder := &fakeEmbedder{}
	cache := NewPageCacheStore(db)
	crawler, err := NewCrawler(server.Client(), embedder, fakeS, WithPageCache(cache), withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pageURL := server.URL + "/page.html"

	// Return cached entry with LastFetchedAt after the sitemap lastmod
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}).
			AddRow("tenant", server.URL+"/sitemap.xml", pageURL, "hash", nil, nil, nil, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)),
	)

	if _, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false); err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if embedder.callCount() != 0 {
		t.Fatalf("expected 0 embed calls (lastmod skip), got %d", embedder.callCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestContentHash(t *testing.T) {
	h1 := contentHash("hello world")
	h2 := contentHash("hello world")
	h3 := contentHash("different content")

	if h1 != h2 {
		t.Fatalf("same content should produce same hash")
	}
	if h1 == h3 {
		t.Fatalf("different content should produce different hash")
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64-char hex string, got %d chars", len(h1))
	}
}

func TestCrawlDelayFloor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	parsed, _ := url.Parse(server.URL + "/sitemap.xml")
	rules := crawler.getRobotsRules(context.Background(), parsed)
	if rules.delay < defaultMinCrawlDelay {
		t.Fatalf("expected delay >= %v, got %v", defaultMinCrawlDelay, rules.delay)
	}
}

func TestCrawlDelayCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 3600"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation())
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	parsed, _ := url.Parse(server.URL + "/sitemap.xml")
	rules := crawler.getRobotsRules(context.Background(), parsed)
	if rules.delay > maxCrawlDelay {
		t.Fatalf("expected delay <= %v, got %v", maxCrawlDelay, rules.delay)
	}
}

func TestCrawlerContinuesOn404(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><urlset>
				<url><loc>` + server.URL + `/missing.html</loc></url>
				<url><loc>` + server.URL + `/page.html</loc></url>
			</urlset>`))
		case "/page.html":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>OK</title></head><body><p>Content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	_, err = crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false)
	if err != nil {
		t.Fatalf("expected no error (should skip 404), got: %v", err)
	}
	if embedder.callCount() != 1 {
		t.Fatalf("expected 1 embed call (skipped 404, embedded good page), got %d", embedder.callCount())
	}
}

func TestCrawlPagesEmbedsDirectURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/guide-a":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Guide A</title></head><body><p>Guide A content.</p></body></html>`))
		case "/guide-b":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Guide B</title></head><body><p>Guide B content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pages := []string{server.URL + "/guide-a", server.URL + "/guide-b"}
	if err := crawler.CrawlPages(context.Background(), "tenant", pages, false); err != nil {
		t.Fatalf("crawl pages: %v", err)
	}
	if embedder.callCount() != 2 {
		t.Fatalf("expected 2 embed calls, got %d", embedder.callCount())
	}
	if len(store.upserted) != 2 {
		t.Fatalf("expected 2 upserts, got %d", len(store.upserted))
	}
}

func TestCrawlPagesSkipsUnchangedHash(t *testing.T) {
	pageContent := `<!doctype html><html><head><title>Guide</title></head><body><p>Unchanged content.</p></body></html>`
	precomputedHash := func() string {
		_, content := extractContent([]byte(pageContent), "https://example.com/page")
		return contentHash(content)
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/guide":
			_, _ = w.Write([]byte(pageContent))
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
	crawler, err := NewCrawler(server.Client(), embedder, store, WithPageCache(cache), withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pageURL := server.URL + "/guide"

	// Return cached entry with matching content hash
	mock.ExpectQuery("SELECT tenant_id").WithArgs("tenant", pageURL).WillReturnRows(
		sqlmock.NewRows([]string{"tenant_id", "source_root", "page_url", "content_hash", "etag", "last_modified", "raw_size", "last_fetched_at"}).
			AddRow("tenant", "pagelist://direct", pageURL, precomputedHash, nil, nil, nil, time.Now().Add(-1*time.Hour)),
	)

	// After hash match, update cache
	mock.ExpectExec("INSERT INTO skipper\\.skipper_page_cache").WithArgs(
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
		sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(1, 1))

	pages := []string{pageURL}
	if err := crawler.CrawlPages(context.Background(), "tenant", pages, false); err != nil {
		t.Fatalf("crawl pages: %v", err)
	}
	if embedder.callCount() != 0 {
		t.Fatalf("expected 0 embed calls (hash match), got %d", embedder.callCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCrawlPagesRespectsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/guide":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Guide</title></head><body><p>Content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	pages := []string{server.URL + "/guide"}
	err = crawler.CrawlPages(ctx, "tenant", pages, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestParseSitemapPriority(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>https://example.com/low</loc><priority>0.3</priority><changefreq>monthly</changefreq></url>
  <url><loc>https://example.com/high</loc><priority>0.9</priority><changefreq>daily</changefreq></url>
  <url><loc>https://example.com/mid</loc><priority>0.5</priority></url>
  <url><loc>https://example.com/default</loc></url>
</urlset>`)

	_, pages, err := parseSitemapXML(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pages) != 4 {
		t.Fatalf("expected 4 pages, got %d", len(pages))
	}
	if pages[0].Priority != 0.3 || pages[0].ChangeFreq != "monthly" {
		t.Fatalf("unexpected first page: %+v", pages[0])
	}
	if pages[1].Priority != 0.9 || pages[1].ChangeFreq != "daily" {
		t.Fatalf("unexpected second page: %+v", pages[1])
	}
	if pages[2].Priority != 0.5 {
		t.Fatalf("unexpected third page priority: %f", pages[2].Priority)
	}
	if pages[3].Priority != 0 {
		t.Fatalf("expected default priority 0, got %f", pages[3].Priority)
	}
}

func TestExtractLinks(t *testing.T) {
	html := []byte(`<!doctype html><html><head><title>Test</title></head><body>
		<a href="/about">About</a>
		<a href="https://example.com/docs">Docs</a>
		<a href="https://other.com/ext">External</a>
		<a href="#section">Anchor</a>
		<a href="mailto:x@y.com">Email</a>
		<a href="/about">Duplicate</a>
		<a href="https://example.com/docs?q=1">Query stripped</a>
	</body></html>`)

	links := extractLinks(html, "https://example.com/page")
	expected := map[string]bool{
		"https://example.com/about": true,
		"https://example.com/docs":  true,
	}
	if len(links) != len(expected) {
		t.Fatalf("expected %d links, got %d: %v", len(expected), len(links), links)
	}
	for _, l := range links {
		if !expected[l] {
			t.Fatalf("unexpected link: %s", l)
		}
	}
}

func TestExtractLinksEmpty(t *testing.T) {
	links := extractLinks(nil, "https://example.com")
	if len(links) != 0 {
		t.Fatalf("expected 0 links from nil data, got %d", len(links))
	}
}

func TestCrawlPagesEmptyList(t *testing.T) {
	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(http.DefaultClient, embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	if err := crawler.CrawlPages(context.Background(), "tenant", nil, false); err != nil {
		t.Fatalf("expected nil error for empty list, got %v", err)
	}
	if embedder.callCount() != 0 {
		t.Fatalf("expected 0 embed calls, got %d", embedder.callCount())
	}
}

func TestCrawlAndEmbed_EmbedFailureContinues(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/page1.html</loc></url>
  <url><loc>` + server.URL + `/page2.html</loc></url>
</urlset>`))
		case "/page1.html":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page</title></head><body><p>Content one.</p></body></html>`))
		case "/page2.html":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Page</title></head><body><p>Content two.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{err: errors.New("API error")}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	result, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false)
	if err != nil {
		t.Fatalf("expected no fatal error, got: %v", err)
	}
	if result.Failed != 2 {
		t.Fatalf("expected 2 failed pages, got %d", result.Failed)
	}
	if result.Embedded != 0 {
		t.Fatalf("expected 0 embedded pages, got %d", result.Embedded)
	}
}

func TestCrawlAndEmbed_NoChunksTracked(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/tags</loc></url>
</urlset>`))
		case "/tags":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Tags</title></head><body><p>Thin content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{err: ErrNoChunks}
	crawler, err := NewCrawler(server.Client(), embedder, store, withSkipURLValidation(), WithMinCrawlDelay(0))
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	result, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false)
	if err != nil {
		t.Fatalf("expected no fatal error, got: %v", err)
	}
	if result.NoChunks != 1 {
		t.Fatalf("expected 1 no_chunks page, got %d", result.NoChunks)
	}
	if result.Failed != 0 {
		t.Fatalf("expected 0 failed pages (no_chunks is not a failure), got %d", result.Failed)
	}
}

func TestCrawlAndEmbed_ExcludePatterns(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<urlset>
  <url><loc>` + server.URL + `/tags</loc></url>
  <url><loc>` + server.URL + `/tags/foo</loc></url>
  <url><loc>` + server.URL + `/docs/guide</loc></url>
</urlset>`))
		case "/tags":
			t.Fatal("should not fetch excluded /tags")
		case "/tags/foo":
			t.Fatal("should not fetch excluded /tags/foo")
		case "/docs/guide":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Guide</title></head><body><p>Guide content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store,
		withSkipURLValidation(),
		WithMinCrawlDelay(0),
		WithExcludePatterns([]string{"/tags"}),
	)
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	result, err := crawler.CrawlAndEmbed(context.Background(), "tenant", server.URL+"/sitemap.xml", false)
	if err != nil {
		t.Fatalf("crawl and embed: %v", err)
	}
	if result.Excluded != 2 {
		t.Fatalf("expected 2 excluded pages, got %d", result.Excluded)
	}
	if result.Embedded != 1 {
		t.Fatalf("expected 1 embedded page, got %d", result.Embedded)
	}
	if embedder.callCount() != 1 {
		t.Fatalf("expected 1 embed call, got %d", embedder.callCount())
	}
}

func TestCrawlPages_ExcludePatterns(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nCrawl-delay: 0"))
		case "/tag/example":
			t.Fatal("should not fetch excluded /tag/example")
		case "/docs/guide":
			_, _ = w.Write([]byte(`<!doctype html><html><head><title>Guide</title></head><body><p>Guide content.</p></body></html>`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	store := &fakeStore{}
	embedder := &fakeEmbedder{}
	crawler, err := NewCrawler(server.Client(), embedder, store,
		withSkipURLValidation(),
		WithMinCrawlDelay(0),
		WithExcludePatterns([]string{"/tag/"}),
	)
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}

	pages := []string{server.URL + "/tag/example", server.URL + "/docs/guide"}
	if err := crawler.CrawlPages(context.Background(), "tenant", pages, false); err != nil {
		t.Fatalf("crawl pages: %v", err)
	}
	if embedder.callCount() != 1 {
		t.Fatalf("expected 1 embed call (excluded page skipped), got %d", embedder.callCount())
	}
}
