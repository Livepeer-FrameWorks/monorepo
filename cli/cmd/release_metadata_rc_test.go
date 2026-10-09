package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// An rc tag builds a CLI that reports the rc version. The pending release's
// floor must let that CLI deploy its own release.
func TestReleaseMetadataLetsTheRCCLIDeployItsRelease(t *testing.T) {
	cmd := newReleaseMetadataCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"v0.3.13-rc1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("release-metadata v0.3.13-rc1: %v", err)
	}
	if !strings.Contains(buf.String(), "min_cli_version: ") {
		t.Fatalf("metadata carries no min_cli_version:\n%s", buf.String())
	}
}

func TestCheckReleaseDeploysItself(t *testing.T) {
	cases := []struct {
		version, minCLI string
		ok              bool
	}{
		{"v0.3.13-rc1", "v0.3.13", false},
		{"v0.3.13-rc2", "v0.3.13-rc3", false},
		{"v0.3.13-rc1", "v0.3.13-rc1", true},
		{"v0.3.13-rc2", "v0.3.13-rc1", true},
		{"v0.3.13", "v0.3.13-rc1", true},
		{"v0.3.13", "v0.3.13", true},
		{"v0.3.13", "", true},
	}
	for _, tc := range cases {
		err := checkReleaseDeploysItself(tc.version, tc.minCLI)
		if (err == nil) != tc.ok {
			t.Errorf("checkReleaseDeploysItself(%s, %s) = %v, want ok=%v", tc.version, tc.minCLI, err, tc.ok)
		}
	}
}
