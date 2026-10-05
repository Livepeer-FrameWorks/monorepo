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
	"sync/atomic"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
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

// Readiness proves ONLY that this instance can read its immutable backend, from the recorded background probe: not
// ready before the first successful sentinel read, ready once one succeeded, and still ready through failures shorter
// than storeReadinessWindow. A store that stays unreadable past the window is not ready, and the next successful
// read restores readiness. No resolver, no Foghorn.
func TestStoreReadinessCheck(t *testing.T) {
	newHandler := func() (*AssetHandler, *time.Time) {
		h, _, _, _ := newTestHandler(&fakeS3{data: []byte("ready")}, "")
		now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
		h.now = func() time.Time { return now }
		return h, &now
	}
	stall := errors.New("get readiness sentinel: context deadline exceeded")

	t.Run("not ready before any sentinel read", func(t *testing.T) {
		h, _ := newHandler()
		if got := h.StoreReadinessCheck()(); got.Status != monitoring.StatusUnhealthy {
			t.Fatalf("expected unhealthy before the first probe, got %+v", got)
		}
	})

	t.Run("not ready while the sentinel was never readable, with the reason", func(t *testing.T) {
		h, _ := newHandler()
		h.recordStoreProbe(errors.New("AccessDenied"))
		got := h.StoreReadinessCheck()()
		if got.Status != monitoring.StatusUnhealthy || !strings.Contains(got.Message, "AccessDenied") {
			t.Fatalf("expected unhealthy naming the probe failure, got %+v", got)
		}
	})

	t.Run("a failure inside the window keeps the instance ready", func(t *testing.T) {
		h, now := newHandler()
		h.recordStoreProbe(nil)
		*now = now.Add(storeProbeInterval)
		h.recordStoreProbe(stall)
		*now = now.Add(storeProbeInterval)
		if got := h.StoreReadinessCheck()(); got.Status != monitoring.StatusHealthy {
			t.Fatalf("expected healthy after one failed read inside the window, got %+v", got)
		}
	})

	t.Run("a store refusing reads past the window is not ready until it reads again", func(t *testing.T) {
		h, now := newHandler()
		h.recordStoreProbe(nil)
		*now = now.Add(storeReadinessWindow + time.Second)
		h.recordStoreProbe(&smithy.GenericAPIError{Code: "AccessDenied", Fault: smithy.FaultClient})
		got := h.StoreReadinessCheck()()
		if got.Status != monitoring.StatusUnhealthy || !strings.Contains(got.Message, "AccessDenied") {
			t.Fatalf("expected unhealthy naming the refusal past the window, got %+v", got)
		}
		h.recordStoreProbe(nil)
		if got := h.StoreReadinessCheck()(); got.Status != monitoring.StatusHealthy {
			t.Fatalf("expected healthy after the sentinel reads again, got %+v", got)
		}
	})

	t.Run("a store stalled past the window is degraded, not unhealthy", func(t *testing.T) {
		h, now := newHandler()
		h.recordStoreProbe(nil)
		*now = now.Add(storeReadinessWindow + time.Second)
		h.recordStoreProbe(stall)
		got := h.StoreReadinessCheck()()
		if got.Status != monitoring.StatusDegraded || !strings.Contains(got.Message, "deadline exceeded") {
			t.Fatalf("expected degraded naming the stall past the window, got %+v", got)
		}
	})

	t.Run("the check never reads the store itself", func(t *testing.T) {
		h, _ := newHandler()
		h.storeProbeFn = func(context.Context) error {
			t.Fatal("StoreReadinessCheck must answer from the recorded probe, not read the store")
			return nil
		}
		h.recordStoreProbe(nil)
		if got := h.StoreReadinessCheck()(); got.Status != monitoring.StatusHealthy {
			t.Fatalf("expected healthy, got %+v", got)
		}
	})
}

