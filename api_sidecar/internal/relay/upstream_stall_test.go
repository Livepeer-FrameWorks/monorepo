package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/admission"
)

// testUpstreamBounds keeps the relay's upstream bounds short enough for a
// test to observe them; withUpstreamBounds installs them for relays built
// after the call.
var testUpstreamBounds = upstreamBounds{
	dial:           time.Second,
	tlsHandshake:   time.Second,
	responseHeader: 300 * time.Millisecond,
	readStall:      300 * time.Millisecond,
}

func withUpstreamBounds(t *testing.T, b upstreamBounds) {
	t.Helper()
	prev := relayUpstreamBounds
	relayUpstreamBounds = b
	t.Cleanup(func() { relayUpstreamBounds = prev })
}

// patternBody returns size bytes whose value depends on their offset, so a
// response spliced at the wrong position does not compare equal.
func patternBody(size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte((i * 7) % 251)
	}
	return b
}

func parseTestRange(r *http.Request, size int64) (int64, int64) {
	start, end := int64(0), size-1
	if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
		parts := strings.SplitN(strings.TrimPrefix(rng, "bytes="), "-", 2)
		start, _ = strconv.ParseInt(parts[0], 10, 64)
		if len(parts) == 2 && parts[1] != "" {
			end, _ = strconv.ParseInt(parts[1], 10, 64)
		}
	}
	return start, end
}

// stallUpstream serves body with Range support. stall(n, r) decides, for the
// n-th request (1-based), how many body bytes go out before the response
// stops making progress; a negative value serves the whole range, zero sends
// no headers at all. A stalled response holds until its request ends or the
// test finishes.
type stallUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	ranges []string
}

func (u *stallUpstream) requests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.ranges...)
}

func newStallUpstream(t *testing.T, body []byte, stall func(n int, r *http.Request) int) *stallUpstream {
	t.Helper()
	release := make(chan struct{})
	u := &stallUpstream{}
	var count atomic.Int32
	size := int64(len(body))
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(count.Add(1))
		u.mu.Lock()
		u.ranges = append(u.ranges, r.Header.Get("Range"))
		u.mu.Unlock()
		hold := func() {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		sendBefore := stall(n, r)
		if sendBefore == 0 {
			hold()
			return
		}
		start, end := parseTestRange(r, size)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		part := body[start : end+1]
		if sendBefore > 0 && sendBefore < len(part) {
			_, _ = w.Write(part[:sendBefore])
			w.(http.Flusher).Flush()
			hold()
			return
		}
		_, _ = w.Write(part)
	}))
	t.Cleanup(func() {
		close(release)
		u.srv.Close()
	})
	return u
}

// waitFillsOnCleanup waits, when the test ends, for block fills of s still
// running: a fill outlives the request that started it and may still be
// writing under the test's temp dir.
func waitFillsOnCleanup(t *testing.T, s *Server) {
	t.Helper()
	t.Cleanup(s.fills.wg.Wait)
}

