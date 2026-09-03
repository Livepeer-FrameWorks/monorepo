package leases

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	tr.acquireSourceForTest("vod+abc", []string{"/data/vod/abc.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)

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
	tr.acquireSourceForTest("dvr+rolling1", []string{"/data/dvr/s/dvr1/dvr1.m3u8"}, key, []string{"seg-1.ts", "seg-2.ts"}, false)

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

// ReconcileResolvedSource must upgrade a degraded DVR lease (indexes, segment
// refcounts, degraded counter all consistent), then PRESERVE the resulting
// resolved lease — the poller's scan must never overwrite a STREAM_SOURCE-
// authoritative resolved lease, even if the scan supplies a different path.
func TestReconcileResolvedSource_UpgradesThenPreservesResolved(t *testing.T) {
	seg := newFakeSegmentIndex()
	tr := NewTracker(seg, NewHeatTracker())

	const stream = "dvr+int-x"
	// Type-only degraded lease: no paths, empty hash, no segments.
	tr.acquireSourceForTest(stream, nil, AssetKey{Type: "dvr"}, nil, true)
	if !tr.DegradedDvrCleanupActive() || !tr.IsSourceDegraded(stream) {
		t.Fatal("precondition: a degraded DVR lease should raise the pause")
	}

	const (
		dvrHash  = "hash-XYZ"
		manifest = "/data/dvr/s/hash-XYZ/hash-XYZ.m3u8"
	)
	if got := tr.ReconcileResolvedSource(stream, []string{manifest}, AssetKey{Type: "dvr", Hash: dvrHash}, []string{"seg-1.ts", "seg-2.ts"}, SourceEntry{StreamName: stream}); got != SourceUpgraded {
		t.Fatalf("degraded → resolved outcome = %v, want SourceUpgraded", got)
	}
	if tr.IsSourceDegraded(stream) || tr.DegradedDvrCleanupActive() {
		t.Fatal("pause must lift after upgrade (degradedDvrCount decremented)")
	}
	if !tr.IsPathLeased(manifest) || !tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: dvrHash}) {
		t.Fatal("upgraded lease must pin the new manifest + asset key")
	}
	if tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: ""}) {
		t.Fatal("stale empty-hash DVR asset entry must be removed")
	}
	if got := seg.count(dvrHash, "seg-1.ts"); got != 1 {
		t.Fatalf("seg-1 refcount = %d, want 1", got)
	}

	// Re-reconcile with the SAME data: unchanged, refcounts do not double.
	if got := tr.ReconcileResolvedSource(stream, []string{manifest}, AssetKey{Type: "dvr", Hash: dvrHash}, []string{"seg-1.ts", "seg-2.ts"}, SourceEntry{StreamName: stream}); got != SourceUnchanged {
		t.Fatalf("re-reconcile same data outcome = %v, want SourceUnchanged", got)
	}
	if got := seg.count(dvrHash, "seg-1.ts"); got != 1 {
		t.Fatalf("seg-1 refcount after idempotent reconcile = %d, want 1", got)
	}

	// Reconcile the resolved lease with a DIFFERENT (stale-scan) resolution:
	// it MUST be preserved — the old path/asset stay pinned, the new is NOT
	// installed. This is the STREAM_SOURCE-authority invariant.
	const (
		dvrHashStale  = "hash-STALE"
		manifestStale = "/data/dvr/s/hash-STALE/hash-STALE.m3u8"
	)
	if got := tr.ReconcileResolvedSource(stream, []string{manifestStale}, AssetKey{Type: "dvr", Hash: dvrHashStale}, []string{"seg-9.ts"}, SourceEntry{StreamName: stream}); got != SourceUnchanged {
		t.Fatalf("reconcile differing resolved outcome = %v, want SourceUnchanged (preserve authoritative)", got)
	}
	if !tr.IsPathLeased(manifest) || !tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: dvrHash}) {
		t.Fatal("authoritative resolution must remain pinned after a differing scan")
	}
	if tr.IsPathLeased(manifestStale) || tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: dvrHashStale}) {
		t.Fatal("stale scan resolution must NOT be installed over the authoritative lease")
	}
	if got := seg.count(dvrHashStale, "seg-9.ts"); got != 0 {
		t.Fatalf("stale seg-9 refcount = %d, want 0 (not installed)", got)
	}

	// A missing lease is created.
	if got := tr.ReconcileResolvedSource("dvr+other", []string{"/data/dvr/o/h/h.m3u8"}, AssetKey{Type: "dvr", Hash: "h"}, nil, SourceEntry{StreamName: "dvr+other"}); got != SourceCreated {
		t.Fatalf("missing lease outcome = %v, want SourceCreated", got)
	}

	// Release undoes the current resolution cleanly (no leak).
	tr.ReleaseSource(stream)
	if got := seg.count(dvrHash, "seg-1.ts"); got != 0 {
		t.Fatalf("seg-1 refcount after release = %d, want 0", got)
	}
	if tr.IsPathLeased(manifest) {
		t.Fatal("manifest must be unpinned after release")
	}
}

