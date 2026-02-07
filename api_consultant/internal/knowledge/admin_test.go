package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_consultant/internal/skipper"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newTestAdminAPI builds an AdminAPI with a sqlmock DB and minimal concrete
// dependencies. Store, Embedder, and Crawler require non-nil values in the
// constructor, so we wire them through the real DB (they won't be called in
// most tests that exercise handler-level logic).
func newTestAdminAPI(t *testing.T) (*AdminAPI, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}

	store := NewStore(db)

	// Embedder needs an EmbeddingClient; use a no-op implementation.
	embedder, err := NewEmbedder(&stubEmbeddingClient{})
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}

	crawler, err := NewCrawler(nil, embedder, store)
	if err != nil {
		t.Fatalf("crawler: %v", err)
	}

	logger := logrus.New()
	logger.SetOutput(&discardWriter{})

	api, err := NewAdminAPI(db, store, embedder, crawler, nil, logger)
	if err != nil {
		t.Fatalf("NewAdminAPI: %v", err)
	}
	return api, mock, db
}

type stubEmbeddingClient struct{}

func (s *stubEmbeddingClient) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = make([]float32, 3)
	}
	return vecs, nil
}

// discardWriter satisfies io.Writer for silencing logrus in tests.
type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// operatorRouter creates a gin.Engine that injects tenant ID and operator role
// into the request context, bypassing JWT middleware.
func operatorRouter(api *AdminAPI, tenantID string) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := skipper.WithTenantID(c.Request.Context(), tenantID)
		ctx = skipper.WithRole(ctx, "operator")
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})

	router.GET("/health", api.handleHealth)
	router.GET("/sources", api.handleListSources)
	router.POST("/crawl", api.handleCrawl)
	router.GET("/crawl/:id", api.handleCrawlStatus)
	return router
}

func TestHandleHealthNoTracker(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()

	router := gin.New()
	router.GET("/health", api.handleHealth)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	sources, ok := body["sources"].([]any)
	if !ok {
		t.Fatalf("expected sources array, got %T", body["sources"])
	}
	if len(sources) != 0 {
		t.Fatalf("expected empty sources, got %d", len(sources))
	}
}

