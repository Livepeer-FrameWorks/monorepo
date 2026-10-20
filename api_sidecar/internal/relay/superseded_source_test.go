package relay

import (
	"context"
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

// objectStore serves named objects with Range support and answers 404 for an
// object that was never stored or was deleted, the way S3 answers a presigned
// GET for a missing key.
type objectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *objectStore) delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, name)
}

func (s *objectStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	body, ok := s.objects[strings.TrimPrefix(r.URL.Path, "/")]
	s.mu.Unlock()
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
		return
	}
	expiring := &expiringStore{body: body}
	expiring.ServeHTTP(w, r)
}

// extResolver answers each extension with its own result sequence, the last
// result repeatedly, the way Foghorn answers a relay resolve for the
// artifact's current container per requested extension.
type extResolver struct {
	mu    sync.Mutex
	byExt map[string][]*ResolveResult
	calls map[string]int
}

func (r *extResolver) Resolve(rc ResolveContext) (*ResolveResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]int{}
	}
	seq := r.byExt[rc.Ext]
	if len(seq) == 0 {
		return &ResolveResult{State: ipcpb.AssetState_ASSET_STATE_SOURCE_MISSING}, nil
	}
	idx := min(r.calls[rc.Ext], len(seq)-1)
	r.calls[rc.Ext]++
	res := *seq[idx]
	res.cachedAt = time.Now()
	return &res, nil
}

func (r *extResolver) callCount(ext string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[ext]
}

func storedObject(storeURL, key string, size int) *ResolveResult {
	return &ResolveResult{
		State:             ipcpb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: storeURL + "/" + key + "?sig=s",
		ExpectedSizeBytes: uint64(size),
		URLTTLSeconds:     3600,
	}
}

// A VOD played while processing resolves its uploaded .mp4; after processing
// the artifact is an .mkv. A request for the .mkv must resolve the .mkv and
// serve its bytes, never the .mp4 resolution cached for the same hash.
func TestRelayResolveCacheIsKeyedByExtension(t *testing.T) {
	upload := []byte("UPLOAD-MP4-BYTES-0123456789abcdef")
	processed := []byte("PROCESSED-MKV-BYTES-0123456789ABCDEFGH")
	store := &objectStore{objects: map[string][]byte{"uploads/h.mp4": upload, "vod/h.mkv": processed}}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &extResolver{byExt: map[string][]*ResolveResult{
		".mp4": {storedObject(up.URL, "uploads/h.mp4", len(upload))},
		".mkv": {storedObject(up.URL, "vod/h.mkv", len(processed))},
	}}
	for name, decision := range map[string]admission.CacheDecision{"memory-only": admission.CacheMemoryOnly, "disk": admission.CacheToDisk} {
		t.Run(name, func(t *testing.T) {
			ts := newStaleTestRelay(t, resolver, decision)
			defer ts.Close()

			resp := relayRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/h.mp4", "bytes=0-5")
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("mp4 read during processing: status=%d", resp.StatusCode)
			}

			resp = relayRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/h.mkv", "")
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(got) != string(processed) {
				t.Fatalf("mkv read after processing: status=%d body=%q, want 200 %q", resp.StatusCode, got, processed)
			}
		})
	}
}

