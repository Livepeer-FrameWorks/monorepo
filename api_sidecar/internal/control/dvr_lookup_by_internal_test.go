package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// LookupActiveDVRByInternalName is the race-safe snapshot the poller uses to
// map a rolling-DVR playback token (dvr+<internal_name>) back to its dvr_hash
// and rolling manifest path during boot recovery. The jobs map is keyed by
// dvr_hash, so the lookup must scan by InternalName and return a value copy.
func TestLookupActiveDVRByInternalName(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs: map[string]*DVRJob{
			"hash-A": {DVRHash: "hash-A", InternalName: "stream-int-A", ManifestPath: "/storage/dvr/s/hash-A/hash-A.m3u8"},
			"hash-B": {DVRHash: "hash-B", InternalName: "stream-int-B", ManifestPath: "/storage/dvr/s/hash-B/hash-B.m3u8"},
		},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	hash, manifest, ok := LookupActiveDVRByInternalName("stream-int-B")
	if !ok {
		t.Fatal("expected resolve for known internal name")
	}
	if hash != "hash-B" {
		t.Fatalf("dvr_hash = %q, want hash-B", hash)
	}
	if manifest != "/storage/dvr/s/hash-B/hash-B.m3u8" {
		t.Fatalf("manifest = %q, want the job's ManifestPath", manifest)
	}

	if _, _, ok := LookupActiveDVRByInternalName("no-such-stream"); ok {
		t.Fatal("expected no resolve for unknown internal name")
	}
	if _, _, ok := LookupActiveDVRByInternalName(""); ok {
		t.Fatal("expected no resolve for empty internal name")
	}
}

// Fresh-per-session recording can leave two active recordings sharing an
// internal_name (an old and a new session, possibly on the same node). With no stable
// identity to pick the right one, the lookup must FAIL CLOSED (ok=false) rather than
// return an arbitrary map-iteration match — the poller then installs a degraded DVR
// lease and destructive cleanup pauses for BOTH, instead of pinning one manifest and
// leaving the other's files exposed.
func TestLookupActiveDVRByInternalName_AmbiguousFailsClosed(t *testing.T) {
	dm := &DVRManager{
		logger: logging.NewLogger(),
		jobs: map[string]*DVRJob{
			"hash-old": {DVRHash: "hash-old", InternalName: "stream-dup", ManifestPath: "/storage/dvr/s/hash-old/hash-old.m3u8"},
			"hash-new": {DVRHash: "hash-new", InternalName: "stream-dup", ManifestPath: "/storage/dvr/s/hash-new/hash-new.m3u8"},
		},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	if hash, _, ok := LookupActiveDVRByInternalName("stream-dup"); ok {
		t.Fatalf("two recordings share an internal_name; lookup must fail closed, got hash=%q ok=true", hash)
	}
}

// A nil manager (DVR subsystem never initialized) must resolve nothing rather
// than panic.
func TestLookupActiveDVRByInternalNameNilManager(t *testing.T) {
	dvrManagerOnce.Do(func() {})
	prev := dvrManager
	dvrManager = nil
	t.Cleanup(func() { dvrManager = prev })

	if _, _, ok := LookupActiveDVRByInternalName("anything"); ok {
		t.Fatal("expected no resolve when manager is nil")
	}
}
