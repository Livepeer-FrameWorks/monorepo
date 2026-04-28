package bootstrap

import (
	"strings"
	"testing"
)

func TestValidAlias(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"frameworks", true},
		{"acme-co", true},
		{"a", true},
		{"", false},
		{"-leading", false},
		{"1leading", false},
		{"UPPER", false},
		{"with space", false},
		{"too" + repeat("x", 64), false},
	}
	for _, c := range cases {
		if got := ValidAlias(c.s); got != c.want {
			t.Errorf("ValidAlias(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestAliasFromRef(t *testing.T) {
	cases := []struct {
		ref     string
		want    string
		wantErr bool
	}{
		{"quartermaster.system_tenant", SystemTenantAlias, false},
		{"quartermaster.tenants[acme]", "acme", false},
		{"quartermaster.tenants[BAD]", "", true},
		{"something.else", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := AliasFromRef(c.ref)
		if c.wantErr {
			if err == nil {
				t.Errorf("AliasFromRef(%q) expected error", c.ref)
			}
			continue
		}
		if err != nil {
			t.Errorf("AliasFromRef(%q) unexpected error: %v", c.ref, err)
			continue
		}
		if got != c.want {
			t.Errorf("AliasFromRef(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}

func TestAliasMapLookup(t *testing.T) {
	m := &AliasMap{byAlias: map[string]string{"frameworks": "uuid-system", "acme": "uuid-acme"}}
	id, ok := m.LookupAlias("frameworks")
	if !ok || id != "uuid-system" {
		t.Errorf("LookupAlias(frameworks) = (%q, %v)", id, ok)
	}
	id, err := m.LookupRef("quartermaster.tenants[acme]")
	if err != nil || id != "uuid-acme" {
		t.Errorf("LookupRef customer = (%q, %v)", id, err)
	}
	if _, err := m.LookupRef("quartermaster.tenants[missing]"); err == nil {
		t.Error("LookupRef on missing alias: expected error")
	}
}

func repeat(s string, n int) string { return strings.Repeat(s, n) }
