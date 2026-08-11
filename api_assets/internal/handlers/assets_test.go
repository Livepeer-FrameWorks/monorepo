package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"frameworks/api_assets/internal/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mediakeys"
)

type fakeS3 struct {
	data    []byte
	err     error
	bodyErr error // when set, the returned body fails mid-read with this error (GetObject itself succeeds)
	calls   int
	lastKey string
}

// errAfterReader yields prefix bytes, then fails — models a body stream that dies mid-transfer.
type errAfterReader struct {
	prefix []byte
	err    error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if len(r.prefix) > 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	return 0, r.err
}

func (f *fakeS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.calls++
	if params != nil && params.Key != nil {
		f.lastKey = *params.Key
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.bodyErr != nil {
		// GetObject SUCCEEDS; the failure surfaces while READING the body (errAfterReader), modelling a mid-body outage.
		//nolint:nilerr // intentional: the returned nil is the GetObject error; f.bodyErr is delivered via the body stream.
		return &s3.GetObjectOutput{Body: io.NopCloser(&errAfterReader{prefix: f.data, err: f.bodyErr})}, nil
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
		s3:           s3client,
		bucket:       "test-bucket",
		prefix:       prefix,
		serviceToken: "test-token",
		cache:        cache.NewLRU(1024*1024, 5*time.Minute),
		logger:       logging.NewLoggerWithService("test"),
		cacheHits:    hits,
		cacheMisses:  misses,
		s3Errors:     s3errs,
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

// A BACKEND failure (timeout / outage / credentials) is NOT "asset absent": it must be 503, never a 404 a CDN would
// cache. Only a typed NoSuchKey/NotFound is 404.
func TestHandleGetAsset_BackendFailureIs503(t *testing.T) {
	fake := &fakeS3{err: fmt.Errorf("s3 connection refused")}
	h, _, _, s3errs := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on a backend failure, got %d", w.Code)
	}
	if counterValue(s3errs) != 1 {
		t.Fatal("expected 1 s3 error")
	}
}

func TestHandleGetAsset_NoSuchKeyIs404(t *testing.T) {
	fake := &fakeS3{err: &s3types.NoSuchKey{}}
	h, _, _, _ := newTestHandler(fake, "")

	w := serveRequest(h, "/assets/stream123/poster.jpg")

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a typed NoSuchKey, got %d", w.Code)
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

// /ready proves ONLY that this instance can read its immutable backend: a reachable store is 200, an unreachable one
// is 503. No resolver, no Foghorn.
func TestHandleReady(t *testing.T) {
	fake := &fakeS3{data: []byte("jpeg-data")}

	t.Run("store reachable is 200", func(t *testing.T) {
		h, _, _, _ := newTestHandler(fake, "")
		h.storeReachableFn = func(context.Context) bool { return true }
		w := serveRequest(h, "/ready")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
		}
	})

	t.Run("store unreachable is 503", func(t *testing.T) {
		h, _, _, _ := newTestHandler(fake, "")
		h.storeReachableFn = func(context.Context) bool { return false }
		w := serveRequest(h, "/ready")
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when the store is unreachable, got %d body=%s", w.Code, w.Body.String())
		}
	})
}

// Readiness FULLY READS a KNOWN sentinel object (provisioned by Foghorn under the served namespace): a successful body
// read proves this instance can read the served namespace, and ANY failure — sentinel absent, AccessDenied, wrong
// bucket, bad credentials, transport, OR a body that fails mid-read/empty — is NOT ready. Reading a real object (not a
// missing one) to completion is what stops a denied/absent/truncated response from masquerading as ready.
func TestProbeStoreReachable_RequiresSentinelRead(t *testing.T) {
	t.Run("sentinel fully readable is ready and probes the sentinel key under the prefix", func(t *testing.T) {
		fake := &fakeS3{data: []byte("ready\n")}
		h, _, _, _ := newTestHandler(fake, "prod")
		if !h.probeStoreReachable(context.Background()) {
			t.Fatal("a fully-readable sentinel must be ready")
		}
		if want := "prod/" + mediakeys.ReadinessSentinelKey; fake.lastKey != want {
			t.Fatalf("probed key = %q, want %q (sentinel under served namespace + prefix)", fake.lastKey, want)
		}
	})

	t.Run("any GetObject error is not ready", func(t *testing.T) {
		for _, e := range []error{
			&s3types.NoSuchKey{},                          // sentinel absent
			&smithy.GenericAPIError{Code: "AccessDenied"}, // denied — must NOT pass (the false-open this fix closes)
			&smithy.GenericAPIError{Code: "NoSuchBucket"}, // wrong bucket
			errors.New("dial tcp: i/o timeout"),           // transport
		} {
			fake := &fakeS3{err: e}
			h, _, _, _ := newTestHandler(fake, "")
			if h.probeStoreReachable(context.Background()) {
				t.Fatalf("GetObject error %v must be NOT ready", e)
			}
		}
	})

	t.Run("a body that fails mid-read is not ready", func(t *testing.T) {
		fake := &fakeS3{data: []byte("re"), bodyErr: errors.New("connection reset mid-body")}
		h, _, _, _ := newTestHandler(fake, "")
		if h.probeStoreReachable(context.Background()) {
			t.Fatal("a mid-body read failure must be NOT ready (response headers alone do not prove a read)")
		}
	})

	t.Run("an empty body is not ready", func(t *testing.T) {
		fake := &fakeS3{data: []byte{}}
		h, _, _, _ := newTestHandler(fake, "")
		if h.probeStoreReachable(context.Background()) {
			t.Fatal("an empty sentinel body must be NOT ready")
		}
	})
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

// /assets honors the S3 prefix, mapping to prefix/thumbnails/{id}/{file}.
func TestHandleGetAsset_HonorsPrefix(t *testing.T) {
	fake := &fakeS3{data: []byte("x")}
	h, _, _, _ := newTestHandler(fake, "assets/v1")
	w := serveRequest(h, "/assets/id-9/poster.jpg")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if want := "assets/v1/thumbnails/id-9/poster.jpg"; fake.lastKey != want {
		t.Fatalf("key = %q, want %q", fake.lastKey, want)
	}
}

// The namespaced /static/{namespace}/… route is deliberately NOT registered: /assets is Chandler's sole public
// contract, so a /static request 404s at the router and never reaches S3.
func TestStaticRouteNotRegistered(t *testing.T) {
	fake := &fakeS3{data: []byte("x")}
	h, _, _, _ := newTestHandler(fake, "")
	w := serveRequest(h, "/static/thumbnails/stream-1/poster.jpg")
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unregistered /static route, got %d", w.Code)
	}
	if fake.calls != 0 {
		t.Fatalf("an unregistered route must not hit S3, got %d calls", fake.calls)
	}
}
