package leases

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// fakeSegmentIndex records AcquireView/ReleaseView calls.
type fakeSegmentIndex struct {
	mu       sync.Mutex
	acquired map[string]int
}

func newFakeSegmentIndex() *fakeSegmentIndex {
	return &fakeSegmentIndex{acquired: make(map[string]int)}
}

func (f *fakeSegmentIndex) AcquireView(dvrHash, segmentName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired[dvrHash+"|"+segmentName]++
}

func (f *fakeSegmentIndex) ReleaseView(dvrHash, segmentName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquired[dvrHash+"|"+segmentName]--
}

func (f *fakeSegmentIndex) count(dvrHash, segmentName string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired[dvrHash+"|"+segmentName]
}

func TestSourceLease_AcquireReleaseProtectsPath(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("vod+abc", []string{"/data/vod/abc.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)

	if !tr.IsPathLeased("/data/vod/abc.mp4") {
		t.Fatalf("expected path to be leased after AcquireSource")
	}

	tr.ReleaseSource("vod+abc")
	if tr.IsPathLeased("/data/vod/abc.mp4") {
		t.Fatalf("expected path to be unleased after ReleaseSource")
	}
}

func TestSourceLease_DVRFansOutSegmentViews(t *testing.T) {
	seg := newFakeSegmentIndex()
	tr := NewTracker(seg, NewHeatTracker())

	key := AssetKey{Type: "dvr", Hash: "dvr1"}
	tr.AcquireSource("dvr+rolling1", []string{"/data/dvr/s/dvr1/dvr1.m3u8"}, key, []string{"seg-1.ts", "seg-2.ts"}, false)

	if got := seg.count("dvr1", "seg-1.ts"); got != 1 {
		t.Fatalf("seg-1 expected refcount 1, got %d", got)
	}
	if got := seg.count("dvr1", "seg-2.ts"); got != 1 {
		t.Fatalf("seg-2 expected refcount 1, got %d", got)
	}

	tr.ReleaseSource("dvr+rolling1")
	if got := seg.count("dvr1", "seg-1.ts"); got != 0 {
		t.Fatalf("seg-1 expected refcount 0 after release, got %d", got)
	}
}

func TestViewerLease_IdempotentRefireDoesNotDoubleBumpHeatOrViews(t *testing.T) {
	seg := newFakeSegmentIndex()
	heat := NewHeatTracker()
	tr := NewTracker(seg, heat)

	// Establish a source lease so DVR ActiveViews would be visible if
	// viewer churn touched them.
	key := AssetKey{Type: "dvr", Hash: "dvr1"}
	tr.AcquireSource("dvr+rolling1", []string{"/dvr/rolling1.m3u8"}, key, []string{"seg-1.ts"}, false)
	startViews := seg.count("dvr1", "seg-1.ts")

	// First viewer.
	tr.AcquireViewer("session-1", "dvr+rolling1", "/dvr/rolling1.m3u8")
	if got, _ := heat.Lookup("/dvr/rolling1.m3u8"); got.AccessCount != 1 {
		t.Fatalf("expected heat=1 after first viewer, got %d", got.AccessCount)
	}

	// Refire of same session_id (auth invalidation case).
	tr.AcquireViewer("session-1", "dvr+rolling1", "/dvr/rolling1.m3u8")
	if got, _ := heat.Lookup("/dvr/rolling1.m3u8"); got.AccessCount != 1 {
		t.Fatalf("expected heat=1 after refire of same session, got %d", got.AccessCount)
	}
	if got := seg.count("dvr1", "seg-1.ts"); got != startViews {
		t.Fatalf("viewer refire must not touch segment ActiveViews: expected %d, got %d", startViews, got)
	}

	tr.ReleaseViewer("session-1")
	if got, _ := heat.Lookup("/dvr/rolling1.m3u8"); got.AccessCount != 1 {
		t.Fatalf("heat count is monotonic; expected 1 after release, got %d", got.AccessCount)
	}
}

func TestIsPathLeased_AnyLeaseTypePins(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	path := "/data/vod/file.mp4"

	tr.AcquireViewer("sess-1", "vod+abc", path)
	if !tr.IsPathLeased(path) {
		t.Fatalf("viewer lease should pin path")
	}
	tr.ReleaseViewer("sess-1")
	if tr.IsPathLeased(path) {
		t.Fatalf("path should clear after viewer release")
	}

	tr.AcquireSource("vod+abc", []string{path}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)
	if !tr.IsPathLeased(path) {
		t.Fatalf("source lease should pin path")
	}

	// Both held: still leased.
	tr.AcquireViewer("sess-2", "vod+abc", path)
	if !tr.IsPathLeased(path) {
		t.Fatalf("both leases held: path leased")
	}
	tr.ReleaseViewer("sess-2")
	if !tr.IsPathLeased(path) {
		t.Fatalf("source still held: path leased")
	}
	tr.ReleaseSource("vod+abc")
	if tr.IsPathLeased(path) {
		t.Fatalf("both released: path unleased")
	}
}

func TestIsAssetLeased_DVRMatchesByHash(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("dvr+rolling1", []string{"/m1"}, AssetKey{Type: "dvr", Hash: "dvr1"}, nil, false)

	if !tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: "dvr1"}) {
		t.Fatalf("expected dvr1 to be asset-leased")
	}
	if tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: "dvr2"}) {
		t.Fatalf("did not expect dvr2 to be asset-leased")
	}
}

