package relay

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"frameworks/api_sidecar/internal/admission"
)

// sequenceResolver hands out its results in order, the last one repeatedly,
// stamped as freshly resolved the way controlResolver stamps them. Like
// controlResolver, it fails a resolve whose context has ended.
type sequenceResolver struct {
	mu    sync.Mutex
	out   []*ResolveResult
	calls int
}

func (r *sequenceResolver) Resolve(rc ResolveContext) (*ResolveResult, error) {
	if rc.Ctx != nil && rc.Ctx.Err() != nil {
		return nil, rc.Ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := min(r.calls, len(r.out)-1)
	r.calls++
	res := *r.out[idx]
	res.cachedAt = time.Now()
	return &res, nil
}

func (r *sequenceResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// expiringStore stands in for an object store serving presigned GETs. A URL
// whose "sig" query value is in expired answers 403 the way S3 answers an
// expired signature; any other signature serves body with Range support.
type expiringStore struct {
	body    []byte
	mu      sync.Mutex
	expired map[string]bool
	hits    atomic.Int64
	// laterBlocks, when set, holds every request that does not start at byte
	// 0 until it is closed.
	laterBlocks chan struct{}
}

func (s *expiringStore) expire(sig string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired == nil {
		s.expired = map[string]bool{}
	}
	s.expired[sig] = true
}

func (s *expiringStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	if s.laterBlocks != nil && !strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
		<-s.laterBlocks
	}
	s.mu.Lock()
	dead := s.expired[r.URL.Query().Get("sig")]
	s.mu.Unlock()
	if dead {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>")
		return
	}
	size := int64(len(s.body))
	start, end := int64(0), size-1
	if rng := strings.TrimPrefix(r.Header.Get("Range"), "bytes="); rng != "" {
		parts := strings.SplitN(rng, "-", 2)
		start, _ = strconv.ParseInt(parts[0], 10, 64)
		if parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	_, _ = w.Write(s.body[start : end+1])
}

func presignedResult(storeURL, sig string, size int) *ResolveResult {
	return &ResolveResult{
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: storeURL + "/media.mkv?sig=" + sig,
		ExpectedSizeBytes: uint64(size),
		URLTTLSeconds:     3600,
	}
}

func newStaleTestRelay(t *testing.T, resolver Resolver, decision admission.CacheDecision) *httptest.Server {
	t.Helper()
	s := New(Options{
		BasePath:  t.TempDir(),
		Admitter:  &fakeAdmitter{decision: decision},
		Resolver:  resolver,
		BlockSize: 8,
	})
	return mount(t, s)
}

func relayRequest(t *testing.T, method, url, rng string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "MistServer/Unknown")
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// warmResolve makes the relay cache the first resolution, as a playback that
// started before the URL expired does.
func warmResolve(t *testing.T, relayURL string) {
	t.Helper()
	resp := relayRequest(t, http.MethodHead, relayURL, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warm-up HEAD status=%d", resp.StatusCode)
	}
}

// Mist opens a source with a HEAD. When the cached presigned URL has expired
// the relay must re-resolve and answer from the fresh URL, never pass the
// store's 403 to Mist ("Error getting URI info ... 403").
func TestRelayHEADReResolvesExpiredPresignedURL(t *testing.T) {
	body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV")
	store := &expiringStore{body: body}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(up.URL, "old", len(body)), presignedResult(up.URL, "new", len(body))}}
	ts := newStaleTestRelay(t, resolver, admission.CacheMemoryOnly)
	defer ts.Close()
	relayURL := ts.URL + "/internal/artifact/vod/hstale.mkv"

	warmResolve(t, relayURL)
	store.expire("old")

	resp := relayRequest(t, http.MethodHead, relayURL, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD after presign expiry: status=%d content-length=%q, want 200 with %d", resp.StatusCode, resp.Header.Get("Content-Length"), len(body))
	}
	if got := resolver.callCount(); got != 2 {
		t.Fatalf("resolver calls=%d, want 2 (initial + one re-resolve)", got)
	}
}