// The attached registry is kept consistent with the lease under one lock:
// reconcile records on create/upgrade, ReleaseSource forgets, and a preserved
// (unchanged) reconcile does not overwrite the authoritative registry entry.
func TestReconcileResolvedSource_RegistryStaysConsistent(t *testing.T) {
	tr := NewTracker(newFakeSegmentIndex(), NewHeatTracker())
	reg := NewSourceRegistry()
	tr.AttachSourceRegistry(reg)

	const stream = "vod+abc"
	// Authoritative STREAM_SOURCE lease + registry entry.
	tr.AcquireResolvedSource(stream, []string{"/data/vod/abc.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, SourceEntry{
		StreamName: stream, LocalPath: "/data/vod/abc.mp4", AssetType: "vod", InternalName: "abc",
	})
	if e, ok := reg.Lookup(stream); !ok || e.LocalPath != "/data/vod/abc.mp4" {
		t.Fatalf("registry after authoritative acquire = %+v ok=%v", e, ok)
	}

	// A stale-scan reconcile must not change the lease OR the registry entry.
	if got := tr.ReconcileResolvedSource(stream, []string{"/stale/path.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, SourceEntry{
		StreamName: stream, LocalPath: "/stale/path.mp4", AssetType: "vod", InternalName: "abc",
	}); got != SourceUnchanged {
		t.Fatalf("reconcile over authoritative outcome = %v, want SourceUnchanged", got)
	}
	if e, _ := reg.Lookup(stream); e.LocalPath != "/data/vod/abc.mp4" {
		t.Fatalf("registry must retain the authoritative path, got %q", e.LocalPath)
	}

	// STREAM_END: ReleaseSource drops lease AND registry entry atomically.
	tr.ReleaseSource(stream)
	if tr.HasSourceLease(stream) {
		t.Fatal("lease must be gone after release")
	}
	if _, ok := reg.Lookup(stream); ok {
		t.Fatal("registry entry must be forgotten atomically with the lease")
	}
}

// The authoritative STREAM_SOURCE writer must UPDATE the lease it owns, not just
// refresh timestamps: a degraded (pathless) lease upgrades to the real path/key
// with its degraded counter cleared, and a stale scanner-derived resolved lease
// is replaced so the file Mist actually reads is the one pinned.
func TestAcquireResolvedSource_ReplacesDegradedAndStale(t *testing.T) {
	tr := NewTracker(newFakeSegmentIndex(), NewHeatTracker())
	tr.AttachSourceRegistry(NewSourceRegistry())

	// (a) degraded → STREAM_SOURCE resolves it.
	const dstream = "dvr+resolve-me"
	dkey := AssetKey{Type: "dvr", Hash: "resolve-me"}
	tr.EnsureDegradedSource(dstream, dkey)
	if !tr.IsSourceDegraded(dstream) || !tr.DegradedDvrCleanupActive() {
		t.Fatal("precondition: stream must start degraded with the DVR pause raised")
	}
	realPath := "/data/dvr/resolve-me/resolve-me.m3u8"
	tr.AcquireResolvedSource(dstream, []string{realPath}, dkey, []string{"seg-1.ts"}, SourceEntry{
		StreamName: dstream, LocalPath: realPath, AssetType: "dvr", InternalName: "resolve-me",
	})
	if tr.IsSourceDegraded(dstream) {
		t.Fatal("authoritative STREAM_SOURCE must clear the degraded posture")
	}
	if tr.DegradedDvrCleanupActive() {
		t.Fatal("degraded DVR counter must be decremented after resolution")
	}
	if !tr.IsPathLeased(realPath) || !tr.IsAssetLeased(dkey) {
		t.Fatal("resolved lease must pin the real path and asset")
	}

	// (b) stale-resolved path A → STREAM_SOURCE supplies real path B.
	const vstream = "vod+abc"
	vkey := AssetKey{Type: "vod", Hash: "abc"}
	tr.AcquireResolvedSource(vstream, []string{"/stale/A.mp4"}, vkey, nil, SourceEntry{StreamName: vstream, LocalPath: "/stale/A.mp4", AssetType: "vod", InternalName: "abc"})
	tr.AcquireResolvedSource(vstream, []string{"/real/B.mp4"}, vkey, nil, SourceEntry{StreamName: vstream, LocalPath: "/real/B.mp4", AssetType: "vod", InternalName: "abc"})
	if tr.IsPathLeased("/stale/A.mp4") {
		t.Fatal("stale path A must be unpinned when STREAM_SOURCE supplies B")
	}
	if !tr.IsPathLeased("/real/B.mp4") {
		t.Fatal("real path B (what Mist reads) must be pinned")
	}
	if e, ok := tr.LookupSource(vstream); !ok || e.LocalPath != "/real/B.mp4" {
		t.Fatalf("registry must hold authoritative path B, got %+v ok=%v", e, ok)
	}
}

