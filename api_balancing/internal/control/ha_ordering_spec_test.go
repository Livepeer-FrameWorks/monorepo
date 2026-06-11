package control

import (
	"testing"
	"time"
)

// Acceptance specs for the registry ordering invariant (see
// docs/architecture/foghorn-ha.md, "Ordering and replay semantics"):
// ordering comes from the changelog's server-assigned entry IDs gated by
// per-key watermarks, so a causally-older peer change never wins over
// fresher local state regardless of any instance's wall clock, and a valid
// pre-restart delete is honored because no wall-clock stamp can mask it.

// A causally-older delete from a clock-ahead peer must not wipe a
// fresher local source entry. Wall clocks are irrelevant: the peer's delete
// occupies an earlier changelog position than the local re-admission, so the
// watermark drops it no matter what the peer's clock said.
func TestApplyRedisChange_SkewedDelete_DoesNotWipeFresherSource(t *testing.T) {
	r := newPopulatedRegistry(t)
	const internal = "60546679b497415db2338cd5cae54992"
	if res := r.AdmitAndReserve(internal, "node-A", nil); res.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", res.Decision)
	}

	// The local admission's registry write landed at changelog position 5-0.
	r.watermarks.Record("source|"+internal, "5-0")

	// The peer's delete was logged BEFORE that (4-0) — causally older, even
	// if the peer's wall clock ran an hour fast when it published.
	r.handleRegistryChangelogEntry("4-0", RegistryChange{
		InstanceID: "peer",
		Entity:     RegistryEntitySource,
		Operation:  RegistryOpDelete,
		Key:        internal,
	})

	r.mu.RLock()
	_, ok := r.byInt[internal]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("causally-older delete wiped a fresher local source entry")
	}
}

// A valid artifact delete published before a restart must still apply
// afterward. The restart resets the in-memory watermark, and nothing about
// rehydration (the post-restart `cached` stamp) may mask a delete that is
// genuinely the newest change for the key.
func TestApplyRedisChange_PostRestartDelete_StillApplies(t *testing.T) {
	r := newPopulatedRegistry(t)
	const hash = "artifacthash1"

	// Simulate a rehydrated artifact: cached == restart time (now), no
	// watermark for the key (fresh process).
	r.artifacts.mu.Lock()
	r.artifacts.byHash[hash] = &cachedArtifact{entry: ArtifactEntry{ArtifactHash: hash}, cached: time.Now()}
	r.artifacts.mu.Unlock()

	// A genuine delete replayed from the changelog after restart.
	r.handleRegistryChangelogEntry("1-0", RegistryChange{
		InstanceID: "peer",
		Entity:     RegistryEntityArtifact,
		Operation:  RegistryOpDelete,
		Key:        hash,
	})

	r.artifacts.mu.RLock()
	_, ok := r.artifacts.byHash[hash]
	r.artifacts.mu.RUnlock()
	if ok {
		t.Fatal("post-restart rehydration masked a valid pre-restart artifact delete")
	}
}
