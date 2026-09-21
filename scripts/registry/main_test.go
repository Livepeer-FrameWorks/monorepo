package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateEnforcementRejectsUnknownGatesMissingFieldsAndEnforcers(t *testing.T) {
	root := t.TempDir()
	for _, def := range enforcementGates {
		for _, enforcer := range def.Enforcers {
			path := filepath.Join(root, filepath.FromSlash(enforcer))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	fields := map[string]bool{}
	for _, def := range enforcementGates {
		fields[def.CapabilitiesField] = true
	}
	reg := &Registry{Features: []Feature{{Slug: "tenant", EnforcedBy: []string{"custom-domain"}}}}

	v := &validator{repoRoot: root, schemaFields: fields}
	v.validateEnforcement(reg)
	if len(v.errors) != 0 {
		t.Fatalf("complete gate set rejected: %v", v.errors)
	}

	reg.Features[0].Subitems = []Feature{{Slug: "branding", EnforcedBy: []string{"custom-branding", "custom-branding"}}}
	delete(fields, "TenantCapabilities.customDomain")
	if err := os.Remove(filepath.Join(root, "api_control", "internal", "grpc", "media_retention.go")); err != nil {
		t.Fatal(err)
	}
	v = &validator{repoRoot: root, schemaFields: fields}
	v.validateEnforcement(reg)
	joined := strings.Join(v.errors, "\n")
	for _, want := range []string{
		`branding: enforced_by gate "custom-branding" is not a known gate`,
		`branding: enforced_by lists "custom-branding" twice`,
		`enforcement gate "custom-domain": capabilities field "TenantCapabilities.customDomain" not found`,
		`enforcement gate "recording-retention": enforcer file "api_control/internal/grpc/media_retention.go" does not exist`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing error %q in:\n%s", want, joined)
		}
	}
}

func TestShippedProductSlugsIncludesShippedSubitemsOnly(t *testing.T) {
	reg := Registry{Features: []Feature{
		{Slug: "playback", Status: "shipped", Subitems: []Feature{
			{Slug: "viewer-protocol-selection", Status: "shipped"},
			{Slug: "player-roadmap", Status: "roadmap"},
		}},
		{Slug: "processing", Status: "partial", Subitems: []Feature{{Slug: "ai", Status: "shipped"}}},
	}}
	got := strings.Join(shippedProductSlugs(reg), ",")
	if got != "ai,playback,viewer-protocol-selection" {
		t.Fatalf("shipped slugs = %s", got)
	}
}
