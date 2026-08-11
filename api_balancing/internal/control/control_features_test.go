package control

import "testing"

// ControlFeaturesForProtocol is the single source of a sidecar session's protocol-gated capabilities. Locking the
// thresholds here keeps the extension seam explicit: staged freeze at v1, staged thumbnail + authoritative inventory
// at v2. A version below a floor yields false — but registration rejects any sidecar below MinControlProtocolVersion,
// so no admitted session is ever below the floors (see the invariant test below).
func TestControlFeaturesForProtocol_Thresholds(t *testing.T) {
	cases := []struct {
		v                                int32
		freeze, thumbnail, authInventory bool
	}{
		{-1, false, false, false}, // never negative in practice, but must not misclassify
		{0, false, false, false},  // pre-staged sidecar (Register.control_protocol_version absent)
		{1, true, false, false},   // staged freeze only
		{2, true, true, true},     // + staged thumbnail + versioned inventory
		{3, true, true, true},     // a future version keeps every earlier capability
	}
	for _, c := range cases {
		got := ControlFeaturesForProtocol(c.v)
		if got.StagedFreeze != c.freeze || got.StagedThumbnail != c.thumbnail || got.AuthoritativeInventory != c.authInventory {
			t.Fatalf("v=%d: got %+v, want freeze=%v thumbnail=%v authInventory=%v",
				c.v, got, c.freeze, c.thumbnail, c.authInventory)
		}
	}
}

// Registration rejects anything below MinControlProtocolVersion, so every ADMITTED session is inventory-authoritative
// (and staged-capable). This locks that invariant: if the minimum is ever lowered below the authoritative-inventory
// floor, an admitted session could send unversioned inventory with no handler for it, so this fails loudly to signal
// that a supported protocol RANGE would first be required.
func TestMinControlProtocolVersion_AdmittedSessionsAreAuthoritative(t *testing.T) {
	if MinControlProtocolVersion < AuthoritativeInventoryProtocolMin {
		t.Fatalf("MinControlProtocolVersion (%d) below AuthoritativeInventoryProtocolMin (%d): an admitted session could send unversioned inventory with no legacy path",
			MinControlProtocolVersion, AuthoritativeInventoryProtocolMin)
	}
	if f := ControlFeaturesForProtocol(MinControlProtocolVersion); !f.AuthoritativeInventory || !f.StagedFreeze || !f.StagedThumbnail {
		t.Fatalf("a session at the minimum protocol must hold every capability, got %+v", f)
	}
}