// eventuallyHasBlock waits briefly for a block's rename, which follows the
// last byte the client receives.
func eventuallyHasBlock(store *BlockStore, idx int64) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !store.HasBlock(idx) {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

func relayFor(t *testing.T, up *stallUpstream, hash string, size int64, decision admission.CacheDecision, blockSize int64) *Server {
	t.Helper()
	return relayForUpstream(t, up.srv, hash, size, decision, blockSize)
}

func getWithin(t *testing.T, url, rng string, budget time.Duration) ([]byte, int, time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	began := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, time.Since(began), err
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	return got, resp.StatusCode, time.Since(began), err
}

// An upstream that sends a block's headers and part of its body and then goes
// silent is abandoned after the read-stall bound, and the rest of the block
// is fetched from the byte where the stalled response stopped. The client
// receives the whole block in one response and the block lands in the cache.
func TestColdBlockStalledMidBodyResumesFromStallOffset(t *testing.T) {
	withUpstreamBounds(t, testUpstreamBounds)
	const size = 64 * 1024
	const sent = 16 * 1024
	body := patternBody(size)
	up := newStallUpstream(t, body, func(n int, _ *http.Request) int {
		if n == 1 {
			return sent
		}
		return -1
	})
	s := relayFor(t, up, "stallmid", size, admission.CacheToDisk, size)
	ts := mount(t, s)
	defer ts.Close()

	got, status, elapsed, err := getWithin(t, ts.URL+"/internal/artifact/vod/stallmid.mkv", fmt.Sprintf("bytes=0-%d", size-1), 5*time.Second)
	if err != nil {
		t.Fatalf("relay read failed after %s with %d of %d bytes: %v", elapsed, len(got), size, err)
	}
	if status != http.StatusPartialContent || !bytes.Equal(got, body) {
		t.Fatalf("status=%d body=%d bytes equal=%v, want 206 and the full block", status, len(got), bytes.Equal(got, body))
	}
	if limit := testUpstreamBounds.readStall + 2*time.Second; elapsed > limit {
		t.Fatalf("stalled block took %s, want under %s", elapsed, limit)
	}
	reqs := up.requests()
	if want := fmt.Sprintf("bytes=%d-%d", sent, size-1); reqs[len(reqs)-1] != want {
		t.Fatalf("upstream requests = %v, want the last to resume at %q", reqs, want)
	}
	store := NewBlockStore(filepath.Join(s.basePath, "vod", "stallmid.mkv"), size)
	if !eventuallyHasBlock(store, 0) {
		t.Fatal("resumed block was not cached")
	}
}

// An upstream that keeps stalling ends the relay response within the bound
// instead of holding it open, and releases the block so the next request
// fetches and serves it.
func TestColdBlockPersistentStallFailsWithinBoundAndRetryServes(t *testing.T) {
	withUpstreamBounds(t, testUpstreamBounds)
	const size = 64 * 1024
	body := patternBody(size)
	var stalling atomic.Bool
	stalling.Store(true)
	up := newStallUpstream(t, body, func(_ int, _ *http.Request) int {
		if stalling.Load() {
			return 8 * 1024
		}
		return -1
	})
	s := relayFor(t, up, "stallall", size, admission.CacheToDisk, size)
	ts := mount(t, s)
	defer ts.Close()

	url := ts.URL + "/internal/artifact/vod/stallall.mkv"
	got, _, elapsed, _ := getWithin(t, url, "", 10*time.Second)
	limit := time.Duration(1+maxBlockFetchResumes)*testUpstreamBounds.readStall + 2*time.Second
	if elapsed > limit {
		t.Fatalf("stalled fetch held the response for %s, want under %s", elapsed, limit)
	}
	if len(got) >= size {
		t.Fatalf("stalled fetch delivered %d bytes, want a truncated response", len(got))
	}

	stalling.Store(false)
	got, status, _, err := getWithin(t, url, "", 5*time.Second)
	if err != nil || status != http.StatusOK || !bytes.Equal(got, body) {
		t.Fatalf("retry: status=%d bytes=%d err=%v, want 200 and the full block", status, len(got), err)
	}
	// The failed fill kept the bytes it wrote; the retry fetches only the rest.
	reqs := up.requests()
	if last := reqs[len(reqs)-1]; strings.HasPrefix(last, "bytes=0-") {
		t.Fatalf("upstream requests = %v, want the retry to continue after the bytes the failed fill wrote", reqs)
	}
}

// A block fill that cannot create its cache file does not fail a playback
// read: the reader streams the block from the upstream without caching it.
func TestColdBlockFillWithoutCacheFileStreamsUpstream(t *testing.T) {
	const size = 64 * 1024
	body := patternBody(size)
	up := upstreamServer(t, body)
	defer up.Close()
	s := relayForUpstream(t, up, "nocachefile", size, admission.CacheToDisk, size)
	ts := mount(t, s)
	defer ts.Close()
	store := NewBlockStore(filepath.Join(s.basePath, "vod", "nocachefile.mkv"), size)
	if err := os.MkdirAll(store.BlockPath(0)+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	s.cleanedBlockDirs.Store(store.Dir(), struct{}{})

	got, status, _, err := getWithin(t, ts.URL+"/internal/artifact/vod/nocachefile.mkv", fmt.Sprintf("bytes=0-%d", size-1), 5*time.Second)
	if err != nil || status != http.StatusPartialContent || !bytes.Equal(got, body) {
		t.Fatalf("status=%d bytes=%d err=%v, want 206 and the full block", status, len(got), err)
	}
	if store.HasBlock(0) {
		t.Fatal("block cached although its cache file could not be created")
	}
}

// A reader that stops reading the block it started does not hold the block
// back from other readers: the block's fill does not wait on any reader.
func TestColdBlockReaderNotHeldBySlowReader(t *testing.T) {
	withUpstreamBounds(t, testUpstreamBounds)
	const size = 8 << 20
	body := patternBody(size)
	up := newStallUpstream(t, body, func(int, *http.Request) int { return -1 })
	s := relayFor(t, up, "slowreader", size, admission.CacheToDisk, size)
	ts := mount(t, s)
	defer ts.Close()
	url := ts.URL + "/internal/artifact/vod/slowreader.mkv"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rng := fmt.Sprintf("bytes=0-%d", size-1)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("Range", rng)
	slow, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Body.Close()

	got, status, elapsed, err := getWithin(t, url, rng, 5*time.Second)
	if err != nil || status != http.StatusPartialContent || !bytes.Equal(got, body) {
		t.Fatalf("second reader: status=%d bytes=%d err=%v after %s, want 206 and the full block", status, len(got), err, elapsed)
	}
	if n := len(up.requests()); n != 1 {
		t.Fatalf("upstream requests = %v, want the one fill both readers share", up.requests())
	}
}

// An upstream that accepts the connection and never answers fails the
// request within the response-header bound.
func TestColdBlockUpstreamWithoutHeadersFailsWithinBound(t *testing.T) {
	withUpstreamBounds(t, testUpstreamBounds)
	const size = 64 * 1024
	up := newStallUpstream(t, patternBody(size), func(int, *http.Request) int { return 0 })
	s := relayFor(t, up, "noheaders", size, admission.CacheMemoryOnly, size)
	ts := mount(t, s)
	defer ts.Close()

	_, status, elapsed, err := getWithin(t, ts.URL+"/internal/artifact/vod/noheaders.mkv", "", 5*time.Second)
	if err != nil {
		t.Fatalf("relay request did not complete after %s: %v", elapsed, err)
	}
	if status < 500 {
		t.Fatalf("status=%d, want a 5xx", status)
	}
	if limit := testUpstreamBounds.responseHeader + 2*time.Second; elapsed > limit {
		t.Fatalf("headerless upstream held the request for %s, want under %s", elapsed, limit)
	}
}

// A request for another block of the same asset does not delete the cache
// file an in-flight fetch of this process is still writing.
func TestColdBlockConcurrentRequestKeepsInflightTmp(t *testing.T) {
	const blockSize = 32 * 1024
	const size = 2 * blockSize
	body := patternBody(size)
	block1Done := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := parseTestRange(r, size)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		part := body[start : end+1]
		if start != 0 {
			_, _ = w.Write(part)
			return
		}
		_, _ = w.Write(part[:len(part)/2])
		w.(http.Flusher).Flush()
		select {
		case <-block1Done:
		case <-time.After(5 * time.Second):
		}
		_, _ = w.Write(part[len(part)/2:])
	}))
	defer up.Close()
	s := relayForUpstream(t, up, "twoblocks", size, admission.CacheToDisk, blockSize)
	ts := mount(t, s)
	defer ts.Close()
	url := ts.URL + "/internal/artifact/vod/twoblocks.mkv"

	errs := make(chan error, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", blockSize-1))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		got, err := io.ReadAll(resp.Body)
		if err == nil && !bytes.Equal(got, body[:blockSize]) {
			err = fmt.Errorf("block 0 body mismatch (%d bytes)", len(got))
		}
		errs <- err
	}()

	store := NewBlockStore(filepath.Join(s.basePath, "vod", "twoblocks.mkv"), blockSize)
	deadline := time.Now().Add(5 * time.Second)
	for {
		matches, _ := filepath.Glob(filepath.Join(store.Dir(), "*.tmp"))
		if len(matches) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("block 0 fetch never started its cache file")
		}
		time.Sleep(5 * time.Millisecond)
	}

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", blockSize, size-1))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	close(block1Done)

	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if !eventuallyHasBlock(store, 0) {
		t.Fatal("block 0 was not cached: a concurrent request removed its in-flight cache file")
	}
}

