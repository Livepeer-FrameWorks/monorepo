package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"frameworks/api_sidecar/internal/admission"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// fakeAdmitter returns whatever decision the test wants.
type fakeAdmitter struct{ decision admission.CacheDecision }

func (f *fakeAdmitter) Decide(_ context.Context, _ string, _ admission.StorageIntent, _ uint64) (admission.CacheDecision, error) {
	return f.decision, nil
}

// fakeResolver returns canned results indexed by (kind, hash).
type fakeResolver struct {
	out   map[string]*ResolveResult
	err   error
	calls int
}

func (f *fakeResolver) Resolve(rc ResolveContext) (*ResolveResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	key := rc.AssetKind + "/" + rc.AssetHash
	if r, ok := f.out[key]; ok {
		return r, nil
	}
	return &ResolveResult{State: pb.AssetState_ASSET_STATE_SOURCE_MISSING}, nil
}

type fakeFreeze struct {
	calls []string
}

func (f *fakeFreeze) OnLocalDtshGenerated(kind, hash, localPath string) {
	f.calls = append(f.calls, kind+"/"+hash+"/"+localPath)
}

type unexpectedEOFReader struct {
	sent bool
}

func (r *unexpectedEOFReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, "partial dtsh"), nil
}

// upstreamServer stands in for S3. Reports total size + range support on
// HEAD, returns the supplied body on full GET, and slices the body for
// well-formed Range requests so tests can assert the relay's cold-path
// behavior (HEAD/Range probes must not trigger a full-asset download
// through the write-through cache).
func upstreamServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := int64(len(body))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if rng := r.Header.Get("Range"); rng != "" && strings.HasPrefix(rng, "bytes=") {
			spec := strings.TrimPrefix(rng, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := size - 1
			if len(parts) == 2 && parts[1] != "" {
				if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					end = e
				}
			}
			if start < 0 || start >= size || end < start || end >= size {
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
			return
		}
		_, _ = w.Write(body)
	}))
}

func newTestServer(t *testing.T, basePath string, decision admission.CacheDecision, resolver Resolver, freeze FreezeHandoff) *Server {
	t.Helper()
	return New(Options{
		BasePath: basePath,
		Admitter: &fakeAdmitter{decision: decision},
		Resolver: resolver,
		Freeze:   freeze,
	})
}

func mount(t *testing.T, s *Server) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	s.MountRoutes(r)
	return httptest.NewServer(r)
}

// doGet wraps http.DefaultClient.Do with a context-bound GET so tests
// satisfy the project's noctx lint check.
func doGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

func doMistGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "MistServer/Unknown")
	return http.DefaultClient.Do(req)
}

func TestServeWarmFile(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv"
	full := filepath.Join(dir, "vod", file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("warm bytes")
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if !resp.Close {
		t.Fatal("expected warm relay response to close the connection for Mist URIReader")
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
}

func TestServeWarmClipRequiresNestedSafePath(t *testing.T) {
	dir := t.TempDir()
	hash := "cliphash"
	file := hash + ".mkv"
	full := filepath.Join(dir, "clips", "streamA", file)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("nested clip bytes")
	if err := os.WriteFile(full, body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/clip/streamA/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nested clip status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}

	badResp, err := doGet(t, ts.URL+"/internal/artifact/clip/streamA/extra/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != http.StatusNotFound {
		t.Fatalf("extra path segment status=%d want 404", badResp.StatusCode)
	}

	traversalResp, err := doGet(t, ts.URL+"/internal/artifact/clip/../"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer traversalResp.Body.Close()
	if traversalResp.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status=%d want 404", traversalResp.StatusCode)
	}
}

func TestServeColdFileWritesBlocksToDisk(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv"
	body := []byte("cold bytes from s3 — span several blocks for coverage")
	up := upstreamServer(t, body)
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/object",
		ExpectedSizeBytes: uint64(len(body)),
		ContentType:       "video/x-matroska",
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	// 16-byte block size — body spans multiple blocks so we exercise
	// the per-block fetch + concatenation path.
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 16,
	})
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	// Block cache wrote every block to disk; meta.json present.
	blocksDir := filepath.Join(dir, "vod", file+".blocks")
	if _, metaErr := os.Stat(filepath.Join(blocksDir, "meta.json")); metaErr != nil {
		t.Fatalf("expected meta.json in block dir: %v", metaErr)
	}
	// Reassemble from blocks; full content should match.
	entries, err := os.ReadDir(blocksDir)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".blk" {
			blocks = append(blocks, e.Name())
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("no .blk files in %s; entries=%v", blocksDir, entries)
	}
}

