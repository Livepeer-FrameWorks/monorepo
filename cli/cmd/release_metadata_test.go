package cmd

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The v0.2.97 release manifest MUST carry rollback_disabled: [chandler] so a deploying CLI skips automatic rollback
// for Chandler across the /ready contract cut. This asserts the GENERATED YAML (catalog → release-metadata → manifest
// lines), not just the in-memory lookup — the CI pipeline appends exactly this output to dist/manifest.yaml.
func TestReleaseMetadata_EmitsRollbackDisabledForV0_2_97(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"v0.2.97"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("release-metadata v0.2.97: %v", err)
	}

	var meta struct {
		MinCLIVersion    string   `yaml:"min_cli_version"`
		RollbackDisabled []string `yaml:"rollback_disabled"`
	}
	if err := yaml.Unmarshal(buf.Bytes(), &meta); err != nil {
		t.Fatalf("emitted manifest is not valid YAML: %v\n%s", err, buf.String())
	}
	if len(meta.RollbackDisabled) != 1 || meta.RollbackDisabled[0] != "chandler" {
		t.Fatalf("rollback_disabled = %v, want [chandler]\nfull output:\n%s", meta.RollbackDisabled, buf.String())
	}
}

// A prerelease target resolves to the same base entry, so it must emit the same rollback policy.
func TestReleaseMetadata_RollbackDisabledIsBaseNormalized(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"v0.2.97-rc3"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("release-metadata v0.2.97-rc3: %v", err)
	}
	if !strings.Contains(buf.String(), "rollback_disabled:") || !strings.Contains(buf.String(), "- chandler") {
		t.Fatalf("prerelease must emit the base release's rollback policy; got:\n%s", buf.String())
	}
}

// Release generation must FAIL for a target that is not declared in the catalog, so an undeclared release can never
// publish partial metadata that silently drops a per-release boundary (e.g. a v0.3.0 that never restates the Chandler
// rollback cut). This forces every release to declare its explicit catalog entry.
func TestReleaseMetadata_RejectsUndeclaredTarget(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"v0.2.98"}) // not declared in the catalog
	if err := cmd.Execute(); err == nil {
		t.Fatalf("release-metadata must reject an undeclared target; emitted:\n%s", buf.String())
	}
}

// A catalog typo (a deploy name that is not a real service) must FAIL release generation, not silently disable
// rollback for nothing.
func TestValidateRollbackDisabledNames_RejectsUnknown(t *testing.T) {
	if err := validateRollbackDisabledNames([]string{"chandler"}); err != nil {
		t.Fatalf("a valid deploy name must pass: %v", err)
	}
	if err := validateRollbackDisabledNames([]string{"chndler"}); err == nil {
		t.Fatal("a typo'd deploy name must be rejected")
	}
}
