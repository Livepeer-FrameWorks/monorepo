package artifacts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEnvDiff_HasDifferences(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		d    EnvDiff
		want bool
	}{
		{"empty", EnvDiff{}, false},
		{"added", EnvDiff{Added: []string{"FOO"}}, true},
		{"removed", EnvDiff{Removed: []string{"BAR"}}, true},
		{"changed", EnvDiff{Changed: []string{"BAZ"}}, true},
		{"mixed", EnvDiff{Added: []string{"A"}, Removed: []string{"B"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.d.HasDifferences(); got != c.want {
				t.Errorf("HasDifferences: want %v, got %v", c.want, got)
			}
		})
	}
}

func TestConfigDiff_DivergencesCountsNonMatch(t *testing.T) {
	t.Parallel()
	d := ConfigDiff{
		Entries: []ConfigDiffEntry{
			{Status: StatusMatch},
			{Status: StatusMatch},
			{Status: StatusDiffer},
			{Status: StatusMissingOnHost},
			{Status: StatusProbeError},
		},
	}
	if got := d.Divergences(); got != 3 {
		t.Errorf("want 3, got %d", got)
	}
}

func TestEnvDiff_JSONOutputHasNoValueFields(t *testing.T) {
	t.Parallel()
	// Structural guarantee: EnvDiff JSON carries no value-bearing fields.
	d := EnvDiff{
		Added:   []string{"SECRET_A"},
		Removed: []string{"SECRET_B"},
		Changed: []string{"SECRET_C"},
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, banned := range []string{"value", "Value", "old", "Old", "new", "New"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("EnvDiff JSON contains banned value-ish field %q: %s", banned, raw)
		}
	}
}

func TestConfigDiffEntry_StructuralGuaranteesOnReflectedFields(t *testing.T) {
	t.Parallel()
	// EnvDiff has exactly three slice fields: Added, Removed, Changed.
	tt := reflect.TypeFor[EnvDiff]()
	wantFields := map[string]bool{"Added": true, "Removed": true, "Changed": true}
	if tt.NumField() != len(wantFields) {
		t.Fatalf("EnvDiff has %d fields, want %d — did a value field slip in?", tt.NumField(), len(wantFields))
	}
	for field := range tt.Fields() {
		if !wantFields[field.Name] {
			t.Errorf("EnvDiff has unexpected field %q", field.Name)
		}
	}
}