// The cached resolution names an object storage no longer holds (the
// superseded upload, deleted by staging cleanup). The relay must re-resolve
// once and serve the artifact's current object, not answer 404 from the cached
// resolution until its TTL runs out.
func TestRelayUpstream404ReResolvesOnce(t *testing.T) {
	old := []byte("OLD-OBJECT-0123456789abcdefghijklmn")
	current := []byte("CURRENT-OBJECT-0123456789ABCDEFGHIJKLMNOPQRS")
	cases := []struct {
		name     string
		method   string
		rng      string
		decision admission.CacheDecision
		status   int
		body     string
	}{
		{name: "HEAD", method: http.MethodHead, decision: admission.CacheMemoryOnly, status: http.StatusOK},
		{name: "ranged memory-only", method: http.MethodGet, rng: "bytes=2-9", decision: admission.CacheMemoryOnly, status: http.StatusPartialContent, body: string(current[2:10])},
		{name: "ranged disk", method: http.MethodGet, rng: "bytes=2-9", decision: admission.CacheToDisk, status: http.StatusPartialContent, body: string(current[2:10])},
		{name: "full disk", method: http.MethodGet, decision: admission.CacheToDisk, status: http.StatusOK, body: string(current)},
		{name: "unknown size", method: http.MethodGet, rng: "bytes=2-9", decision: admission.CacheMemoryOnly, status: http.StatusPartialContent, body: string(current[2:10])},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &objectStore{objects: map[string][]byte{"a/old.mkv": old, "a/current.mkv": current}}
			up := httptest.NewServer(store)
			defer up.Close()
			first, second := storedObject(up.URL, "a/old.mkv", len(old)), storedObject(up.URL, "a/current.mkv", len(current))
			if tc.name == "unknown size" {
				first.ExpectedSizeBytes, second.ExpectedSizeBytes = 0, 0
			}
			resolver := &extResolver{byExt: map[string][]*ResolveResult{".mkv": {first, second}}}
			ts := newStaleTestRelay(t, resolver, tc.decision)
			defer ts.Close()
			relayURL := ts.URL + "/internal/artifact/vod/h.mkv"

			warmResolve(t, relayURL)
			store.delete("a/old.mkv")

			resp := relayRequest(t, tc.method, relayURL, tc.rng)
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.status || string(got) != tc.body {
				t.Fatalf("%s after the cached object was deleted: status=%d body=%q, want %d %q", tc.method, resp.StatusCode, got, tc.status, tc.body)
			}
			if tc.method == http.MethodHead && resp.Header.Get("Content-Length") != strconv.Itoa(len(current)) {
				t.Fatalf("HEAD Content-Length=%q, want %d", resp.Header.Get("Content-Length"), len(current))
			}
			if n := resolver.callCount(".mkv"); n != 2 {
				t.Fatalf("resolver calls=%d, want 2 (initial + one re-resolve)", n)
			}

			// The renewed resolution is cached: the next read needs no resolve.
			resp = relayRequest(t, http.MethodGet, relayURL, "bytes=0-3")
			got, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusPartialContent || string(got) != string(current[:4]) {
				t.Fatalf("read after renewal: status=%d body=%q", resp.StatusCode, got)
			}
			if n := resolver.callCount(".mkv"); n != 2 {
				t.Fatalf("resolver calls after renewal=%d, want 2", n)
			}
		})
	}
}

// When the fresh resolution's object is missing too, the request answers 404
// after exactly one re-resolve.
func TestRelayUpstream404AfterReResolveAnswers404(t *testing.T) {
	store := &objectStore{objects: map[string][]byte{}}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &extResolver{byExt: map[string][]*ResolveResult{".mkv": {storedObject(up.URL, "a/gone.mkv", 32)}}}
	for name, decision := range map[string]admission.CacheDecision{"memory-only": admission.CacheMemoryOnly, "disk": admission.CacheToDisk} {
		t.Run(name, func(t *testing.T) {
			resolver.mu.Lock()
			resolver.calls = nil
			resolver.mu.Unlock()
			ts := newStaleTestRelay(t, resolver, decision)
			defer ts.Close()

			resp := relayRequest(t, http.MethodGet, ts.URL+"/internal/artifact/vod/h.mkv", "bytes=0-7")
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", resp.StatusCode)
			}
			if n := resolver.callCount(".mkv"); n != 2 {
				t.Fatalf("resolver calls=%d, want 2 (initial + one re-resolve)", n)
			}
		})
	}
}

