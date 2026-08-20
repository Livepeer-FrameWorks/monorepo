package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

// A non-persistent (GitHub temp-checkout) source must be REFUSED with a clear error, never reported as success —
// otherwise the operator believes the channel changed while the source repo is untouched. A persistent source patches
// the on-disk file in place.
func TestRunSetChannel_SourcePersistence(t *testing.T) {
	writeManifest := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "cluster.yaml")
		if err := os.WriteFile(p, []byte("version: \"1\"\ntype: cluster\nchannel: stable\n"), 0644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("non-persistent source refuses and does not write", func(t *testing.T) {
		p := writeManifest(t)
		rc := &resolvedCluster{
			Manifest:               &inventory.Manifest{Channel: "stable"},
			ManifestPath:           p,
			Source:                 inventory.SourceGithubRepoFlag,
			SourcePersistsManifest: false,
		}
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		err := runSetChannel(cmd, rc, "rc")
		if err == nil || !strings.Contains(err.Error(), "non-writable source") {
			t.Fatalf("expected a non-writable-source error, got: %v", err)
		}
		// The on-disk file must be untouched.
		data, _ := os.ReadFile(p)
		if !strings.Contains(string(data), "channel: stable") {
			t.Fatalf("file must be unchanged, got:\n%s", data)
		}
	})

	t.Run("persistent source patches in place", func(t *testing.T) {
		p := writeManifest(t)
		rc := &resolvedCluster{
			Manifest:               &inventory.Manifest{Channel: "stable"},
			ManifestPath:           p,
			Source:                 inventory.SourceGitopsDirFlag,
			SourcePersistsManifest: true,
		}
		cmd := &cobra.Command{}
		out := &bytes.Buffer{}
		cmd.SetOut(out)
		if err := runSetChannel(cmd, rc, "rc"); err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		data, _ := os.ReadFile(p)
		if !strings.Contains(string(data), "channel: rc") {
			t.Fatalf("channel not patched, got:\n%s", data)
		}
		if !strings.Contains(out.String(), "cluster release plan") || !strings.Contains(out.String(), "cluster release apply --dry-run") {
			t.Fatalf("next steps must use the canonical release lifecycle, got:\n%s", out.String())
		}
		if strings.Contains(out.String(), "cluster upgrade --all") {
			t.Fatalf("next steps must not present the recovery-only upgrade flow as canonical, got:\n%s", out.String())
		}
	})
}

func TestManifestSourcePersistsToDisk(t *testing.T) {
	cases := []struct {
		source inventory.ManifestSource
		want   bool
	}{
		{inventory.SourceManifestFlag, true},
		{inventory.SourceGitopsDirEnv, true},
		{inventory.SourceCwdHeuristic, true},
		{inventory.SourceContextLastManifest, true},
		{inventory.SourceGithubRepoFlag, false},
		{inventory.SourceGithubRepoEnv, false},
	}
	for _, tc := range cases {
		if got := manifestSourcePersistsToDisk(tc.source, fwcfg.Context{}); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.source, got, tc.want)
		}
	}
}
