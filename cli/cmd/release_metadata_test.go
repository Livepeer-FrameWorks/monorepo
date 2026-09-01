package cmd

import (
	"bytes"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseMetadata_EmitsV0_3Boundary(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"v0.3.0"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("release-metadata v0.3.0: %v", err)
	}

	var meta struct {
		MinCLIVersion    string   `yaml:"min_cli_version"`
		RollbackDisabled []string `yaml:"rollback_disabled"`
	}
	if err := yaml.Unmarshal(buf.Bytes(), &meta); err != nil {
		t.Fatalf("emitted manifest is not valid YAML: %v\n%s", err, buf.String())
	}
	if meta.MinCLIVersion != "v0.3.0-rc1" || len(meta.RollbackDisabled) != 3 || meta.RollbackDisabled[0] != "foghorn" || meta.RollbackDisabled[1] != "navigator" || meta.RollbackDisabled[2] != "purser" {
		t.Fatalf("metadata = %+v, want v0.3.0 floor and Foghorn/Navigator/Purser rollback disabled\nfull output:\n%s", meta, buf.String())
	}
}

// A prerelease target resolves to the same base entry, so it must emit the same rollback policy.
func TestReleaseMetadata_RollbackDisabledIsBaseNormalized(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"v0.3.0-rc3"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("release-metadata v0.3.0-rc3: %v", err)
	}
	if !strings.Contains(buf.String(), "min_cli_version: v0.3.0-rc1") || !strings.Contains(buf.String(), "- foghorn") || !strings.Contains(buf.String(), "- navigator") || !strings.Contains(buf.String(), "- purser") {
		t.Fatalf("prerelease must emit the base release's v0.3 metadata; got:\n%s", buf.String())
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
