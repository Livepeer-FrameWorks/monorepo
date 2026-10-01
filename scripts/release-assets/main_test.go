package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGitHub serves the release endpoints the publisher uses from memory. Its hooks inject failures per request.
type fakeGitHub struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	release map[string]any
	assets  map[int64]*remoteAsset
	nextID  int64
	// inFlight and maxInFlight count concurrent requests.
	inFlight, maxInFlight int
	requests              []string
	createdWith           map[string]any
	patchedWith           map[string]any
	// fail, when it returns true, has already written the response for this request.
	fail func(w http.ResponseWriter, r *http.Request, n int) bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, assets: map[int64]*remoteAsset{}, nextID: 100}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	n := len(f.requests)
	fail := f.fail
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	// Give an overlapping request the chance to show up in inFlight.
	time.Sleep(2 * time.Millisecond)
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if fail != nil && fail(w, r, n) {
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/tags/v1.0.0":
		if f.release == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, f.release)
	case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/releases":
		if f.release != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
			return
		}
		f.createdWith = map[string]any{}
		_ = json.Unmarshal(body, &f.createdWith)
		f.release = map[string]any{"id": 7, "upload_url": f.server.URL + "/uploads/repos/o/r/releases/7/assets{?name,label}"}
		writeJSON(w, http.StatusCreated, f.release)
	case r.Method == http.MethodPatch && r.URL.Path == "/repos/o/r/releases/7":
		f.patchedWith = map[string]any{}
		_ = json.Unmarshal(body, &f.patchedWith)
		writeJSON(w, http.StatusOK, f.release)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/releases/7/assets":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		var all []remoteAsset
		for id := int64(0); id < f.nextID+1; id++ {
			if a, ok := f.assets[id]; ok {
				all = append(all, *a)
			}
		}
		start := min((page-1)*perPage, len(all))
		end := min(start+perPage, len(all))
		writeJSON(w, http.StatusOK, all[start:end])
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/o/r/releases/assets/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/o/r/releases/assets/"), 10, 64)
		if _, ok := f.assets[id]; !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		delete(f.assets, id)
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && r.URL.Path == "/uploads/repos/o/r/releases/7/assets":
		name := r.URL.Query().Get("name")
		for _, a := range f.assets {
			if a.Name == name {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed", "errors": []any{map[string]any{"code": "already_exists"}}})
				return
			}
		}
		sum := sha256.Sum256(body)
		f.nextID++
		a := &remoteAsset{ID: f.nextID, Name: name, Size: int64(len(body)), State: "uploaded", Digest: "sha256:" + hex.EncodeToString(sum[:])}
		f.assets[a.ID] = a
		writeJSON(w, http.StatusCreated, a)
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusTeapot)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeGitHub) addAsset(name string, content []byte, state string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	sum := sha256.Sum256(content)
	f.nextID++
	f.assets[f.nextID] = &remoteAsset{ID: f.nextID, Name: name, Size: int64(len(content)), State: state, Digest: "sha256:" + hex.EncodeToString(sum[:])}
	return f.nextID
}

func (f *fakeGitHub) assetsByName() map[string]remoteAsset {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]remoteAsset{}
	for _, a := range f.assets {
		out[a.Name] = *a
	}
	return out
}

func (f *fakeGitHub) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// testClient returns a client against the fake whose sleeps are recorded instead of taken, on a clock they advance.
func testClient(f *fakeGitHub, log io.Writer) (*client, *[]time.Duration) {
	c := newClient(f.server.URL, "test-token", log)
	var slept []time.Duration
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }
	c.sleep = func(d time.Duration) {
		slept = append(slept, d)
		clock = clock.Add(d)
	}
	return c, &slept
}

func writeFiles(t *testing.T, files map[string]string) []string {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

func rcOptions(files []string) options {
	return options{repo: "o/r", tag: "v1.0.0", name: "v1.0.0", prerelease: true, makeLatest: "false", generateNotes: true, files: files}
}

func secondaryLimits(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{"message": "You have exceeded a secondary rate limit. Please wait a few minutes before you try again."})
}

func TestCreatesReleaseAndUploadsAssetsOneAtATime(t *testing.T) {
	f := newFakeGitHub(t)
	files := map[string]string{}
	for i := range 120 {
		files[fmt.Sprintf("asset-%03d.tar.gz", i)] = strings.Repeat("x", i)
	}
	c, _ := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, files))); err != nil {
		t.Fatal(err)
	}
	if f.maxInFlight != 1 {
		t.Fatalf("max concurrent requests = %d, want 1", f.maxInFlight)
	}
	want := map[string]any{"tag_name": "v1.0.0", "name": "v1.0.0", "prerelease": true, "make_latest": "false", "generate_release_notes": true}
	for k, v := range want {
		if f.createdWith[k] != v {
			t.Fatalf("create release %s = %v, want %v (body %v)", k, f.createdWith[k], v, f.createdWith)
		}
	}
	got := f.assetsByName()
	if len(got) != len(files) {
		t.Fatalf("release has %d assets, want %d", len(got), len(files))
	}
	for name, content := range files {
		sum := sha256.Sum256([]byte(content))
		if got[name].Digest != "sha256:"+hex.EncodeToString(sum[:]) {
			t.Fatalf("asset %s digest = %s", name, got[name].Digest)
		}
	}
}