func TestReconcileSources_2StrikesReleasesAbsent(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("vod+a", []string{"/a"}, AssetKey{Type: "vod", Hash: "a"}, nil, false)
	tr.AcquireSource("vod+b", []string{"/b"}, AssetKey{Type: "vod", Hash: "b"}, nil, false)

	// First poll: only 'a' present.
	tr.ReconcileSources(map[string]struct{}{"vod+a": {}})
	if tr.SourceCount() != 2 {
		t.Fatalf("expected no releases after 1 strike, got SourceCount=%d", tr.SourceCount())
	}

	// Second poll: still only 'a'.
	released := tr.ReconcileSources(map[string]struct{}{"vod+a": {}})
	if len(released) != 1 || released[0] != "vod+b" {
		t.Fatalf("expected release of vod+b after 2 strikes, got %v", released)
	}
	if tr.IsPathLeased("/b") {
		t.Fatalf("/b should be unleased after reconciliation drop")
	}
}

func TestReconcileSources_PresentResetsStrikes(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("vod+a", []string{"/a"}, AssetKey{Type: "vod", Hash: "a"}, nil, false)

	tr.ReconcileSources(map[string]struct{}{})             // 1 strike
	tr.ReconcileSources(map[string]struct{}{"vod+a": {}})  // reset
	released := tr.ReconcileSources(map[string]struct{}{}) // 1 strike again
	if len(released) != 0 {
		t.Fatalf("expected no release after strikes reset, got %v", released)
	}
}

func TestReconcileViewers_2StrikesReleasesAbsent(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireViewer("s1", "vod+a", "/a")
	tr.AcquireViewer("s2", "vod+b", "/b")

	tr.ReconcileViewers(map[string]struct{}{"s1": {}}) // strike 1 for s2
	released := tr.ReconcileViewers(map[string]struct{}{"s1": {}})
	if len(released) != 1 || released[0] != "s2" {
		t.Fatalf("expected release of s2 after 2 strikes, got %v", released)
	}
}

func TestDegradedDvr_PausesUntilRelease(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	if tr.DegradedDvrCleanupActive() {
		t.Fatalf("expected non-degraded at start")
	}
	tr.AcquireSource("dvr+x", []string{"/x"}, AssetKey{Type: "dvr", Hash: "h"}, nil, true)
	if !tr.DegradedDvrCleanupActive() {
		t.Fatalf("expected degraded after acquiring degraded source")
	}
	tr.ReleaseSource("dvr+x")
	if tr.DegradedDvrCleanupActive() {
		t.Fatalf("expected non-degraded after release")
	}
}