func TestServeColdFilePropagatesUpstreamNotFoundBeforeMediaHeaders(t *testing.T) {
	dir := t.TempDir()
	hash := "missing"
	file := hash + ".mkv"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such key"))
	}))
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/object",
		ExpectedSizeBytes: 1024,
		ContentType:       "video/x-matroska",
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 512,
	})
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%q want 404 source missing", resp.StatusCode, body)
	}
	if cl := resp.Header.Get("Content-Length"); cl == "1024" {
		t.Fatalf("relay must not advertise media length for missing upstream source")
	}
	if _, err := os.Stat(filepath.Join(dir, "vod", file+".blocks")); !os.IsNotExist(err) {
		t.Fatalf("missing upstream must not create block cache, stat err=%v", err)
	}
}

func TestServeColdMemoryOnlyDoesNotWriteDisk(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv"
	body := []byte("memory-only stream")
	up := upstreamServer(t, body)
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/object",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := newTestServer(t, dir, admission.CacheMemoryOnly, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	// Block cache must not write blocks under memory-only admission.
	blocksDir := filepath.Join(dir, "vod", file+".blocks")
	if entries, _ := os.ReadDir(blocksDir); len(entries) > 0 {
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".blk" {
				t.Fatalf("memory-only must not write blocks; found %s", e.Name())
			}
		}
	}
}

func TestDtshPutLandsLocallyAndHandsOffFreeze(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv.dtsh"
	body := []byte("generated dtsh bytes")

	fz := &fakeFreeze{}
	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, fz)
	ts := mount(t, s)
	defer ts.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/internal/artifact/vod/"+file, bytes.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	target := filepath.Join(dir, "vod", file)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected dtsh on disk: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	if len(fz.calls) != 1 || !strings.Contains(fz.calls[0], target) {
		t.Fatalf("freeze handoff not called: %v", fz.calls)
	}
}

func TestUploadDtshPutWithTrailingSlashLandsLocally(t *testing.T) {
	dir := t.TempDir()
	hash := "uploadabc"
	file := hash + ".mov.dtsh"
	body := []byte("generated upload dtsh bytes")

	fz := &fakeFreeze{}
	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, fz)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/internal/artifact/upload/"+file+"/", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	target := filepath.Join(dir, "upload", file)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected upload dtsh on disk: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	if len(fz.calls) != 1 || !strings.Contains(fz.calls[0], target) {
		t.Fatalf("freeze handoff not called: %v", fz.calls)
	}
}

func TestUploadDtshGetServesWarmSidecarAfterMistGeneratesIt(t *testing.T) {
	dir := t.TempDir()
	hash := "uploadwarm"
	file := hash + ".mov.dtsh"
	body := []byte("warm upload sidecar")
	target := filepath.Join(dir, "upload", file)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/upload/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
}

func TestUploadDtshGetUsesGenericSidecarResolve(t *testing.T) {
	dir := t.TempDir()
	hash := "uploadcold"
	file := hash + ".mov.dtsh"
	body := []byte("cold upload sidecar")
	up := upstreamServer(t, body)
	defer up.Close()

	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:            pb.AssetState_ASSET_STATE_PLAYABLE,
		DtshPresignedGet: up.URL,
	}}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/upload/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	target := filepath.Join(dir, "upload", file)
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected upload dtsh to be cached locally: %v", err)
	}
}

func TestClipDtshPutLandsNextToNestedClipMedia(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv.dtsh"
	body := []byte("nested generated dtsh bytes")

	fz := &fakeFreeze{}
	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, fz)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/internal/artifact/clip/streamA/"+file, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	target := filepath.Join(dir, "clips", "streamA", file)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected nested dtsh on disk: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "clips", file)); !os.IsNotExist(err) {
		t.Fatalf("flat clip sidecar should not be written, stat err=%v", err)
	}
	if len(fz.calls) != 1 || !strings.Contains(fz.calls[0], target) {
		t.Fatalf("freeze handoff not called for nested path: %v", fz.calls)
	}
}

