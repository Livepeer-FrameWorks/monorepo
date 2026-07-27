package handlers

import (
	"bytes"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

type fakeS3 struct {
	data    []byte
	err     error
	calls   int
	lastKey string
}

func (f *fakeS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.calls++
	if params != nil && params.Key != nil {
		f.lastKey = *params.Key
	}
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
		s3:             s3client,
		bucket:         "test-bucket",
		prefix:         prefix,
		serviceToken:   "test-token",
		cache:          cache.NewLRU(1024*1024, 5*time.Minute),
		logger:         logging.NewLoggerWithService("test"),
		cacheHits:      hits,
		cacheMisses:    misses,
		s3Errors:       s3errs,
		activeVersions: map[string]versionEntry{},
		httpClient:     &http.Client{Timeout: 2 * time.Second},
		// Default seam: an AUTHORITATIVE "no active version" (resolver reachable, asset never versioned) so the
		// cache/serving tests exercise the legacy-key path without a live resolver. Tests that need the real HTTP
		// pull (cold-miss / unreachable / no-version-over-HTTP) clear this and set foghornResolveURL.
		resolveVersionFn: func(_ context.Context, _ string) (string, bool, bool) { return "", true, false },
	}
	return h, hits, misses, s3errs
}

func init() {
	gin.SetMode(gin.TestMode)
}

func serveRequest(h *AssetHandler, urlPath string) *httptest.ResponseRecorder {
	return serveMethodRequest(h, http.MethodGet, urlPath)
}

func serveMethodRequest(h *AssetHandler, method, urlPath string) *httptest.ResponseRecorder {
	router := gin.New()
	h.RegisterRoutes(router)
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, urlPath, nil)
	router.ServeHTTP(w, req)
	return w
}

func serveJSONRequest(h *AssetHandler, method, urlPath, body, token string) *httptest.ResponseRecorder {
	router := gin.New()
	h.RegisterRoutes(router)
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), method, urlPath, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

func TestHandleHeadAsset_CacheMiss_S3Success(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, _, misses, s3errs := newTestHandler(fake, "")

	w := serveMethodRequest(h, http.MethodHead, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
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
	for file, expected := range allowedFiles {
		t.Run(file, func(t *testing.T) {
			fake := &fakeS3{data: []byte("content")}
			h, _, _, _ := newTestHandler(fake, "")

			w := serveRequest(h, "/assets/key123/"+file)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", file, w.Code)
			}
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, strings.Split(expected.contentType, ";")[0]) {
				t.Fatalf("expected content type starting with %q, got %q", expected.contentType, ct)
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

func TestHandleGetAsset_CORSHeaders(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/sprite.vtt")

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected wildcard CORS origin, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "Content-Range") {
		t.Fatalf("expected range headers exposed, got %q", got)
	}
}

func TestHandleAssetOptionsReturnsCORSPreflight(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveMethodRequest(h, http.MethodOptions, "/assets/stream123/sprite.vtt")

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "OPTIONS") {
		t.Fatalf("expected OPTIONS in allow methods, got %q", got)
	}
}

func TestHandleGetAsset_SpriteCacheControl(t *testing.T) {
	fake := &fakeS3{data: []byte("data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/sprite.jpg")

	cc := w.Header().Get("Cache-Control")
	if cc != "public, no-cache" {
		t.Fatalf("expected sprite cache-control header, got %q", cc)
	}
}

func TestHandleGetAsset_QueryDoesNotBypassServerCache(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, hits, misses, _ := newTestHandler(fake, "")

	serveRequest(h, "/assets/stream123/sprite.jpg?_fw_thumb=1")
	w := serveRequest(h, "/assets/stream123/sprite.jpg?_fw_thumb=2")

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if fake.calls != 1 {
		t.Fatalf("expected one S3 fetch, got %d", fake.calls)
	}
	if counterValue(hits) != 1 {
		t.Fatalf("expected 1 cache hit, got %v", counterValue(hits))
	}
	if counterValue(misses) != 1 {
		t.Fatalf("expected 1 cache miss, got %v", counterValue(misses))
	}
}

func TestHandleInvalidateCache_RequiresServiceToken(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveJSONRequest(h, http.MethodPost, "/internal/assets/cache/invalidate", `{"assetKey":"stream123"}`, "")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestHandleInvalidateCache_RemovesSelectedFiles(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}
	h, hits, misses, _ := newTestHandler(fake, "")

	serveRequest(h, "/assets/stream123/sprite.jpg")
	serveRequest(h, "/assets/stream123/sprite.vtt")
	w := serveJSONRequest(
		h,
		http.MethodPost,
		"/internal/assets/cache/invalidate",
		`{"assetKey":"stream123","files":["sprite.jpg"]}`,
		"test-token",
	)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	serveRequest(h, "/assets/stream123/sprite.jpg")
	serveRequest(h, "/assets/stream123/sprite.vtt")

	if fake.calls != 3 {
		t.Fatalf("expected 3 S3 fetches, got %d", fake.calls)
	}
	if counterValue(hits) != 1 {
		t.Fatalf("expected 1 cache hit, got %v", counterValue(hits))
	}
	if counterValue(misses) != 3 {
		t.Fatalf("expected 3 cache misses, got %v", counterValue(misses))
	}
}

// After a push (invalidation carrying activeVersion), a GET serves the immutable versioned object — while the
// public URL stays /assets/{assetKey}/{file}. The backing S3 key is thumbnails/{assetKey}/v/{version}/{file}.
func TestHandleGetAsset_ServesVersionedObjectAfterPush(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveJSONRequest(h, http.MethodPost, "/internal/assets/cache/invalidate",
		`{"assetKey":"stream-1","activeVersion":"v-abc","files":["poster.jpg"]}`, "test-token")
	if w.Code != http.StatusOK {
		t.Fatalf("push invalidate failed: %d %s", w.Code, w.Body.String())
	}

	if w := serveRequest(h, "/assets/stream-1/poster.jpg"); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if fake.lastKey != "thumbnails/stream-1/v/v-abc/poster.jpg" {
		t.Fatalf("expected versioned backing key, got %q", fake.lastKey)
	}
}

// With no pushed version, a cold GET pulls the active version from the in-cell Foghorn resolve endpoint and then
// serves the versioned object. A resolver that reports no version leaves the legacy un-versioned key.
func TestHandleGetAsset_ColdMissPullsVersionFromFoghorn(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")

	var gotAssetKey string
	foghorn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotAssetKey = r.URL.Query().Get("assetKey")
		_, _ = w.Write([]byte(`{"activeVersion":"v-xyz"}`))
	}))
	defer foghorn.Close()
	h.resolveVersionFn = nil // exercise the real HTTP pull
	h.foghornResolveURL = foghorn.URL

	if w := serveRequest(h, "/assets/stream-2/poster.jpg"); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if gotAssetKey != "stream-2" {
		t.Fatalf("Foghorn resolver was asked for %q", gotAssetKey)
	}
	if fake.lastKey != "thumbnails/stream-2/v/v-xyz/poster.jpg" {
		t.Fatalf("expected pulled versioned key, got %q", fake.lastKey)
	}
}

