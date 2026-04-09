package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"frameworks/api_assets/internal/cache"
	"frameworks/pkg/logging"
)

type fakeS3 struct {
	data []byte
	err  error
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(strings.NewReader(string(f.data))),
	}, nil
}

func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.(prometheus.Metric).Write(&m); err != nil {
		return -1
	}
	return m.GetCounter().GetValue()
}

func newTestHandler(s3client S3Getter, prefix string) (*AssetHandler, prometheus.Counter, prometheus.Counter, prometheus.Counter) {
	hits := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_hits"})
	misses := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_misses"})
	s3errs := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_s3errs"})
	h := &AssetHandler{
		s3:          s3client,
		bucket:      "test-bucket",
		prefix:      prefix,
		cache:       cache.NewLRU(1024*1024, 5*time.Minute),
		logger:      logging.NewLoggerWithService("test"),
		cacheHits:   hits,
		cacheMisses: misses,
		s3Errors:    s3errs,
	}
	return h, hits, misses, s3errs
}

func init() {
	gin.SetMode(gin.TestMode)
}

func serveRequest(h *AssetHandler, urlPath string) *httptest.ResponseRecorder {
	router := gin.New()
	h.RegisterRoutes(router)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, urlPath, nil)
	router.ServeHTTP(w, req)
	return w
}

func TestFullKey_WithPrefix(t *testing.T) {
	h := &AssetHandler{prefix: "assets/v1"}
	got := h.fullKey("thumbnails/abc/poster.jpg")
	if got != "assets/v1/thumbnails/abc/poster.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestFullKey_WithTrailingSlash(t *testing.T) {
	h := &AssetHandler{prefix: "assets/v1/"}
	got := h.fullKey("thumbnails/abc/poster.jpg")
	if got != "assets/v1/thumbnails/abc/poster.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestFullKey_EmptyPrefix(t *testing.T) {
	h := &AssetHandler{prefix: ""}
	got := h.fullKey("thumbnails/abc/poster.jpg")
	if got != "thumbnails/abc/poster.jpg" {
		t.Fatalf("got %q", got)
	}
}

func TestHandleGetAsset_CacheMiss_S3Success(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, hits, misses, s3errs := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != "jpeg-data" {
		t.Fatalf("unexpected body: %q", w.Body.String())
	}
	if counterValue(hits) != 0 {
		t.Fatal("expected 0 cache hits")
	}
	if counterValue(misses) != 1 {
		t.Fatal("expected 1 cache miss")
	}
	if counterValue(s3errs) != 0 {
		t.Fatal("expected 0 s3 errors")
	}
}

func TestHandleGetAsset_CacheHit(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, hits, misses, _ := newTestHandler(fake, "")

	// First request populates cache
	serveRequest(h, "/assets/stream123/poster.jpg")
	// Second request should hit cache
	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if counterValue(hits) != 1 {
		t.Fatalf("expected 1 cache hit, got %v", counterValue(hits))
	}
	if counterValue(misses) != 1 {
		t.Fatalf("expected 1 cache miss (first request only), got %v", counterValue(misses))
	}
}

func TestHandleGetAsset_S3Error(t *testing.T) {
	fake := &fakeS3{err: fmt.Errorf("s3 connection refused")}
	h, _, _, s3errs := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 on S3 error, got %d", w.Code)
	}
	if counterValue(s3errs) != 1 {
		t.Fatal("expected 1 s3 error")
	}
}

func TestHandleGetAsset_DisallowedFile(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/malicious.exe")

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for disallowed file, got %d", w.Code)
	}
}

func TestHandleGetAsset_PathTraversal(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/..%2f..%2fetc/poster.jpg")

	if w.Code == http.StatusOK {
		t.Fatal("path traversal should not return 200")
	}
}

func TestHandleGetAsset_AllAllowedFiles(t *testing.T) {
	for file, expectedCT := range allowedFiles {
		t.Run(file, func(t *testing.T) {
			fake := &fakeS3{data: []byte("content")}
			h, _, _, _ := newTestHandler(fake, "")

			w := serveRequest(h, "/assets/key123/"+file)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", file, w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, strings.Split(expectedCT, ";")[0]) {
				t.Fatalf("expected content type starting with %q, got %q", expectedCT, ct)
			}
		})
	}
}

func TestHandleGetAsset_NoBucket(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")
	h.bucket = ""

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when bucket empty, got %d", w.Code)
	}
}

func TestHandleGetAsset_CacheControl(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	cc := w.Header().Get("Cache-Control")
	if cc != "public, max-age=30" {
		t.Fatalf("expected cache-control header, got %q", cc)
	}
}