func TestDtshPutUnexpectedEOFDoesNotReportSuccess(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv.dtsh"

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPut, "/internal/artifact/vod/"+file, &unexpectedEOFReader{})
	c.Params = gin.Params{{Key: "file", Value: file}}

	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, nil)
	s.putSidecarWithStream(c, "vod", "")

	if w.Code == http.StatusOK {
		t.Fatal("incomplete sidecar upload must not report 200 OK")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", w.Code, http.StatusBadRequest)
	}
	if _, err := os.Stat(filepath.Join(dir, "vod", file)); !os.IsNotExist(err) {
		t.Fatalf("incomplete sidecar must not be durable, stat err=%v", err)
	}
}

func TestDtshPutEmptyBodyDoesNotReportSuccess(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv.dtsh"

	s := newTestServer(t, dir, admission.CacheToDisk, &fakeResolver{}, nil)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, ts.URL+"/internal/artifact/vod/"+file, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("empty sidecar upload must not report 200 OK")
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if _, err := os.Stat(filepath.Join(dir, "vod", file)); !os.IsNotExist(err) {
		t.Fatalf("empty sidecar must not be durable, stat err=%v", err)
	}
}

func TestDtshGetCold404ForwardsToMistGeneration(t *testing.T) {
	dir := t.TempDir()
	hash := "abc"
	file := hash + ".mkv.dtsh"
	// Resolver returns playable media but no dtsh sidecar.
	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: "http://unused",
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for missing dtsh; got %d", resp.StatusCode)
	}
}

func TestDtshGetLocalMediaStillChecksResolvedSidecar(t *testing.T) {
	dir := t.TempDir()
	hash := "localvod"
	mediaFile := hash + ".mkv"
	mediaPath := filepath.Join(dir, "vod", mediaFile)
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("local mkv bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := []byte("dtsh from s3")
	up := upstreamServer(t, body)
	defer up.Close()

	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: {
		State:            pb.AssetState_ASSET_STATE_PLAYABLE,
		DtshPresignedGet: up.URL,
		URLTTLSeconds:    60,
	}}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/vod/"+mediaFile+".dtsh")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got=%q want=%q", got, body)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", resolver.calls)
	}
}

func TestDtshGetResolveErrorReturns404GenerationSignal(t *testing.T) {
	dir := t.TempDir()
	hash := "localvod"
	file := hash + ".mkv.dtsh"

	resolver := &fakeResolver{err: fmt.Errorf("control stream unavailable")}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 generation signal for dtsh resolve failure; got %d", resp.StatusCode)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", resolver.calls)
	}
}

func TestDtshGetUpstreamErrorReturns404GenerationSignal(t *testing.T) {
	dir := t.TempDir()
	hash := "sidecar500"
	file := hash + ".mkv.dtsh"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "s3 unavailable", http.StatusInternalServerError)
	}))
	defer up.Close()

	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: {
		State:            pb.AssetState_ASSET_STATE_PLAYABLE,
		DtshPresignedGet: up.URL,
		URLTTLSeconds:    60,
	}}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doMistGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 generation signal for dtsh upstream failure; got %d", resp.StatusCode)
	}
}

func TestColdHEADDoesNotTriggerFullDownload(t *testing.T) {
	// Mist's HTTP::URIReader HEADs first to learn Content-Length +
	// Accept-Ranges; the cold path must not write-through-cache a full GET
	// in response. Verifies the relay forwards HEAD upstream and leaves
	// disk untouched.
	dir := t.TempDir()
	hash := "h1"
	file := hash + ".mkv"
	body := []byte("body for HEAD probe")
	up := upstreamServer(t, body)
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, ts.URL+"/internal/artifact/vod/"+file, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status=%d", resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD content-length=%q want %d", cl, len(body))
	}
	if got, _ := io.ReadAll(resp.Body); len(got) != 0 {
		t.Fatalf("HEAD body should be empty; got %d bytes", len(got))
	}
	// HEAD must not create the block-cache dir either — no fetch happened.
	if _, err := os.Stat(filepath.Join(dir, "vod", file+".blocks")); !os.IsNotExist(err) {
		t.Fatalf("HEAD probe must not create block cache dir; stat err=%v", err)
	}
}

