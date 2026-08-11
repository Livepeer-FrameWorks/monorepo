package gitops

import "testing"

// IsRollbackDisabled reads per-RELEASE metadata: a service listed in RollbackDisabled has automatic rollback turned
// off for THIS release (a readiness-contract cut whose previous binary cannot pass the current gate). A release that
// omits the entry keeps normal rollback — the mechanism is data-driven, not a permanent per-service code exception.
func TestManifest_IsRollbackDisabled(t *testing.T) {
	m := &Manifest{RollbackDisabled: []string{"chandler"}}
	if !m.IsRollbackDisabled("chandler") {
		t.Fatal("chandler must be reported rollback-disabled when listed")
	}
	for _, svc := range []string{"foghorn", "commodore", ""} {
		if m.IsRollbackDisabled(svc) {
			t.Fatalf("%q must NOT be rollback-disabled (not listed)", svc)
		}
	}

	// A release with no metadata disables nothing.
	empty := &Manifest{}
	if empty.IsRollbackDisabled("chandler") {
		t.Fatal("a release that omits rollback_disabled must keep normal rollback for every service")
	}
}
