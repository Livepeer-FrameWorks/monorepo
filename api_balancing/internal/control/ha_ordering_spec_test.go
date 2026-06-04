package control

import (
	"testing"
	"time"
)

// Specs for the stream-registry ordering invariant: ordering is by a logical
// version, so a causally-older peer change never wins over fresher local state
// regardless of wall-clock skew.
const haOrderingRFC = "requires the state-sync ordering mechanism (docs/rfcs/foghorn-ha-ordering.md)"

// A causally-older delete from a clock-ahead peer (larger PublishedAtUnixNano)
// must not wipe a fresher local source entry.
func TestApplyRedisChange_SkewedDelete_DoesNotWipeFresherSource(t *testing.T) {
	t.Skip(haOrderingRFC)

	r := newPopulatedRegistry(t)
	const internal = "60546679b497415db2338cd5cae54992"
	if res := r.AdmitAndReserve(internal, "node-A", nil); res.Decision != AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", res.Decision)
	}

	// Delete published by a fast-clocked peer: numerically newer, causally older.
	r.applyRedisChange(RegistryChange{
		Entity:              RegistryEntitySource,
		Operation:           RegistryOpDelete,
		Key:                 internal,
		PublishedAtUnixNano: time.Now().Add(time.Hour).UnixNano(),
	})

	r.mu.RLock()
	_, ok := r.byInt[internal]
	r.mu.RUnlock()
	if !ok {
		t.Fatal("skewed (fast-clock) delete wiped a fresher local source entry")
	}
}

// A valid artifact delete published before a restart must still apply
// afterward — the post-restart `cached` stamp must not mask it.
func TestApplyRedisChange_PostRestartDelete_StillApplies(t *testing.T) {
	t.Skip(haOrderingRFC)

	r := newPopulatedRegistry(t)
	const hash = "artifacthash1"

	// Simulate a rehydrated artifact: cached == restart time (now).
	r.artifacts.mu.Lock()
	r.artifacts.byHash[hash] = &cachedArtifact{entry: ArtifactEntry{ArtifactHash: hash}, cached: time.Now()}
	r.artifacts.mu.Unlock()

	// A genuine delete that was published before this process restarted.
	r.applyRedisChange(RegistryChange{
		Entity:              RegistryEntityArtifact,
		Operation:           RegistryOpDelete,
		Key:                 hash,
		PublishedAtUnixNano: time.Now().Add(-time.Minute).UnixNano(),
	})

	r.artifacts.mu.RLock()
	_, ok := r.artifacts.byHash[hash]
	r.artifacts.mu.RUnlock()
	if ok {
		t.Fatal("post-restart cached stamp dropped a valid pre-restart artifact delete")
	}
}