// Readers that hit the same deleted object at once are all served from the
// artifact's current object, each with at most one re-resolve.
func TestRelayConcurrentReadersOfDeletedObjectAllRecover(t *testing.T) {
	old := []byte("OLD-OBJECT-0123456789abcdefghijklmn")
	current := []byte("CURRENT-OBJECT-0123456789ABCDEFGHIJKLMNOPQRS")
	store := &objectStore{objects: map[string][]byte{"a/old.mkv": old, "a/current.mkv": current}}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &extResolver{byExt: map[string][]*ResolveResult{".mkv": {storedObject(up.URL, "a/old.mkv", len(old)), storedObject(up.URL, "a/current.mkv", len(current))}}}
	ts := newStaleTestRelay(t, resolver, admission.CacheMemoryOnly)
	defer ts.Close()
	relayURL := ts.URL + "/internal/artifact/vod/h.mkv"
	warmResolve(t, relayURL)
	store.delete("a/old.mkv")

	var wg sync.WaitGroup
	statuses := make(chan int, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := relayRequest(t, http.MethodGet, relayURL, "bytes=0-7")
			_, _ = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			statuses <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	for st := range statuses {
		if st != http.StatusPartialContent {
			t.Fatalf("concurrent read status=%d, want 206", st)
		}
	}
	if n := resolver.callCount(".mkv"); n > 1+16 {
		t.Fatalf("resolver calls=%d, want at most one re-resolve per request", n)
	}
}

// gatedResolver blocks every resolve until release is closed.
type gatedResolver struct {
	release chan struct{}
	result  *ResolveResult
	calls   atomic.Int64
}

func (r *gatedResolver) Resolve(ResolveContext) (*ResolveResult, error) {
	r.calls.Add(1)
	<-r.release
	res := *r.result
	res.cachedAt = time.Now()
	return &res, nil
}

// Requests whose upstream lost the same cached resolution while a re-resolve
// runs wait for it instead of resolving on their own, the stale resolution
// stays cached until its replacement arrives, and a request that fails over
// after the re-resolve finished takes its answer from the cache.
func TestResolveRenewalIsSharedAcrossRequests(t *testing.T) {
	resolver := &gatedResolver{release: make(chan struct{}), result: storedObject("http://store", "a/current.mkv", 44)}
	s := New(Options{BasePath: t.TempDir(), Admitter: &fakeAdmitter{decision: admission.CacheMemoryOnly}, Resolver: resolver, BlockSize: 8})
	stale := storedObject("http://store", "a/old.mkv", 35)
	stale.cachedAt = time.Now()
	s.cache.Put("vod", "h", ".mkv", stale)

	const waiters = 16
	results := make(chan *ResolveResult, waiters+1)
	var wg sync.WaitGroup
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fresh, ok := s.resolveRenewer(context.Background(), "vod", "h", ".mkv")(stale)
			if !ok {
				fresh = nil
			}
			results <- fresh
		}()
	}
	for resolver.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if got, ok := s.cache.Get("vod", "h", ".mkv"); !ok || got != stale {
		t.Fatal("the stale resolution left the cache before its replacement arrived")
	}
	time.Sleep(20 * time.Millisecond)
	close(resolver.release)
	wg.Wait()
	late, ok := s.resolveRenewer(context.Background(), "vod", "h", ".mkv")(stale)
	if !ok {
		t.Fatal("late renewal got no resolution")
	}
	results <- late
	close(results)
	var first *ResolveResult
	for r := range results {
		if r == nil || r.MediaPresignedURL != resolver.result.MediaPresignedURL {
			t.Fatalf("renewal answered %+v, want the fresh resolution", r)
		}
		if first == nil {
			first = r
		} else if r != first {
			t.Fatal("renewals answered different resolutions")
		}
	}
	if n := resolver.calls.Load(); n != 1 {
		t.Fatalf("resolver calls=%d, want 1 shared re-resolve", n)
	}
}

// A re-resolve after the cached object was deleted can name another object of
// the same size. The blocks cached from the deleted object are not that
// object's bytes, so the relay drops them and serves every block from the
// renewed object.
func TestRelayBlockCacheDropsBlocksOfReplacedObjectOfEqualSize(t *testing.T) {
	old := []byte("OLD-OBJECT-0123456789abcdefghijk")
	current := []byte("CUR-OBJECT-ABCDEFGHIJKLMNOPQRSTU")
	if len(old) != len(current) {
		t.Fatalf("test objects must have equal size: %d vs %d", len(old), len(current))
	}
	store := &objectStore{objects: map[string][]byte{"a/old.mkv": old, "a/current.mkv": current}}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &extResolver{byExt: map[string][]*ResolveResult{".mkv": {storedObject(up.URL, "a/old.mkv", len(old)), storedObject(up.URL, "a/current.mkv", len(current))}}}
	ts := newStaleTestRelay(t, resolver, admission.CacheToDisk)
	defer ts.Close()
	relayURL := ts.URL + "/internal/artifact/vod/h.mkv"

	read := func(rng string) string {
		t.Helper()
		resp := relayRequest(t, http.MethodGet, relayURL, rng)
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			t.Fatalf("read %q: status=%d", rng, resp.StatusCode)
		}
		return string(got)
	}
	if got := read("bytes=0-7"); got != string(old[:8]) {
		t.Fatalf("first read=%q, want %q", got, old[:8])
	}
	store.delete("a/old.mkv")
	if got := read("bytes=8-15"); got != string(current[8:16]) {
		t.Fatalf("read after the object was replaced=%q, want %q", got, current[8:16])
	}
	if got := read("bytes=0-7"); got != string(current[:8]) {
		t.Fatalf("block cached from the replaced object served again: got %q, want %q", got, current[:8])
	}
	if got := read(""); got != string(current) {
		t.Fatalf("full read=%q, want %q", got, current)
	}
}