// RunStoreProbe reads the sentinel as soon as it starts, so readiness does not wait a full interval after boot.
func TestRunStoreProbeReadsImmediately(t *testing.T) {
	h, _, _, _ := newTestHandler(&fakeS3{data: []byte("ready")}, "")
	probed := make(chan struct{}, 1)
	h.storeProbeFn = func(context.Context) error {
		select {
		case probed <- struct{}{}:
		default:
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.RunStoreProbe(ctx); close(done) }()
	select {
	case <-probed:
	case <-time.After(time.Second):
		t.Fatal("RunStoreProbe did not probe on start")
	}
	cancel()
	<-done
	if got := h.StoreReadinessCheck()(); got.Status != monitoring.StatusHealthy {
		t.Fatalf("expected healthy after the start-up probe, got %+v", got)
	}
}

// The service router owns /ready, so the asset routes must not register it; a second registration panics in gin.
func TestRegisterRoutesLeavesReadyToServiceRouter(t *testing.T) {
	h, _, _, _ := newTestHandler(&fakeS3{data: []byte("x")}, "")
	if w := serveRequest(h, "/ready"); w.Code != http.StatusNotFound {
		t.Fatalf("expected RegisterRoutes to leave /ready unregistered, got %d", w.Code)
	}
}

// Readiness FULLY READS a KNOWN sentinel object (provisioned by Foghorn under the served namespace): a successful body
// read proves this instance can read the served namespace, and ANY failure — sentinel absent, AccessDenied, wrong
// bucket, bad credentials, transport, OR a body that fails mid-read/empty — is NOT ready. Reading a real object (not a
// missing one) to completion is what stops a denied/absent/truncated response from masquerading as ready.
func TestProbeStore_RequiresSentinelRead(t *testing.T) {
	t.Run("sentinel fully readable is ready and probes the sentinel key under the prefix", func(t *testing.T) {
		fake := &fakeS3{data: []byte("ready\n")}
		h, _, _, _ := newTestHandler(fake, "prod")
		if err := h.probeStore(context.Background()); err != nil {
			t.Fatalf("a fully-readable sentinel must be ready: %v", err)
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
			if h.probeStore(context.Background()) == nil {
				t.Fatalf("GetObject error %v must be NOT ready", e)
			}
		}
	})

	t.Run("a body that fails mid-read is not ready", func(t *testing.T) {
		fake := &fakeS3{data: []byte("re"), bodyErr: errors.New("connection reset mid-body")}
		h, _, _, _ := newTestHandler(fake, "")
		if h.probeStore(context.Background()) == nil {
			t.Fatal("a mid-body read failure must be NOT ready (response headers alone do not prove a read)")
		}
	})

	t.Run("an empty body is not ready", func(t *testing.T) {
		fake := &fakeS3{data: []byte{}}
		h, _, _, _ := newTestHandler(fake, "")
		if h.probeStore(context.Background()) == nil {
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

// A sentinel read failure past the readiness window is classified by what the real S3 client returned. A store that
// is slow or failing server-side (5xx, throttling, a read that outlives the probe deadline) affects every replica
// reading it, so the instance stays ready as degraded and Quartermaster keeps the pool in rotation. A store that
// refuses this instance (403 AccessDenied, 404 NoSuchKey/NoSuchBucket) is this instance's configuration and makes
// it unhealthy.
func TestStoreReadinessClassifiesRealStoreFailures(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		hang   bool
		want   string
	}{
		{name: "probe deadline exceeded", hang: true, want: monitoring.StatusDegraded},
		{name: "503 SlowDown", status: http.StatusServiceUnavailable, code: "SlowDown", want: monitoring.StatusDegraded},
		{name: "500 InternalError", status: http.StatusInternalServerError, code: "InternalError", want: monitoring.StatusDegraded},
		{name: "403 AccessDenied", status: http.StatusForbidden, code: "AccessDenied", want: monitoring.StatusUnhealthy},
		{name: "404 NoSuchBucket", status: http.StatusNotFound, code: "NoSuchBucket", want: monitoring.StatusUnhealthy},
		{name: "404 NoSuchKey", status: http.StatusNotFound, code: "NoSuchKey", want: monitoring.StatusUnhealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var failing atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !failing.Load() {
					_, _ = w.Write([]byte("ready"))
					return
				}
				if tc.hang {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>` + tc.code + `</Code><Message>x</Message></Error>`))
			}))
			defer srv.Close()
			h, err := NewAssetHandler(S3Config{
				Bucket: "assets", Region: "us-east-1", Endpoint: srv.URL, AccessKey: "a", SecretKey: "s",
			}, cache.NewLRU(16, time.Minute), logging.NewLoggerWithService("chandler-test"),
				prometheus.NewCounter(prometheus.CounterOpts{Name: "classify_hits"}),
				prometheus.NewCounter(prometheus.CounterOpts{Name: "classify_misses"}),
				prometheus.NewCounter(prometheus.CounterOpts{Name: "classify_errors"}))
			if err != nil {
				t.Fatalf("NewAssetHandler: %v", err)
			}
			now := time.Date(2026, 10, 5, 18, 8, 0, 0, time.UTC)
			h.now = func() time.Time { return now }
			h.recordStoreProbe(h.probeStore(context.Background()))
			failing.Store(true)
			now = now.Add(storeReadinessWindow + time.Second)
			probeErr := h.probeStore(context.Background())
			if probeErr == nil {
				t.Fatal("expected the failing store to fail the probe")
			}
			h.recordStoreProbe(probeErr)
			if got := h.StoreReadinessCheck()(); got.Status != tc.want {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Message, tc.want)
			}
		})
	}
}