// The relay's default upstream client carries the dial, TLS handshake,
// response-header and body read-stall bounds.
func TestRelayDefaultUpstreamClientIsBounded(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), Admitter: &fakeAdmitter{}, Resolver: &fakeResolver{}})
	st, ok := s.httpc.Transport.(*stallBoundTransport)
	if !ok {
		t.Fatalf("upstream transport = %T, want *stallBoundTransport", s.httpc.Transport)
	}
	if st.readStall <= 0 || st.readStall >= mistSourceReadTimeout {
		t.Fatalf("read stall = %s, want under Mist's %s source read budget", st.readStall, mistSourceReadTimeout)
	}
	base, ok := st.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T", st.base)
	}
	if base.TLSHandshakeTimeout <= 0 || base.ResponseHeaderTimeout <= 0 || base.DialContext == nil {
		t.Fatalf("transport bounds tls=%s header=%s dial=%v, want all set", base.TLSHandshakeTimeout, base.ResponseHeaderTimeout, base.DialContext != nil)
	}
}

// A presigned URL that expires while its block is stalled mid-body is renewed
// for the resume: the rest of the block is fetched from the copied offset on
// the fresh resolution and the client receives every byte exactly once.
func TestColdBlockResumeAfterPresignExpiryRenewsAndContinues(t *testing.T) {
	withUpstreamBounds(t, testUpstreamBounds)
	const size = 64 * 1024
	const sent = 16 * 1024
	body := patternBody(size)
	var (
		mu     sync.Mutex
		oldUse int
		seen   []string
	)
	release := make(chan struct{})
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.URL.Query().Get("sig")
		mu.Lock()
		seen = append(seen, sig+" "+r.Header.Get("Range"))
		first := false
		if sig == "old" {
			oldUse++
			first = oldUse == 1
		}
		mu.Unlock()
		if sig == "old" && !first {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>")
			return
		}
		start, end := parseTestRange(r, size)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		part := body[start : end+1]
		if first {
			_, _ = w.Write(part[:sent])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = w.Write(part)
	}))
	t.Cleanup(func() {
		close(release)
		store.Close()
	})
	resolver := &sequenceResolver{out: []*ResolveResult{presignedResult(store.URL, "old", size), presignedResult(store.URL, "new", size)}}
	s := New(Options{
		BasePath:  t.TempDir(),
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: size,
	})
	ts := mount(t, s)
	defer ts.Close()

	got, status, elapsed, err := getWithin(t, ts.URL+"/internal/artifact/vod/expiring.mkv", fmt.Sprintf("bytes=0-%d", size-1), 5*time.Second)
	mu.Lock()
	reqs := append([]string(nil), seen...)
	mu.Unlock()
	if err != nil || status != http.StatusPartialContent || !bytes.Equal(got, body) {
		t.Fatalf("status=%d bytes=%d equal=%v err=%v after %s (upstream requests %v), want 206 and the whole block once", status, len(got), bytes.Equal(got, body), err, elapsed, reqs)
	}
	if want := fmt.Sprintf("new bytes=%d-%d", sent, size-1); reqs[len(reqs)-1] != want {
		t.Fatalf("upstream requests = %v, want the last to resume on the renewed URL at %q", reqs, want)
	}
	if resolver.callCount() != 2 {
		t.Fatalf("resolver calls = %d, want 2 (initial + one renewal)", resolver.callCount())
	}
}