func TestHandleHealthWithTracker(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()

	tracker := NewHealthTracker()
	tracker.RecordSuccess("https://example.com/sitemap.xml", 42)
	api.SetHealth(tracker)

	router := gin.New()
	router.GET("/health", api.handleHealth)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	sources, ok := body["sources"].([]any)
	if !ok {
		t.Fatalf("expected sources array, got %T", body["sources"])
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	src := sources[0].(map[string]any)
	if src["source"] != "https://example.com/sitemap.xml" {
		t.Errorf("unexpected source: %v", src["source"])
	}
	if int(src["pages_total"].(float64)) != 42 {
		t.Errorf("expected pages_total=42, got %v", src["pages_total"])
	}
}

func TestHandleListSources(t *testing.T) {
	api, mock, db := newTestAdminAPI(t)
	defer db.Close()

	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{"source_url", "page_count", "last_crawl_at"}).
		AddRow("https://docs.example.com", 5, now).
		AddRow("upload://readme.md", 1, nil)

	mock.ExpectQuery("SELECT.*FROM skipper\\.skipper_knowledge.*WHERE tenant_id").
		WithArgs("tenant-abc").
		WillReturnRows(rows)

	router := operatorRouter(api, "tenant-abc")
	req := httptest.NewRequest(http.MethodGet, "/sources?tenant_id=tenant-abc", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var body []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(body))
	}
	if body[0]["source_url"] != "https://docs.example.com" {
		t.Errorf("unexpected source_url: %v", body[0]["source_url"])
	}
	if int(body[0]["page_count"].(float64)) != 5 {
		t.Errorf("expected page_count=5, got %v", body[0]["page_count"])
	}
	if body[0]["last_crawl_at"] == nil {
		t.Error("expected last_crawl_at to be set for first source")
	}
	if body[1]["last_crawl_at"] != nil {
		t.Errorf("expected last_crawl_at to be nil for second source, got %v", body[1]["last_crawl_at"])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandleListSourcesMissingTenantID(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()

	router := gin.New()
	router.GET("/sources", api.handleListSources)

	req := httptest.NewRequest(http.MethodGet, "/sources", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleCrawlRejectsPrivateIP(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()

	tests := []struct {
		name string
		url  string
	}{
		{"localhost", "http://localhost/sitemap.xml"},
		{"127.0.0.1", "http://127.0.0.1/sitemap.xml"},
		{"private 10.x", "http://10.0.0.1/sitemap.xml"},
		{"private 192.168.x", "http://192.168.1.1/sitemap.xml"},
		{"metadata endpoint", "http://169.254.169.254/latest/meta-data/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := operatorRouter(api, "tenant-ssrf")
			body := `{"sitemap_url":"` + tt.url + `","tenant_id":"tenant-ssrf"}`
			req := httptest.NewRequest(http.MethodPost, "/crawl", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for %s, got %d: %s", tt.url, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "invalid sitemap url") {
				t.Errorf("expected SSRF rejection message, got: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleCrawlRejectsEmptySitemapURL(t *testing.T) {
	api, _, db := newTestAdminAPI(t)
	defer db.Close()

	router := operatorRouter(api, "tenant-a")
	body := `{"sitemap_url":"","tenant_id":"tenant-a"}`
	req := httptest.NewRequest(http.MethodPost, "/crawl", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCrawlStatusTenantIsolation(t *testing.T) {
	api, mock, db := newTestAdminAPI(t)
	defer db.Close()

	// The query must include both job ID and tenant_id, enforcing tenant isolation.
	mock.ExpectQuery("SELECT.*FROM skipper\\.skipper_crawl_jobs WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("job-123", "tenant-isolated").
		WillReturnError(sql.ErrNoRows)

	router := operatorRouter(api, "tenant-isolated")
	req := httptest.NewRequest(http.MethodGet, "/crawl/job-123", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing job with tenant isolation, got %d: %s", rec.Code, rec.Body.String())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestHandleCrawlStatusReturnsJob(t *testing.T) {
	api, mock, db := newTestAdminAPI(t)
	defer db.Close()

	started := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	finished := time.Date(2025, 1, 15, 10, 5, 0, 0, time.UTC)

	rows := sqlmock.NewRows([]string{"id", "tenant_id", "sitemap_url", "status", "error", "started_at", "finished_at"}).
		AddRow("job-456", "tenant-ok", "https://example.com/sitemap.xml", "completed", nil, started, finished)

	mock.ExpectQuery("SELECT.*FROM skipper\\.skipper_crawl_jobs WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("job-456", "tenant-ok").
		WillReturnRows(rows)

	router := operatorRouter(api, "tenant-ok")
	req := httptest.NewRequest(http.MethodGet, "/crawl/job-456", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var job CrawlJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatalf("json decode: %v", err)
	}
	if job.ID != "job-456" {
		t.Errorf("expected id=job-456, got %s", job.ID)
	}
	if job.Status != "completed" {
		t.Errorf("expected status=completed, got %s", job.Status)
	}
	if job.FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestCrawlSemaphoreLimiting(t *testing.T) {
	if maxConcurrentCrawls != 3 {
		t.Fatalf("test assumes maxConcurrentCrawls=3, got %d", maxConcurrentCrawls)
	}

	// Drain the global semaphore to a known empty state, then restore.
	// This avoids interference from other tests that might have left tokens.
	drainSem := func() int {
		drained := 0
		for {
			select {
			case <-crawlSem:
				drained++
			default:
				return drained
			}
		}
	}
	drained := drainSem()
	defer func() {
		for i := 0; i < drained; i++ {
			crawlSem <- struct{}{}
		}
	}()

	var running int32
	var maxRunning int32
	var wg sync.WaitGroup

	// Block channel keeps goroutines alive until we release them.
	block := make(chan struct{})

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			crawlSem <- struct{}{}
			cur := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&maxRunning)
				if cur <= old || atomic.CompareAndSwapInt32(&maxRunning, old, cur) {
					break
				}
			}
			<-block
			atomic.AddInt32(&running, -1)
			<-crawlSem
		}()
	}

	// Give goroutines time to acquire the semaphore.
	time.Sleep(50 * time.Millisecond)

	peak := atomic.LoadInt32(&maxRunning)
	if peak > int32(maxConcurrentCrawls) {
		t.Errorf("semaphore allowed %d concurrent, expected max %d", peak, maxConcurrentCrawls)
	}
	if peak != int32(maxConcurrentCrawls) {
		t.Logf("peak concurrent: %d (expected %d)", peak, maxConcurrentCrawls)
	}

	close(block)
	wg.Wait()

	final := atomic.LoadInt32(&maxRunning)
	if final > int32(maxConcurrentCrawls) {
		t.Errorf("max concurrent was %d, expected <= %d", final, maxConcurrentCrawls)
	}
}

func TestOperatorOnlyMiddleware(t *testing.T) {
	tests := []struct {
		role      string
		wantAllow bool
	}{
		{"admin", true},
		{"operator", true},
		{"provider", true},
		{"service", true},
		{"viewer", false},
		{"", false},
		{"user", false},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				ctx := skipper.WithRole(c.Request.Context(), tt.role)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			router.Use(operatorOnlyMiddleware())
			router.GET("/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if tt.wantAllow && rec.Code != http.StatusOK {
				t.Errorf("role %q: expected 200, got %d", tt.role, rec.Code)
			}
			if !tt.wantAllow && rec.Code != http.StatusForbidden {
				t.Errorf("role %q: expected 403, got %d", tt.role, rec.Code)
			}
		})
	}
}

func TestResolveTenantID(t *testing.T) {
	tests := []struct {
		name     string
		explicit string
		header   string
		ctxID    string
		role     string
		wantID   string
		wantOK   bool
	}{
		{"explicit wins (service)", "explicit-t", "header-t", "ctx-t", "service", "explicit-t", true},
		{"header fallback (service)", "", "header-t", "ctx-t", "service", "header-t", true},
		{"explicit ignored (non-service)", "explicit-t", "", "ctx-t", "admin", "ctx-t", true},
		{"context fallback", "", "", "ctx-t", "", "ctx-t", true},
		{"none", "", "", "", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			var gotID string
			var gotOK bool
			router.GET("/resolve", func(c *gin.Context) {
				ctx := c.Request.Context()
				if tt.ctxID != "" {
					ctx = skipper.WithTenantID(ctx, tt.ctxID)
				}
				if tt.role != "" {
					ctx = skipper.WithRole(ctx, tt.role)
				}
				c.Request = c.Request.WithContext(ctx)
				gotID, gotOK = resolveTenantID(c, tt.explicit)
			})

			req := httptest.NewRequest(http.MethodGet, "/resolve", nil)
			if tt.header != "" {
				req.Header.Set("X-Tenant-Id", tt.header)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if gotID != tt.wantID {
				t.Errorf("tenantID = %q, want %q", gotID, tt.wantID)
			}
			if gotOK != tt.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tt.wantOK)
			}
		})
	}
}
