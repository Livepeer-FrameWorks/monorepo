package cmd

import (
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestCopyMetadata_Isolation(t *testing.T) {
	src := map[string]any{"a": 1, "b": "two"}
	cp := copyMetadata(src)

	if len(cp) != len(src) {
		t.Fatalf("len(copy) = %d, want %d", len(cp), len(src))
	}
	// Mutating the copy must not touch the source — that is the only reason
	// this function exists.
	cp["a"] = 99
	cp["c"] = "new"
	if src["a"] != 1 {
		t.Errorf("source mutated: src[a] = %v, want 1", src["a"])
	}
	if _, ok := src["c"]; ok {
		t.Error("source gained key c from copy mutation")
	}

	// nil input yields an empty (non-nil) map.
	if got := copyMetadata(nil); got == nil || len(got) != 0 {
		t.Errorf("copyMetadata(nil) = %v, want empty non-nil map", got)
	}
}

func TestPostgresDatabaseNames(t *testing.T) {
	if got := postgresDatabaseNames(&inventory.Manifest{}); got != nil {
		t.Errorf("no postgres config: got %v, want nil", got)
	}

	disabled := &inventory.Manifest{Infrastructure: inventory.InfrastructureConfig{
		Postgres: &inventory.PostgresConfig{Enabled: false, Databases: []inventory.DatabaseConfig{{Name: "x"}}},
	}}
	if got := postgresDatabaseNames(disabled); got != nil {
		t.Errorf("disabled postgres: got %v, want nil", got)
	}

	enabled := &inventory.Manifest{Infrastructure: inventory.InfrastructureConfig{
		Postgres: &inventory.PostgresConfig{Enabled: true, Databases: []inventory.DatabaseConfig{
			{Name: "commodore"}, {Name: "purser"},
		}},
	}}
	got := postgresDatabaseNames(enabled)
	if len(got) != 2 || got[0] != "commodore" || got[1] != "purser" {
		t.Errorf("enabled postgres: got %v, want [commodore purser]", got)
	}
}