// A presigned URL renewed for the same object keeps the blocks cached through
// the expired URL: they are served from disk without another upstream fetch.
func TestRelayBlockCacheKeepsBlocksAcrossPresignRenewal(t *testing.T) {
	body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV")
	store := &expiringStore{body: body}
	up := httptest.NewServer(store)
	defer up.Close()
	resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(up.URL, "old", len(body)), presignedResult(up.URL, "new", len(body))}}
	ts := newStaleTestRelay(t, resolver, admission.CacheToDisk)
	defer ts.Close()
	relayURL := ts.URL + "/internal/artifact/vod/hrenew.mkv"

	read := func(rng, want string) {
		t.Helper()
		resp := relayRequest(t, http.MethodGet, relayURL, rng)
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPartialContent || string(got) != want {
			t.Fatalf("read %q: status=%d body=%q, want 206 %q", rng, resp.StatusCode, got, want)
		}
	}
	read("bytes=0-7", string(body[:8]))
	store.expire("old")
	read("bytes=8-15", string(body[8:16]))
	if n := resolver.callCount(); n != 2 {
		t.Fatalf("resolver calls=%d, want 2 (initial + renewal)", n)
	}
	hits := store.hits.Load()
	read("bytes=0-7", string(body[:8]))
	if n := store.hits.Load(); n != hits {
		t.Fatalf("block cached before the renewal was fetched again: upstream hits %d -> %d", hits, n)
	}
}

// A renewal in the middle of a response that names another object of the
// same size cannot continue that response: its later blocks would be another
// object's bytes after this object's. The response ends short instead, and
// the next read serves the renewed object whole.
func TestRelayMidResponseRenewalToAnotherObjectDoesNotSplice(t *testing.T) {
	old := []byte("OLD-OBJECT-0123456789abcdefghijk")
	current := []byte("CUR-OBJECT-ABCDEFGHIJKLMNOPQRSTU")
	cases := []struct {
		name     string
		decision admission.CacheDecision
		// block1Hit is the upstream request count once block 1 is requested:
		// a disk read preflights block 0 before it fills it.
		block1Hit int64
	}{
		{name: "memory-only", decision: admission.CacheMemoryOnly, block1Hit: 2},
		{name: "disk", decision: admission.CacheToDisk, block1Hit: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldStore := &expiringStore{body: old, laterBlocks: make(chan struct{})}
			oldUp := httptest.NewServer(oldStore)
			defer oldUp.Close()
			curUp := httptest.NewServer(&expiringStore{body: current})
			defer curUp.Close()
			resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(oldUp.URL, "s1", len(old)), presignedResult(curUp.URL, "s2", len(current))}}
			ts := newStaleTestRelay(t, resolver, tc.decision)
			defer ts.Close()
			relayURL := ts.URL + "/internal/artifact/vod/hmid.mkv"

			type result struct {
				body []byte
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp := relayRequest(t, http.MethodGet, relayURL, "")
				got, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				done <- result{body: got, err: err}
			}()
			// Block 0 is served; the fetch of block 1 waits until the old
			// URL is refused.
			deadline := time.Now().Add(3 * time.Second)
			for oldStore.hits.Load() < tc.block1Hit {
				if time.Now().After(deadline) {
					t.Fatal("block 1 was never requested")
				}
				time.Sleep(5 * time.Millisecond)
			}
			oldStore.expire("s1")
			close(oldStore.laterBlocks)
			r := <-done
			if !strings.HasPrefix(string(old), string(r.body)) {
				t.Fatalf("response mixed two objects: %q (old %q, current %q)", r.body, old, current)
			}

			resp := relayRequest(t, http.MethodGet, relayURL, "")
			got, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(got) != string(current) {
				t.Fatalf("read after the renewal: status=%d body=%q, want 200 %q", resp.StatusCode, got, current)
			}
		})
	}
}
