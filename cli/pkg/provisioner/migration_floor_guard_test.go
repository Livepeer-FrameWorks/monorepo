package provisioner

import "testing"

// TestValidateBaselineFloor pins that a persisted `_schema_baseline` marker is trusted only if it is a canonical
// version: a corrupt marker must be refused (not silently normalized by CompareSemver into a fold-everything floor),
// while "" (no marker — an existing pre-baseline cluster) is allowed.
func TestValidateBaselineFloor(t *testing.T) {
	valid := []string{"", "v0.2.96", "v1.0.0"}
	for _, f := range valid {
		if err := validateBaselineFloor("periscope", f); err != nil {
			t.Errorf("valid marker %q was refused: %v", f, err)
		}
	}
	corrupt := []string{"garbage", "999999999999999999999999", "0.2.96", "v0.2", "v01.2.3", "vNaN.0.0"}
	for _, f := range corrupt {
		if err := validateBaselineFloor("periscope", f); err == nil {
			t.Errorf("corrupt marker %q must be refused, but passed", f)
		}
	}
}