func TestColdHEADUsesRangeGetWhenUpstreamRejectsHEAD(t *testing.T) {
	dir := t.TempDir()
	hash := "head-range"
	file := hash + ".mov"
	body := []byte("seekable upload body")
	var headCalls int32
	var rangeGetCalls int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			atomic.AddInt32(&headCalls, 1)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet && r.Header.Get("Range") == "bytes=0-0" {
			atomic.AddInt32(&rangeGetCalls, 1)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(body)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[:1])
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: res}}
	s := newTestServer(t, dir, admission.CacheToDisk, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodHead, ts.URL+"/internal/artifact/upload/"+file, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status=%d", resp.StatusCode)
	}
	if cl := resp.Header.Get("Content-Length"); cl != strconv.Itoa(len(body)) {
		t.Fatalf("HEAD content-length=%q want %d", cl, len(body))
	}
	if atomic.LoadInt32(&headCalls) != 0 {
		t.Fatalf("relay should not forward HEAD to presigned GET URL")
	}
	if atomic.LoadInt32(&rangeGetCalls) != 1 {
		t.Fatalf("expected exactly one range GET probe, got %d", rangeGetCalls)
	}
	if got, _ := io.ReadAll(resp.Body); len(got) != 0 {
		t.Fatalf("HEAD body should be empty; got %d bytes", len(got))
	}
}

func TestUploadRangedGETRetriesFullOn416(t *testing.T) {
	dir := t.TempDir()
	hash := "uploadretry"
	file := hash + ".mov"
	body := []byte("full upload body")
	var rangedGets int32
	var fullGets int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Header.Get("Range") != "" {
			atomic.AddInt32(&rangedGets, 1)
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		atomic.AddInt32(&fullGets, 1)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	defer up.Close()

	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL,
	}}}
	s := newTestServer(t, dir, admission.CacheMemoryOnly, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/upload/"+file, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=999999-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("body=%q want %q", got, body)
	}
	if atomic.LoadInt32(&rangedGets) != 1 || atomic.LoadInt32(&fullGets) != 1 {
		t.Fatalf("upstream ranged/full GETs = %d/%d, want 1/1", rangedGets, fullGets)
	}
}

func TestUploadIgnoresZeroByteLocalPlaceholder(t *testing.T) {
	dir := t.TempDir()
	hash := "zeroupload"
	file := hash + ".mov"
	localPath := filepath.Join(dir, "upload", file)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(localPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	up := upstreamServer(t, []byte("remote upload body"))
	defer up.Close()

	resolver := &fakeResolver{out: map[string]*ResolveResult{"upload/" + hash: {
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL,
	}}}
	s := newTestServer(t, dir, admission.CacheMemoryOnly, resolver, nil)
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/upload/"+file, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=0-5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status=%d want 206", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote" {
		t.Fatalf("body=%q want remote", got)
	}
}