// A missing SERVICE_TOKEN (unusable resolver) must FAIL CLOSED, not be treated as positive proof that the asset
// has no version — otherwise Chandler serves a stale legacy object while Foghorn publishes versioned ones.
func TestHandleGetAsset_UnconfiguredResolverFailsClosed(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")
	h.resolveVersionFn = nil // no seam
	h.serviceToken = ""      // missing SERVICE_TOKEN → resolver unusable
	h.foghornResolveURL = ""

	w := serveRequest(h, "/assets/stream-unconfigured/poster.jpg")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unconfigured resolver (no SERVICE_TOKEN) must fail closed (503), got %d", w.Code)
	}
	if fake.lastKey != "" {
		t.Fatalf("must not fetch any S3 object when the resolver is unusable, fetched %q", fake.lastKey)
	}
}

// A GONE resolve (parent artifact terminal) must 404 and NOT serve a surviving legacy object, and must evict any
// cached versioned mapping.
func TestHandleGetAsset_GoneReturns404AndEvicts(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")
	// Seed a fresh cached versioned mapping, then have the resolver report GONE.
	h.versionsMu.Lock()
	h.activeVersions["stream-gone"] = versionEntry{version: "v1", resolvedAt: time.Now()}
	h.versionsMu.Unlock()
	h.resolveVersionFn = func(_ context.Context, _ string) (string, bool, bool) { return "", true, true }

	// Cache hit would serve v1 — but the resolver is only consulted on cold miss. Force a cold miss by expiring.
	h.versionsMu.Lock()
	h.activeVersions["stream-gone"] = versionEntry{version: "v1", resolvedAt: time.Now().Add(-time.Hour)}
	h.versionsMu.Unlock()

	w := serveRequest(h, "/assets/stream-gone/poster.jpg")
	if w.Code != http.StatusNotFound {
		t.Fatalf("a GONE asset must 404, got %d", w.Code)
	}
	if fake.lastKey != "" {
		t.Fatalf("a GONE asset must not fetch any S3 object (legacy or versioned), fetched %q", fake.lastKey)
	}
	// The cached mapping was evicted.
	h.versionsMu.RLock()
	_, still := h.activeVersions["stream-gone"]
	h.versionsMu.RUnlock()
	if still {
		t.Fatal("a GONE resolve must evict the cached versioned mapping")
	}
}

// P2: when the version resolver is CONFIGURED but unreachable and nothing is cached, Chandler fails closed (503)
// rather than serving a possibly-stale legacy object for what might be a versioned asset.
func TestHandleGetAsset_ResolverUnreachableFailsClosed(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")
	h.resolveVersionFn = nil                   // exercise the real HTTP pull
	h.foghornResolveURL = "http://127.0.0.1:1" // configured but connection-refused

	w := serveRequest(h, "/assets/stream-unreach/poster.jpg")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 fail-closed on an unreachable resolver, got %d", w.Code)
	}
	if fake.lastKey != "" {
		t.Fatalf("must not fetch any S3 object when the version is unresolvable, fetched %q", fake.lastKey)
	}
}

// A resolver that reports no version (never-published / migration) leaves Chandler serving the legacy key.
func TestHandleGetAsset_NoVersionServesLegacyKey(t *testing.T) {
	fake := &fakeS3{data: []byte("poster")}
	h, _, _, _ := newTestHandler(fake, "")

	foghorn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"activeVersion":""}`))
	}))
	defer foghorn.Close()
	h.resolveVersionFn = nil // exercise the real HTTP pull
	h.foghornResolveURL = foghorn.URL

	if w := serveRequest(h, "/assets/stream-3/poster.jpg"); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if fake.lastKey != "thumbnails/stream-3/poster.jpg" {
		t.Fatalf("expected legacy key fallback, got %q", fake.lastKey)
	}
}