func TestHasSourceLease(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	if tr.HasSourceLease("vod+abc") {
		t.Fatal("expected no lease before acquire")
	}
	tr.AcquireSource("vod+abc", []string{"/data/vod/abc.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)
	if !tr.HasSourceLease("vod+abc") {
		t.Fatal("expected lease after acquire")
	}
	if tr.HasSourceLease("") {
		t.Fatal("empty stream name must never report a lease")
	}
	var nilTr *Tracker
	if nilTr.HasSourceLease("vod+abc") {
		t.Fatal("nil tracker must report no lease")
	}
}

func TestDegradedVodCleanupActive(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	if tr.DegradedVodCleanupActive() {
		t.Fatal("expected non-degraded at start")
	}
	tr.AcquireSource("vod+x", []string{"/x.mp4"}, AssetKey{Type: "vod", Hash: "h"}, nil, true)
	if !tr.DegradedVodCleanupActive() {
		t.Fatal("expected degraded after acquiring a degraded VOD source")
	}
	tr.ReleaseSource("vod+x")
	if tr.DegradedVodCleanupActive() {
		t.Fatal("expected non-degraded after release")
	}
}

func TestViewerCount(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	if tr.ViewerCount() != 0 {
		t.Fatalf("expected 0 viewers initially, got %d", tr.ViewerCount())
	}
	tr.AcquireViewer("s1", "live+stream", "/data/vod/abc.mp4")
	tr.AcquireViewer("s2", "live+stream", "/data/vod/abc.mp4")
	if tr.ViewerCount() != 2 {
		t.Fatalf("expected 2 viewers, got %d", tr.ViewerCount())
	}
	tr.ReleaseViewer("s1")
	if tr.ViewerCount() != 1 {
		t.Fatalf("expected 1 viewer after release, got %d", tr.ViewerCount())
	}
}

// DeletePathIfUnleased is the TOCTOU-safe unlink: it must refuse while a source
// or viewer lease pins the path, and unlink the real file once unleased.
func TestDeletePathIfUnleased(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.mp4")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("vod+abc", []string{path}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)

	if err := tr.DeletePathIfUnleased(path); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("expected ErrLeaseHeld while leased, got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("file must survive a refused delete")
	}

	tr.ReleaseSource("vod+abc")
	if err := tr.DeletePathIfUnleased(path); err != nil {
		t.Fatalf("delete after release failed: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file must be gone after a successful delete")
	}

	if err := tr.DeletePathIfUnleased(""); err == nil {
		t.Fatal("empty path must error")
	}
}

// DeleteDVRDirIfUnleased must refuse while a matching DVR asset lease is held
// or degraded-cleanup is active, and recursively remove the tree once clear.
func TestDeleteDVRDirIfUnleased(t *testing.T) {
	base := t.TempDir()
	dvrDir := filepath.Join(base, "dvr", "stream", "h")
	if err := os.MkdirAll(filepath.Join(dvrDir, "segments"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dvrDir, "segments", "seg.ts"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tr := NewTracker(nil, NewHeatTracker())
	tr.AcquireSource("dvr+rolling", []string{filepath.Join(dvrDir, "h.m3u8")}, AssetKey{Type: "dvr", Hash: "h"}, nil, false)

	if err := tr.DeleteDVRDirIfUnleased(dvrDir, "h"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("expected ErrLeaseHeld while DVR asset leased, got %v", err)
	}
	if _, err := os.Stat(dvrDir); err != nil {
		t.Fatal("dir must survive a refused delete")
	}

	tr.ReleaseSource("dvr+rolling")
	if err := tr.DeleteDVRDirIfUnleased(dvrDir, "h"); err != nil {
		t.Fatalf("delete after release failed: %v", err)
	}
	if _, err := os.Stat(dvrDir); !os.IsNotExist(err) {
		t.Fatal("dir must be gone after a successful delete")
	}

	if err := tr.DeleteDVRDirIfUnleased("", "h"); err == nil {
		t.Fatal("empty dvr dir must error")
	}
}