// Absence reconciliation must forget the source-registry entry ATOMICALLY with
// the lease removal (under the tracker lock) — not leave it for a separate poller
// forget that could race a concurrent STREAM_SOURCE. Deterministic: after the
// absence-dwell release, the registry entry must be gone too.
func TestReconcileSources_ForgetsRegistryAtomically(t *testing.T) {
	tr := NewTracker(newFakeSegmentIndex(), NewHeatTracker())
	tr.AttachSourceRegistry(NewSourceRegistry())

	const stream = "vod+gone"
	key := AssetKey{Type: "vod", Hash: "gone"}
	tr.ReconcileResolvedSource(stream, []string{"/data/vod/gone.mp4"}, key, nil, SourceEntry{
		StreamName: stream, LocalPath: "/data/vod/gone.mp4", AssetType: "vod", InternalName: "gone",
	})
	if _, ok := tr.LookupSource(stream); !ok {
		t.Fatal("precondition: registry entry must exist after reconcile")
	}

	absent := map[string]struct{}{}
	firstMissing := time.Now()
	tr.ReconcileSourcesAt(absent, firstMissing)
	tr.ReconcileSourcesAt(absent, firstMissing.Add(sourceMissingDwell))

	if tr.HasSourceLease(stream) {
		t.Fatal("lease must be released after two absent polls")
	}
	if _, ok := tr.LookupSource(stream); ok {
		t.Fatal("registry entry must be forgotten atomically with the lease release")
	}
}

// A STREAM_END racing the poller's reconcile must never leave the lease and the
// registry disagreeing: every mutation sets or clears BOTH under the tracker
// lock. An observer reading both through the tracker's single boundary must never
// see them disagree, and the final state must be consistent too. Run under -race,
// and exercise ReconcileSources' atomic release+forget as well.
func TestReconcileVsStreamEnd_LeaseAndRegistryNeverTear(t *testing.T) {
	tr := NewTracker(newFakeSegmentIndex(), NewHeatTracker())
	tr.AttachSourceRegistry(NewSourceRegistry())

	const stream = "dvr+racer"
	key := AssetKey{Type: "dvr", Hash: "racer"}
	entry := SourceEntry{StreamName: stream, LocalPath: "/data/dvr/racer/racer.m3u8", AssetType: "dvr", InternalName: "racer"}
	absent := map[string]struct{}{}

	const rounds = 500
	var wg sync.WaitGroup
	var observerFail int32
	wg.Add(4)
	// Reconciler: repeatedly (re)installs the resolved lease + registry entry.
	go func() {
		defer wg.Done()
		for range rounds {
			tr.ReconcileResolvedSource(stream, []string{entry.LocalPath}, key, []string{"seg-1.ts"}, entry)
		}
	}()
	// STREAM_END: forgets both atomically.
	go func() {
		defer wg.Done()
		for range rounds {
			tr.ReleaseSource(stream)
		}
	}()
	// Absence reconciliation: releases the lease AND forgets the registry under
	// one lock, so a concurrent STREAM_SOURCE cannot reinstall between the two.
	go func() {
		defer wg.Done()
		for range rounds {
			firstMissing := time.Now()
			tr.ReconcileSourcesAt(absent, firstMissing)
			tr.ReconcileSourcesAt(absent, firstMissing.Add(sourceMissingDwell))
		}
	}()
	// Observer: reads lease presence and registry presence under ONE lock; they
	// must never disagree mid-flight.
	go func() {
		defer wg.Done()
		for range rounds {
			if lease, reg := tr.sourcePresence(stream); lease != reg {
				atomic.StoreInt32(&observerFail, 1)
			}
		}
	}()
	wg.Wait()

	if atomic.LoadInt32(&observerFail) != 0 {
		t.Fatal("observer saw lease and registry disagree under one lock (torn state)")
	}
	hasLease := tr.HasSourceLease(stream)
	_, hasReg := tr.LookupSource(stream)
	if hasLease != hasReg {
		t.Fatalf("final lease/registry torn: hasLease=%v hasReg=%v", hasLease, hasReg)
	}
}