func TestColdRangedGETServesAndCachesBlock(t *testing.T) {
	// Ranged GET returns a real 206 directly from S3 and the served
	// block lands in the on-disk block cache so subsequent ranged
	// readers hit it instead of re-fetching. Uses a small block size so
	// the test body crosses block boundaries.
	dir := t.TempDir()
	hash := "h2"
	file := hash + ".mkv"
	body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV") // 32 bytes
	up := upstreamServer(t, body)
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 8, // 4 blocks of 8 bytes each
	})
	ts := mount(t, s)
	defer ts.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/vod/"+file, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=4-9")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("ranged GET status=%d want 206", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "456789" {
		t.Fatalf("ranged body=%q want %q", got, "456789")
	}

	// Range [4,9] spans blocks 0 (bytes 0-7) and 1 (bytes 8-15). The
	// served-range Content-Length is 6 bytes, so the client's ReadAll
	// returns once those land — but each block's full 8-byte cache
	// fill continues on the server side after the client is satisfied
	// (stream-first: cache write is a side effect of the in-flight S3
	// read, not a prerequisite for serving). Poll for the blocks to
	// appear on disk rather than asserting synchronously.
	blocksDir := filepath.Join(dir, "vod", file+".blocks")
	block0 := filepath.Join(blocksDir, "0000000000.blk")
	block1 := filepath.Join(blocksDir, "0000000001.blk")
	deadline := time.Now().Add(2 * time.Second)
	for _, p := range []string{block0, block1} {
		for {
			if _, err := os.Stat(p); err == nil {
				break
			}
			if time.Now().After(deadline) {
				entries, _ := os.ReadDir(blocksDir)
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Fatalf("expected block on disk after ranged fetch: %s\nblocks dir contents: %v", p, names)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Block 3 (bytes 24-31) should NOT have been fetched — outside the range.
	block3 := filepath.Join(blocksDir, "0000000003.blk")
	if _, err := os.Stat(block3); !os.IsNotExist(err) {
		t.Fatalf("unrelated block should not have been fetched: %s err=%v", block3, err)
	}
}

func TestBlockServeRangeFromWarmBlocks(t *testing.T) {
	// Second ranged GET hits the on-disk block — no S3 call should
	// happen. Tracked via a counter on the upstream fake.
	dir := t.TempDir()
	hash := "h3"
	file := hash + ".mkv"
	body := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUV") // 32 bytes
	var s3Calls int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&s3Calls, 1)
		size := int64(len(body))
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			spec := strings.TrimPrefix(rng, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := size - 1
			if len(parts) == 2 && parts[1] != "" {
				if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					end = e
				}
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body[start : end+1])
			return
		}
		_, _ = w.Write(body)
	}))
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 8,
	})
	ts := mount(t, s)
	defer ts.Close()

	doRanged := func(rng string) []byte {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/vod/"+file, nil)
		req.Header.Set("Range", rng)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	if got := doRanged("bytes=0-7"); string(got) != "01234567" {
		t.Fatalf("first range body=%q", got)
	}
	firstCallCount := atomic.LoadInt32(&s3Calls)
	if firstCallCount == 0 {
		t.Fatalf("expected at least one S3 call on cold fetch")
	}
	if got := doRanged("bytes=0-7"); string(got) != "01234567" {
		t.Fatalf("second range body=%q", got)
	}
	if atomic.LoadInt32(&s3Calls) != firstCallCount {
		t.Fatalf("second ranged GET should hit warm block, not S3; first=%d after=%d", firstCallCount, s3Calls)
	}
}

func TestColdRangedGETStreamsFirstByteBeforeFullBlock(t *testing.T) {
	// The cold-fetch path must stream bytes to the client as they arrive
	// from S3, not buffer the whole block before sending byte 1. The
	// fixture simulates a slow S3 by sleeping between chunks of the
	// response body — the relay should write the first byte to the
	// client well before the upstream finishes the full block.
	dir := t.TempDir()
	hash := "stream"
	file := hash + ".mkv"
	const blockBytes = 1024
	body := bytes.Repeat([]byte{'x'}, blockBytes)

	// Slow upstream: writes the response body in 4 chunks with 100ms
	// gaps. Full body takes ~400ms; the first chunk arrives in ~0ms.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		size := int64(len(body))
		w.Header().Set("Accept-Ranges", "bytes")
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			spec := strings.TrimPrefix(rng, "bytes=")
			parts := strings.SplitN(spec, "-", 2)
			start, _ := strconv.ParseInt(parts[0], 10, 64)
			end := size - 1
			if len(parts) == 2 && parts[1] != "" {
				if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					end = e
				}
			}
			slice := body[start : end+1]
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
			w.Header().Set("Content-Length", strconv.Itoa(len(slice)))
			w.WriteHeader(http.StatusPartialContent)
			flusher, _ := w.(http.Flusher)
			chunk := len(slice) / 4
			for i := 0; i < 4; i++ {
				lo := i * chunk
				hi := lo + chunk
				if i == 3 {
					hi = len(slice)
				}
				_, _ = w.Write(slice[lo:hi])
				if flusher != nil {
					flusher.Flush()
				}
				if i < 3 {
					time.Sleep(100 * time.Millisecond)
				}
			}
			return
		}
		_, _ = w.Write(body)
	}))
	defer up.Close()

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	// Block size equals body size so the request spans exactly one block;
	// the slow upstream simulates per-chunk arrival within that block.
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: blockBytes,
	})
	ts := mount(t, s)
	defer ts.Close()

	// Request the first 1 byte of the block. Buffered-block behavior
	// would wait ~400ms (full block download) before sending byte 1;
	// streaming behavior sends byte 1 in ~0ms.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ts.URL+"/internal/artifact/vod/"+file, nil)
	req.Header.Set("Range", "bytes=0-0")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status=%d want 206", resp.StatusCode)
	}
	// Read exactly 1 byte and measure how long it took. With buffered
	// block fill it would be ~400ms; streaming must be much faster.
	one := make([]byte, 1)
	if _, err := io.ReadFull(resp.Body, one); err != nil {
		t.Fatalf("read first byte: %v", err)
	}
	firstByteAt := time.Since(start)
	if firstByteAt > 200*time.Millisecond {
		t.Fatalf("first byte took %v — streaming should deliver it well before full block (~400ms)", firstByteAt)
	}
}