// A cold block whose upstream stalls mid-body, and whose resume answers later
// than the reader's own deadline, keeps filling after that reader gives up:
// the next reader receives the whole block from the fill already in flight,
// and the block is never requested from its first byte again. The reader that
// gives up stands for Mist's 5 s source read timeout.
func TestColdBlockFillOutlivesReaderThatGaveUp(t *testing.T) {
	bounds := testUpstreamBounds
	bounds.responseHeader = 2 * time.Second
	withUpstreamBounds(t, bounds)
	const size = 64 * 1024
	const sent = 16 * 1024
	const resumeDelay = 700 * time.Millisecond
	body := patternBody(size)
	release := make(chan struct{})
	var (
		mu     sync.Mutex
		ranges []string
		starts = map[int64]int{}
	)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end := parseTestRange(r, size)
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		starts[start]++
		nth := starts[start]
		mu.Unlock()
		if start == sent && nth == 1 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(resumeDelay):
			}
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		part := body[start : end+1]
		if start == 0 && nth == 1 {
			_, _ = w.Write(part[:sent])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = w.Write(part)
	}))
	t.Cleanup(func() {
		close(release)
		up.Close()
	})
	s := relayForUpstream(t, up, "giveup", size, admission.CacheToDisk, size)
	ts := mount(t, s)
	defer ts.Close()
	url := ts.URL + "/internal/artifact/vod/giveup.mkv"
	rng := fmt.Sprintf("bytes=0-%d", size-1)

	first, _, _, err := getWithin(t, url, rng, testUpstreamBounds.readStall+300*time.Millisecond)
	if err == nil && len(first) == size {
		t.Fatal("first reader got the whole block before its deadline; the resume delay does not exceed it")
	}

	got, status, elapsed, err := getWithin(t, url, rng, 3*time.Second)
	mu.Lock()
	seen := append([]string(nil), ranges...)
	fromStart := starts[0]
	mu.Unlock()
	if err != nil || status != http.StatusPartialContent || !bytes.Equal(got, body) {
		t.Fatalf("second reader: status=%d bytes=%d equal=%v err=%v after %s (upstream requests %v), want 206 and the whole block", status, len(got), bytes.Equal(got, body), err, elapsed, seen)
	}
	if fromStart != 1 {
		t.Fatalf("upstream requests = %v: the block was requested from its first byte %d times, want once", seen, fromStart)
	}
	store := NewBlockStore(filepath.Join(s.basePath, "vod", "giveup.mkv"), size)
	if !eventuallyHasBlock(store, 0) {
		t.Fatal("block was not cached")
	}
}