func TestViewerLease_IdempotentRefireDoesNotDoubleBumpHeatOrViews(t *testing.T) {
	seg := newFakeSegmentIndex()
	heat := NewHeatTracker()
	tr := NewTracker(seg, heat)

	// Establish a source lease so DVR ActiveViews would be visible if
	// viewer churn touched them.
	key := AssetKey{Type: "dvr", Hash: "dvr1"}
	tr.acquireSourceForTest("dvr+rolling1", []string{"/dvr/rolling1.m3u8"}, key, []string{"seg-1.ts"}, false)
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

	tr.acquireSourceForTest("vod+abc", []string{path}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)
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
	tr.acquireSourceForTest("dvr+rolling1", []string{"/m1"}, AssetKey{Type: "dvr", Hash: "dvr1"}, nil, false)

	if !tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: "dvr1"}) {
		t.Fatalf("expected dvr1 to be asset-leased")
	}
	if tr.IsAssetLeased(AssetKey{Type: "dvr", Hash: "dvr2"}) {
		t.Fatalf("did not expect dvr2 to be asset-leased")
	}
}

func TestReconcileSources_ElapsedDwellReleasesAbsent(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.acquireSourceForTest("vod+a", []string{"/a"}, AssetKey{Type: "vod", Hash: "a"}, nil, false)
	tr.acquireSourceForTest("vod+b", []string{"/b"}, AssetKey{Type: "vod", Hash: "b"}, nil, false)

	firstMissing := time.Now()
	tr.ReconcileSourcesAt(map[string]struct{}{"vod+a": {}}, firstMissing)
	if tr.SourceCount() != 2 {
		t.Fatalf("expected no release at the first absence, got SourceCount=%d", tr.SourceCount())
	}

	if released := tr.ReconcileSourcesAt(map[string]struct{}{"vod+a": {}}, firstMissing.Add(sourceMissingDwell-time.Millisecond)); len(released) != 0 {
		t.Fatalf("released before the source-missing dwell elapsed: %v", released)
	}
	released := tr.ReconcileSourcesAt(map[string]struct{}{"vod+a": {}}, firstMissing.Add(sourceMissingDwell))
	if len(released) != 1 || released[0] != "vod+b" {
		t.Fatalf("expected release of vod+b after the absence dwell, got %v", released)
	}
	if tr.IsPathLeased("/b") {
		t.Fatalf("/b should be unleased after reconciliation drop")
	}
}

func TestReconcileSources_PresentResetsStrikes(t *testing.T) {
	tr := NewTracker(nil, NewHeatTracker())
	tr.acquireSourceForTest("vod+a", []string{"/a"}, AssetKey{Type: "vod", Hash: "a"}, nil, false)

	firstMissing := time.Now()
	tr.ReconcileSourcesAt(map[string]struct{}{}, firstMissing)
	tr.ReconcileSourcesAt(map[string]struct{}{"vod+a": {}}, firstMissing.Add(sourceMissingDwell))
	released := tr.ReconcileSourcesAt(map[string]struct{}{}, firstMissing.Add(2*sourceMissingDwell))
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
	tr.acquireSourceForTest("dvr+x", []string{"/x"}, AssetKey{Type: "dvr", Hash: "h"}, nil, true)
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
	tr.acquireSourceForTest("vod+abc", []string{"/data/vod/abc.mp4"}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)
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
	tr.acquireSourceForTest("vod+x", []string{"/x.mp4"}, AssetKey{Type: "vod", Hash: "h"}, nil, true)
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
	tr.acquireSourceForTest("vod+abc", []string{path}, AssetKey{Type: "vod", Hash: "abc"}, nil, false)

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
	tr.acquireSourceForTest("dvr+rolling", []string{filepath.Join(dvrDir, "h.m3u8")}, AssetKey{Type: "dvr", Hash: "h"}, nil, false)

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