func TestRerunKeepsIdenticalAssetsAndReplacesChangedOrIncompleteOnes(t *testing.T) {
	f := newFakeGitHub(t)
	f.release = map[string]any{"id": 7, "upload_url": f.server.URL + "/uploads/repos/o/r/releases/7/assets{?name,label}"}
	paths := writeFiles(t, map[string]string{"same.tar.gz": "same", "changed.tar.gz": "new", "partial.zip": "full", "missing.sha256": "abc"})
	sameID := f.addAsset("same.tar.gz", []byte("same"), "uploaded")
	changedID := f.addAsset("changed.tar.gz", []byte("old"), "uploaded")
	partialID := f.addAsset("partial.zip", []byte("full"), "starter")
	f.addAsset("unlisted.txt", []byte("other"), "uploaded")
	c, _ := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(paths)); err != nil {
		t.Fatal(err)
	}
	if f.createdWith != nil {
		t.Fatalf("existing release was created again: %v", f.createdWith)
	}
	if f.patchedWith["prerelease"] != true || f.patchedWith["make_latest"] != "false" || f.patchedWith["name"] != "v1.0.0" {
		t.Fatalf("existing release patched with %v", f.patchedWith)
	}
	if _, ok := f.patchedWith["body"]; ok {
		t.Fatal("existing release notes were rewritten")
	}
	got := f.assetsByName()
	if got["same.tar.gz"].ID != sameID {
		t.Fatal("identical asset was replaced")
	}
	if got["changed.tar.gz"].ID == changedID || got["changed.tar.gz"].Size != 3 {
		t.Fatalf("changed asset was not replaced: %+v", got["changed.tar.gz"])
	}
	if got["partial.zip"].ID == partialID || got["partial.zip"].State != "uploaded" {
		t.Fatalf("incomplete asset was not replaced: %+v", got["partial.zip"])
	}
	if _, ok := got["missing.sha256"]; !ok {
		t.Fatal("missing asset was not uploaded")
	}
	if _, ok := got["unlisted.txt"]; !ok {
		t.Fatal("an asset that is not in the list was removed")
	}
	if uploads := f.count("POST /uploads/"); uploads != 3 {
		t.Fatalf("uploads = %d, want 3", uploads)
	}
}

func TestWaitsAsLongAsRetryAfterAsks(t *testing.T) {
	f := newFakeGitHub(t)
	limited := false
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/uploads/") && !limited {
			limited = true
			w.Header().Set("Retry-After", "37")
			secondaryLimits(w)
			return true
		}
		return false
	}
	c, slept := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a"}))); err != nil {
		t.Fatal(err)
	}
	if !containsWait(*slept, 37*time.Second) {
		t.Fatalf("waits = %v, want one of 37s", *slept)
	}
	if len(f.assetsByName()) != 1 {
		t.Fatal("asset was not uploaded after the rate limit")
	}
}

func TestSecondaryLimitWithoutRetryAfterWaitsAtLeastAMinuteAndBacksOff(t *testing.T) {
	f := newFakeGitHub(t)
	limits := 0
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/uploads/") && limits < 2 {
			limits++
			secondaryLimits(w)
			return true
		}
		return false
	}
	c, slept := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a"}))); err != nil {
		t.Fatal(err)
	}
	if !containsWait(*slept, time.Minute) || !containsWait(*slept, 2*time.Minute) {
		t.Fatalf("waits = %v, want 1m then 2m", *slept)
	}
}

func TestPrimaryLimitWaitsForReset(t *testing.T) {
	f := newFakeGitHub(t)
	limited := false
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method == http.MethodGet && !limited {
			limited = true
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(1_700_000_000+600, 10))
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "API rate limit exceeded"})
			return true
		}
		return false
	}
	c, slept := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a"}))); err != nil {
		t.Fatal(err)
	}
	if !containsWait(*slept, 601*time.Second) {
		t.Fatalf("waits = %v, want 601s (until the reset)", *slept)
	}
}

func TestDroppedUploadIsRetriedAndALeftoverAssetReconciled(t *testing.T) {
	f := newFakeGitHub(t)
	dropped := 0
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/uploads/") || dropped >= 2 {
			return false
		}
		dropped++
		if dropped == 2 {
			// The second drop happens after GitHub stored the asset, so the retry finds it already there.
			body, _ := io.ReadAll(r.Body)
			f.addAsset(r.URL.Query().Get("name"), body, "uploaded")
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		return true
	}
	var log bytes.Buffer
	c, _ := testClient(f, &log)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a", "b.tar.gz": "b"}))); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	got := f.assetsByName()
	if len(got) != 2 || got["a.tar.gz"].State != "uploaded" || got["b.tar.gz"].State != "uploaded" {
		t.Fatalf("assets = %+v\n%s", got, log.String())
	}
	if !strings.Contains(log.String(), "already uploaded it") {
		t.Fatalf("leftover asset from the dropped attempt was not recognised:\n%s", log.String())
	}
}

