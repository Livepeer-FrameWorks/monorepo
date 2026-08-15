package handlers

import (
	"errors"
	"testing"

	"frameworks/api_sidecar/internal/leases"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A degraded DVR lease installed by an early poll (resolution not yet ready)
// upgrades in place to a resolved lease once a later poll can resolve the SAME
// still-present stream, so DegradedDvrCleanupActive() (the GLOBAL DVR cleanup
// pause) lifts while the stream remains present rather than only when it leaves.
func TestRebuildSourceLeasesFromMist_DegradedDVRUpgradesWhileStreamPresent(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	const (
		streamName = "dvr+int-x"
		dvrHash    = "hash-ABC"
		manifest   = "/storage/dvr/s/hash-ABC/hash-ABC.m3u8"
	)
	present := map[string]struct{}{streamName: {}}

	// Simulate resolution becoming available between two polls of the SAME
	// present stream (the sibling behavioral tests drive the resolver the same
	// way; the real recovery path is covered without the seam in the control
	// package's TestStartRecordingPersistsInternalNameThenRecoveryResolves).
	resolved := false
	resolveDVR := func(internalName string) (string, string, bool) {
		if resolved && internalName == "int-x" {
			return dvrHash, manifest, true
		}
		return "", "", false
	}

	// First poll: unresolved → degraded, type-only DVR lease raises the pause.
	rebuildSourceLeasesFromMist(tracker, present, resolveDVR)
	if !tracker.DegradedDvrCleanupActive() {
		t.Fatal("first poll (unresolved) must raise the degraded DVR pause")
	}

	// Second poll: resolution now available for the SAME present stream.
	resolved = true
	rebuildSourceLeasesFromMist(tracker, present, resolveDVR)

	// Lease upgraded in place: real path + hash, pause lifted.
	if tracker.DegradedDvrCleanupActive() {
		t.Fatal("degraded DVR pause must lift once the lease is upgraded (degradedDvrCount decremented)")
	}
	if !tracker.IsPathLeased(manifest) {
		t.Fatalf("upgraded lease must pin the rolling manifest %q", manifest)
	}
	if !tracker.IsAssetLeased(leases.AssetKey{Type: "dvr", Hash: dvrHash}) {
		t.Fatalf("upgraded lease must pin asset {dvr,%s}", dvrHash)
	}
	if entry, ok := tracker.LookupSource(streamName); !ok || entry.DvrHash != dvrHash || entry.LocalPath != manifest {
		t.Fatalf("source registry entry = %+v (ok=%v), want DvrHash=%s LocalPath=%s", entry, ok, dvrHash, manifest)
	}

	// Index/counter consistency: the global pause is gone, so an UNRELATED DVR
	// dir delete now succeeds, while a delete of the RESOLVED hash is refused
	// by its asset-keyed lease (not by the lifted global degraded pause).
	if err := tracker.DeleteDVRDirIfUnleased(t.TempDir(), "unrelated-hash"); err != nil {
		t.Fatalf("unrelated DVR dir delete must succeed after the pause lifts, got %v", err)
	}
	if err := tracker.DeleteDVRDirIfUnleased(t.TempDir(), dvrHash); !errors.Is(err, leases.ErrLeaseHeld) {
		t.Fatalf("resolved-hash DVR dir delete err = %v, want ErrLeaseHeld (asset pinned)", err)
	}
}
