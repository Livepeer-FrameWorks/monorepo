package control

import (
	"testing"
	"time"
)

// resetRelayGrants isolates the package-global grant store for a test: empty
// in-memory map, no Redis (so the in-mem fallback path is exercised).
func resetRelayGrants(t *testing.T) {
	t.Helper()
	relayGrants.mu.Lock()
	prevMem, prevRedis := relayGrants.mem, relayGrants.redis
	relayGrants.mem = make(map[string]relayGrantMemEntry)
	relayGrants.redis = nil
	relayGrants.mu.Unlock()
	t.Cleanup(func() {
		relayGrants.mu.Lock()
		relayGrants.mem, relayGrants.redis = prevMem, prevRedis
		relayGrants.mu.Unlock()
	})
}

func expireGrant(t *testing.T, id string) {
	t.Helper()
	relayGrants.mu.Lock()
	defer relayGrants.mu.Unlock()
	e, ok := relayGrants.mem[id]
	if !ok {
		t.Fatalf("grant %s not in memory", id)
	}
	e.exp = time.Now().Add(-time.Hour)
	relayGrants.mem[id] = e
}

// MintRelayGrant returns an opaque hex id and stores the grant in memory; a
// subsequent lookup returns the exact authorized artifact/node/paths. The grant
// is the capability — losing its fields would broaden or break relay auth.
func TestMintAndLookupRelayGrant(t *testing.T) {
	resetRelayGrants(t)

	id, err := MintRelayGrant("art-1", "node-1", []string{"/internal/artifact/vod/art-1.mp4"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(id) != 32 {
		t.Fatalf("grant id %q len = %d, want 32 hex chars", id, len(id))
	}
	g, ok := lookupRelayGrant(id)
	if !ok || g.ArtifactHash != "art-1" || g.OriginNodeID != "node-1" || len(g.AllowedPaths) != 1 {
		t.Fatalf("lookup = %+v ok=%v", g, ok)
	}
}

// lookupRelayGrant reports not-found for an unknown id, and treats an expired
// grant as absent (deleting it) — the TTL is the only revocation.
func TestLookupRelayGrant_MissingAndExpired(t *testing.T) {
	resetRelayGrants(t)

	if _, ok := lookupRelayGrant("nope"); ok {
		t.Fatal("unknown id must not resolve")
	}

	id, _ := MintRelayGrant("art-1", "node-1", nil)
	expireGrant(t, id)
	if _, ok := lookupRelayGrant(id); ok {
		t.Fatal("expired grant must not resolve")
	}
	// Lookup of an expired grant also removes it from the store.
	relayGrants.mu.Lock()
	_, stillThere := relayGrants.mem[id]
	relayGrants.mu.Unlock()
	if stillThere {
		t.Fatal("expired grant should be deleted on lookup")
	}
}

// sweepRelayGrants drops expired in-memory entries while leaving live ones.
func TestSweepRelayGrants(t *testing.T) {
	resetRelayGrants(t)

	live, _ := MintRelayGrant("art-live", "node-1", nil)
	dead, _ := MintRelayGrant("art-dead", "node-2", nil)
	expireGrant(t, dead)

	sweepRelayGrants()

	if _, ok := lookupRelayGrant(live); !ok {
		t.Fatal("live grant must survive the sweep")
	}
	relayGrants.mu.Lock()
	_, deadThere := relayGrants.mem[dead]
	relayGrants.mu.Unlock()
	if deadThere {
		t.Fatal("expired grant must be swept")
	}
}