func TestLeftoverPartialUploadIsReplaced(t *testing.T) {
	f := newFakeGitHub(t)
	dropped := false
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/uploads/") || dropped {
			return false
		}
		dropped = true
		f.addAsset(r.URL.Query().Get("name"), []byte("trunc"), "starter")
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
		return true
	}
	c, _ := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "complete"}))); err != nil {
		t.Fatal(err)
	}
	got := f.assetsByName()["a.tar.gz"]
	if got.State != "uploaded" || got.Size != int64(len("complete")) {
		t.Fatalf("asset = %+v", got)
	}
}

func TestWritesAreSpacedAtLeastASecondApart(t *testing.T) {
	f := newFakeGitHub(t)
	c, slept := testClient(f, io.Discard)
	clock := time.Unix(1_700_000_000, 0)
	var writes []time.Time
	c.now = func() time.Time { return clock }
	c.sleep = func(d time.Duration) {
		*slept = append(*slept, d)
		clock = clock.Add(d)
	}
	f.fail = func(_ http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method != http.MethodGet {
			writes = append(writes, clock)
		}
		return false
	}
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a": "a", "b": "b", "c": "c"}))); err != nil {
		t.Fatal(err)
	}
	if len(writes) != 4 {
		t.Fatalf("writes = %d, want 4 (create + 3 uploads)", len(writes))
	}
	for i := 1; i < len(writes); i++ {
		if gap := writes[i].Sub(writes[i-1]); gap < time.Second {
			t.Fatalf("write %d followed the previous one after %s", i, gap)
		}
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	f := newFakeGitHub(t)
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if strings.HasPrefix(r.URL.Path, "/uploads/") {
			w.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	}
	c, _ := testClient(f, io.Discard)
	err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a"})))
	if err == nil || !strings.Contains(err.Error(), "gave up after 8 attempts") {
		t.Fatalf("err = %v", err)
	}
	if uploads := f.count("POST /uploads/"); uploads != 8 {
		t.Fatalf("upload attempts = %d, want 8", uploads)
	}
}

func TestPermissionRefusalIsNotRetried(t *testing.T) {
	f := newFakeGitHub(t)
	f.fail = func(w http.ResponseWriter, r *http.Request, _ int) bool {
		if r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/releases" {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"})
			return true
		}
		return false
	}
	c, _ := testClient(f, io.Discard)
	err := publish(context.Background(), c, rcOptions(writeFiles(t, map[string]string{"a.tar.gz": "a"})))
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("err = %v", err)
	}
	if creates := f.count("POST /repos/o/r/releases"); creates != 1 {
		t.Fatalf("create attempts = %d, want 1", creates)
	}
}

func TestListsAssetsAcrossPages(t *testing.T) {
	f := newFakeGitHub(t)
	f.release = map[string]any{"id": 7, "upload_url": f.server.URL + "/uploads/repos/o/r/releases/7/assets{?name,label}"}
	files := map[string]string{}
	for i := range 150 {
		name := fmt.Sprintf("asset-%03d", i)
		files[name] = name
		f.addAsset(name, []byte(name), "uploaded")
	}
	c, _ := testClient(f, io.Discard)
	if err := publish(context.Background(), c, rcOptions(writeFiles(t, files))); err != nil {
		t.Fatal(err)
	}
	if uploads := f.count("POST /uploads/"); uploads != 0 {
		t.Fatalf("uploads = %d, want 0: every asset is on the second page or the first", uploads)
	}
}

func TestLocalAssetsSkipsMissingAndRefusesDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"one/a.tar.gz", "two/a.tar.gz"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(p), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var log bytes.Buffer
	assets, err := localAssets([]string{filepath.Join(dir, "one/a.tar.gz"), filepath.Join(dir, "absent"), filepath.Join(dir, "two")}, &log)
	if err != nil || len(assets) != 1 {
		t.Fatalf("assets = %v, err = %v", assets, err)
	}
	if !strings.Contains(log.String(), "absent does not exist") || !strings.Contains(log.String(), "is a directory") {
		t.Fatalf("log = %s", log.String())
	}
	if _, err := localAssets([]string{filepath.Join(dir, "one/a.tar.gz"), filepath.Join(dir, "two/a.tar.gz")}, io.Discard); err == nil {
		t.Fatal("two files with one base name were accepted")
	}
}

func containsWait(waits []time.Duration, want time.Duration) bool {
	for _, w := range waits {
		if w == want {
			return true
		}
	}
	return false
}