// A ranged read against an expired cached URL is answered from a fresh
// resolution, for both memory-only and disk-cached playback.
func TestRelayRangedGETReResolvesExpiredPresignedURL(t *testing.T) {
	for name, decision := range map[string]admission.CacheDecision{"memory-only": admission.CacheMemoryOnly, "disk": admission.CacheToDisk} {
		t.Run(name, func(t *testing.T) {
			body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV")
			store := &expiringStore{body: body}
			up := httptest.NewServer(store)
			defer up.Close()
			resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(up.URL, "old", len(body)), presignedResult(up.URL, "new", len(body))}}
			ts := newStaleTestRelay(t, resolver, decision)
			defer ts.Close()
			relayURL := ts.URL + "/internal/artifact/vod/hstale.mkv"

			warmResolve(t, relayURL)
			store.expire("old")

			resp := relayRequest(t, http.MethodGet, relayURL, "bytes=10-21")
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent || string(got) != string(body[10:22]) {
				t.Fatalf("ranged GET after presign expiry: status=%d body=%q, want 206 %q", resp.StatusCode, got, body[10:22])
			}
		})
	}
}

// A read that is already streaming when the URL expires keeps going: the next
// block fetch re-resolves instead of truncating the response.
func TestRelayLongReadSurvivesPresignExpiryMidStream(t *testing.T) {
	body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV")
	store := &expiringStore{body: body, laterBlocks: make(chan struct{})}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(up.URL, "old", len(body)), presignedResult(up.URL, "new", len(body))}}
	ts := newStaleTestRelay(t, resolver, admission.CacheMemoryOnly)
	defer ts.Close()
	relayURL := ts.URL + "/internal/artifact/vod/hstale.mkv"

	resp := relayRequest(t, http.MethodGet, relayURL, "bytes=0-31")
	defer resp.Body.Close()
	first := make([]byte, 8)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatalf("first block: %v", err)
	}
	store.expire("old")
	close(store.laterBlocks)
	rest, _ := io.ReadAll(resp.Body)
	if got := string(first) + string(rest); got != string(body) {
		t.Fatalf("long read across presign expiry returned %q, want %q", got, body)
	}
}

// The resolve cache never serves a presigned URL past the expiry signed into
// it, whatever URL TTL the resolution declared.
func TestResolveCacheHonorsSignedPresignExpiry(t *testing.T) {
	result := func(signedAgo time.Duration) *ResolveResult {
		signedAt := time.Now().Add(-signedAgo).UTC().Format("20060102T150405Z")
		return &ResolveResult{
			State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
			MediaPresignedURL: "https://s3.example/b/m.mkv?X-Amz-Date=" + signedAt + "&X-Amz-Expires=900&X-Amz-Signature=x",
			URLTTLSeconds:     3600,
			cachedAt:          time.Now(),
		}
	}
	c := newResolveCache()
	c.Put("vod", "fresh", ".mkv", result(time.Minute))
	if _, ok := c.Get("vod", "fresh", ".mkv"); !ok {
		t.Fatal("cache dropped a presigned URL with 14 minutes left")
	}
	c.Put("vod", "expiring", ".mkv", result(15*time.Minute-20*time.Second))
	if _, ok := c.Get("vod", "expiring", ".mkv"); ok {
		t.Fatalf("cache served a presigned URL that expires in 20 s (refresh margin %s)", presignRefreshMargin)
	}
}

// Mist reads the .dtsh index through the same resolution; an expired index URL
// is re-resolved within the request instead of answering retry-later.
func TestRelayDtshReResolvesExpiredPresignedURL(t *testing.T) {
	index := validDtshBytes()
	store := &expiringStore{body: index}
	up := httptest.NewServer(store)
	defer up.Close()
	result := func(sig string) *ResolveResult {
		r := presignedResult(up.URL, sig, 32)
		r.DtshPresignedGet = up.URL + "/media.mkv.dtsh?sig=" + sig
		return r
	}
	resolver := &sequenceResolver{out: []*ResolveResult{result("old"), result("new")}}
	ts := newStaleTestRelay(t, resolver, admission.CacheMemoryOnly)
	defer ts.Close()

	resp := relayRequest(t, http.MethodHead, ts.URL+"/internal/artifact/vod/hdtsh.mkv.dtsh", "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("warm-up dtsh HEAD status=%d", resp.StatusCode)
	}
	store.expire("old")

	resp = relayRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/hdtsh.mkv.dtsh", "")
	got, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(got) != len(index) {
		t.Fatalf("dtsh GET after presign expiry: status=%d len=%d, want 200 with %d bytes", resp.StatusCode, len(got), len(index))
	}
}