func TestColdFetchDiskWriteFailureKeepsServingClient(t *testing.T) {
	// If the cache write fails mid-stream (full disk, permissions),
	// the relay must keep streaming bytes to the client — disk cache
	// is a side effect, not a prerequisite for playback.
	dir := t.TempDir()
	hash := "tolerant"
	file := hash + ".mkv"
	body := bytes.Repeat([]byte{'y'}, 256)
	up := upstreamServer(t, body)
	defer up.Close()

	// Pre-create the .blocks dir as a regular FILE so the relay's
	// MkdirAll succeeds (it'll be a no-op against the existing entry's
	// parent) but the block tmpfile open will fail because the path
	// can't be a child of a file.
	blocksParent := filepath.Join(dir, "vod", file+".blocks")
	if err := os.MkdirAll(filepath.Dir(blocksParent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocksParent, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + hash: res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 64,
	})
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("client must still receive full body even when disk cache fails; got %d bytes want %d", len(got), len(body))
	}
}

func TestBlockCacheInvalidatesOnAssetHashMismatch(t *testing.T) {
	// Reprocessing an artifact changes its content-addressed hash. The
	// block cache must drop stale blocks when meta.json's AssetHash
	// doesn't match the resolve, so a new hash's bytes don't get served
	// out of an old hash's blocks.
	dir := t.TempDir()
	file := "abc.mkv"
	blocksDir := filepath.Join(dir, "vod", file+".blocks")
	if err := os.MkdirAll(blocksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-populate with a stale meta + fake block from a "previous" version.
	stale := BlockMeta{AssetHash: "OLDHASH", TotalSize: 1024, BlockSize: 16}
	staleJSON, _ := json.Marshal(stale)
	_ = os.WriteFile(filepath.Join(blocksDir, "meta.json"), staleJSON, 0o644)
	_ = os.WriteFile(filepath.Join(blocksDir, "0000000000.blk"), []byte("stale-content-1234"), 0o644)

	body := []byte("new content for fresh hash")
	up := upstreamServer(t, body)
	defer up.Close()
	newHash := "abc"
	res := &ResolveResult{
		State:             pb.AssetState_ASSET_STATE_PLAYABLE,
		MediaPresignedURL: up.URL + "/o",
		ExpectedSizeBytes: uint64(len(body)),
		URLTTLSeconds:     60,
	}
	resolver := &fakeResolver{out: map[string]*ResolveResult{"vod/" + newHash: res}}
	s := New(Options{
		BasePath:  dir,
		Admitter:  &fakeAdmitter{decision: admission.CacheToDisk},
		Resolver:  resolver,
		BlockSize: 16,
	})
	ts := mount(t, s)
	defer ts.Close()

	resp, err := doGet(t, ts.URL+"/internal/artifact/vod/"+file)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Fatalf("expected fresh body after invalidation; got=%q want=%q", got, body)
	}
	// Meta should now reflect the new hash.
	metaBytes, err := os.ReadFile(filepath.Join(blocksDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m BlockMeta
	if err := json.Unmarshal(metaBytes, &m); err != nil {
		t.Fatal(err)
	}
	if m.AssetHash != newHash {
		t.Fatalf("meta.AssetHash=%q want %q (stale meta not replaced)", m.AssetHash, newHash)
	}
}
