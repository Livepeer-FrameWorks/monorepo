package federation

import "testing"

// PeerExcludesTenant is deliberately one-sided. The receive-side stream
// lifecycle guard denies a publisher's ingest when it fires, and we operate the
// Foghorns ourselves, so a gap in our own view of a peer must never become a
// refusal. Only a populated tenant scope that omits the tenant is evidence.
func TestPeerExcludesTenantAnswersOnlyFromPositiveKnowledge(t *testing.T) {
	pm := &PeerManager{peers: map[string]*peerState{
		"scoped":   {tenantIDs: []string{"tenant-a"}, tenantSet: map[string]struct{}{"tenant-a": {}}},
		"unscoped": {},
	}}

	if !pm.PeerExcludesTenant("scoped", "tenant-b") {
		t.Error("a populated scope that omits the tenant is positive exclusion")
	}
	if pm.PeerExcludesTenant("scoped", "tenant-a") {
		t.Error("a tenant inside the peer's scope must not be excluded")
	}
	for _, unknown := range []struct{ cluster, tenant string }{
		{"unscoped", "tenant-a"}, // peer known, scope not populated
		{"never-seen", "tenant-a"},
		{"", "tenant-a"},
		{"scoped", ""},
	} {
		if pm.PeerExcludesTenant(unknown.cluster, unknown.tenant) {
			t.Errorf("not knowing a peer's scope was turned into a refusal: %+v", unknown)
		}
	}
	var nilManager *PeerManager
	if nilManager.PeerExcludesTenant("scoped", "tenant-b") {
		t.Error("a nil manager must not exclude anything")
	}
}
