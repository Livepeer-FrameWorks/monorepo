package handlers

import (
	"errors"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/leases"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// installTestTracker wires a fresh process-global lease tracker for a test and
// restores the previous singletons afterward. Segment index is nil (a resolved
// DVR source lease is nil-safe without a segment list).
func installTestTracker(t *testing.T) *leases.Tracker {
	t.Helper()
	prevTracker := leases.GlobalTracker()
	prevHeat := leases.GlobalHeat()
	prevDeferred := leases.GlobalDeferredStore()

	heat := leases.NewHeatTracker()
	tracker := leases.NewTracker(nil, heat)
	leases.Install(tracker, leases.NewSourceRegistry(), heat, nil)
	// The tracker owns its registry, so restoring the previous tracker with a
	// fresh registry is sufficient for isolation (prevTracker is nil in these
	// unit tests).
	t.Cleanup(func() { leases.Install(prevTracker, leases.NewSourceRegistry(), prevHeat, prevDeferred) })
	return tracker
}

// A resolved dvr+<internal_name> during boot recovery must install a NORMAL
// (non-degraded) DVR source lease whose path and asset hash are populated from
// the recovered DVR job — NOT a degraded lease.
func TestRebuildSourceLeasesFromMist_DVRResolvedInstallsRealLease(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	const (
		streamName = "dvr+stream-int"
		dvrHash    = "hash-XYZ"
		manifest   = "/storage/dvr/s/hash-XYZ/hash-XYZ.m3u8"
	)
	resolveDVR := func(internalName string) (string, string, bool) {
		if internalName == "stream-int" {
			return dvrHash, manifest, true
		}
		return "", "", false
	}

	rebuildSourceLeasesFromMist(tracker, map[string]struct{}{streamName: {}}, resolveDVR)

	if tracker.DegradedDvrCleanupActive() {
		t.Fatal("resolved DVR lease must NOT be degraded")
	}
	if !tracker.IsPathLeased(manifest) {
		t.Fatalf("expected manifest path %q to be pinned by the lease", manifest)
	}
	if !tracker.IsAssetLeased(leases.AssetKey{Type: "dvr", Hash: dvrHash}) {
		t.Fatalf("expected asset key {dvr,%s} to be leased", dvrHash)
	}
	if entry, ok := tracker.LookupSource(streamName); !ok || entry.DvrHash != dvrHash || entry.LocalPath != manifest {
		t.Fatalf("source registry entry = %+v (ok=%v), want DvrHash=%s LocalPath=%s", entry, ok, dvrHash, manifest)
	}
}

// An UNRESOLVED dvr+<internal_name> (no recovered DVR job yet) must install a
// degraded DVR lease so DegradedDvrCleanupActive() is true and DVR destructive
// cleanup fails closed until the job resolves.
func TestRebuildSourceLeasesFromMist_DVRUnresolvedRaisesDegradedPause(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	unresolved := func(string) (string, string, bool) { return "", "", false }

	if tracker.DegradedDvrCleanupActive() {
		t.Fatal("precondition: degraded DVR cleanup must be inactive before rebuild")
	}

	rebuildSourceLeasesFromMist(tracker, map[string]struct{}{"dvr+orphan-int": {}}, unresolved)

	if !tracker.DegradedDvrCleanupActive() {
		t.Fatal("unresolved DVR stream must raise the degraded DVR pause (DegradedDvrCleanupActive)")
	}
	// Fail-closed: a DVR directory delete must now be refused globally.
	if err := tracker.DeleteDVRDirIfUnleased(t.TempDir(), "some-hash"); !errors.Is(err, leases.ErrLeaseHeld) {
		t.Fatalf("DeleteDVRDirIfUnleased err = %v, want ErrLeaseHeld while degraded", err)
	}
}

// When the degraded (unresolved) DVR stream later clears from Mist's active set,
// reconciliation must release its lease and lift the degraded pause so cleanup
// resumes.
func TestRebuildSourceLeasesFromMist_DVRDegradedPauseLiftsOnRelease(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	unresolved := func(string) (string, string, bool) { return "", "", false }

	const streamName = "dvr+orphan-int"
	rebuildSourceLeasesFromMist(tracker, map[string]struct{}{streamName: {}}, unresolved)
	if !tracker.DegradedDvrCleanupActive() {
		t.Fatal("expected degraded pause active after installing the degraded lease")
	}

	// Stream vanished from Mist: the complete absence dwell releases the lease
	// and decrements degradedDvrCount.
	empty := map[string]struct{}{}
	firstMissing := time.Now()
	tracker.ReconcileSourcesAt(empty, firstMissing)
	tracker.ReconcileSourcesAt(empty, firstMissing.Add(10*time.Second))

	if tracker.DegradedDvrCleanupActive() {
		t.Fatal("degraded pause must lift once the unresolved DVR lease is released")
	}
	// Fail-closed cleanup is the production-observable signal; once the pause
	// lifts, an unrelated DVR dir delete succeeds again.
	if err := tracker.DeleteDVRDirIfUnleased(t.TempDir(), "unrelated"); err != nil {
		t.Fatalf("DVR dir delete must succeed after the degraded lease is released, got %v", err)
	}
}
