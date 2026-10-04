package handlers

import (
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/leases"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A processing output booted under its vod+ name for header generation is not
// in the artifact scan yet; the poller leases the file its source override
// names instead of falling back to a degraded lease.
func TestRebuildSourceLeasesFromMist_VODSourceOverrideInstallsRealLease(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	const streamName = "vod+fresh-output-int"
	output := filepath.Join(t.TempDir(), "fresh.mkv")
	setProcessingSourceOverride(streamName, output)
	t.Cleanup(func() { clearProcessingSourceOverride(streamName) })

	rebuildSourceLeasesFromMist(tracker, map[string]struct{}{streamName: {}}, nil)

	if tracker.DegradedVodCleanupActive() {
		t.Fatal("a VOD whose source override names a local file must not get a degraded lease")
	}
	if !tracker.IsPathLeased(output) {
		t.Fatalf("expected the override file %q to be pinned by the lease", output)
	}
	if !tracker.IsAssetLeased(leases.AssetKey{Type: "vod", Hash: "fresh-output-int"}) {
		t.Fatal("expected the VOD asset to be leased")
	}
}

// Without a scan entry or a local override the VOD stays fail-closed.
func TestRebuildSourceLeasesFromMist_UnresolvedVODStaysDegraded(t *testing.T) {
	monitorLogger = logging.NewLogger()
	tracker := installTestTracker(t)

	rebuildSourceLeasesFromMist(tracker, map[string]struct{}{"vod+unknown-int": {}}, nil)

	if !tracker.DegradedVodCleanupActive() {
		t.Fatal("an unresolved VOD stream must raise the degraded VOD pause")
	}
}
