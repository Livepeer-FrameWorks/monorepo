package cmd

import (
	"os"
	"path/filepath"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

func newPreflightTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("manifest", "", "")
	cmd.Flags().String("gitops-dir", "", "")
	cmd.Flags().String("github-repo", "", "")
	return cmd
}

func withTempHomeAndCwd(t *testing.T) (home, cwd string, restore func()) {
	t.Helper()
	home = t.TempDir()
	cwd = t.TempDir()

	prevHome, hadHome := os.LookupEnv("XDG_CONFIG_HOME")
	prevWD, _ := os.Getwd()
	os.Setenv("XDG_CONFIG_HOME", home)
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	prevEnv := map[string]string{}
	for _, k := range []string{"FRAMEWORKS_MANIFEST", "FRAMEWORKS_GITOPS_DIR", "FRAMEWORKS_GITHUB_REPO"} {
		prevEnv[k] = os.Getenv(k)
		os.Unsetenv(k)
	}

	restore = func() {
		if hadHome {
			os.Setenv("XDG_CONFIG_HOME", prevHome)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
		for k, v := range prevEnv {
			if v != "" {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
		_ = os.Chdir(prevWD)
		fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})
	}
	return home, cwd, restore
}

func TestAnyManifestSourceConfigured_noneReturnsFalse(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	if anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("expected false with no flags/env/context/cwd manifest source")
	}
}

func TestAnyManifestSourceConfigured_contextGitopsReturnsTrue(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	gitopsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gitopsDir, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitopsDir, ".sops.yaml"), []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatalf("write .sops.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitopsDir, "clusters", "dev", "cluster.yaml"), []byte("type: central\n"), 0o644); err != nil {
		t.Fatalf("write cluster.yaml: %v", err)
	}

	cfg := fwcfg.Config{
		Current: "ctx",
		Contexts: map[string]fwcfg.Context{
			"ctx": {Name: "ctx", Gitops: &fwcfg.Gitops{Source: fwcfg.GitopsLocal, LocalPath: gitopsDir, Cluster: "dev"}},
		},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	if !anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("expected true when active context gitops points at a valid repo")
	}
}

func TestAnyManifestSourceConfigured_contextGitopsMissingPathReturnsFalse(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	cfg := fwcfg.Config{
		Current: "ctx",
		Contexts: map[string]fwcfg.Context{
			"ctx": {Name: "ctx", Gitops: &fwcfg.Gitops{Source: fwcfg.GitopsLocal, LocalPath: "/does/not/exist"}},
		},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	if anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("stale context gitops (missing path) must not satisfy the check")
	}
}

func TestAnyManifestSourceConfigured_contextLastManifestPathReturnsTrue(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	manifest := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := os.WriteFile(manifest, []byte("type: central\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cfg := fwcfg.Config{
		Current: "ctx",
		Contexts: map[string]fwcfg.Context{
			"ctx": {Name: "ctx", LastManifestPath: manifest},
		},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	if !anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("expected true when active context has LastManifestPath pointing at an existing file")
	}
}

func TestAnyManifestSourceConfigured_contextLastManifestPathMissingReturnsFalse(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	cfg := fwcfg.Config{
		Current: "ctx",
		Contexts: map[string]fwcfg.Context{
			"ctx": {Name: "ctx", LastManifestPath: "/tmp/definitely-not-real/cluster.yaml"},
		},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	if anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("stale LastManifestPath (file missing) should not satisfy the check")
	}
}

func TestAnyManifestSourceConfigured_cwdGitopsRootReturnsTrue(t *testing.T) {
	_, cwd, restore := withTempHomeAndCwd(t)
	defer restore()

	if err := os.MkdirAll(filepath.Join(cwd, "clusters", "dev"), 0o755); err != nil {
		t.Fatalf("mkdir clusters: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".sops.yaml"), []byte("creation_rules: []\n"), 0o644); err != nil {
		t.Fatalf("write .sops.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "clusters", "dev", "cluster.yaml"), []byte("type: central\n"), 0o644); err != nil {
		t.Fatalf("write cluster.yaml: %v", err)
	}

	if !anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("cwd gitops-root (clusters/ + .sops.yaml) should satisfy the check")
	}
}

func TestAnyManifestSourceConfigured_cwdClusterYAMLAloneReturnsFalse(t *testing.T) {
	_, cwd, restore := withTempHomeAndCwd(t)
	defer restore()

	if err := os.WriteFile(filepath.Join(cwd, "cluster.yaml"), []byte("type: central\n"), 0o644); err != nil {
		t.Fatalf("write cluster.yaml: %v", err)
	}

	if anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("bare cluster.yaml in cwd should NOT satisfy the check")
	}
}

func TestAnyManifestSourceConfigured_envVarReturnsTrue(t *testing.T) {
	_, _, restore := withTempHomeAndCwd(t)
	defer restore()

	os.Setenv("FRAMEWORKS_MANIFEST", "/some/path/cluster.yaml")
	defer os.Unsetenv("FRAMEWORKS_MANIFEST")

	if !anyManifestSourceConfigured(newPreflightTestCmd()) {
		t.Error("FRAMEWORKS_MANIFEST env should satisfy the check")
	}
}

func TestResolvePostgresConnectivityHostUsesFirstNodeForYugabyte(t *testing.T) {
	pg := &inventory.PostgresConfig{
		Engine: "yugabyte",
		Nodes: []inventory.PostgresNode{
			{Host: "yuga-1"},
			{Host: "yuga-2"},
		},
	}

	if got := resolvePostgresConnectivityHost(pg); got != "yuga-1" {
		t.Fatalf("expected first Yugabyte node host, got %q", got)
	}
}

func TestResolvePostgresConnectivityHostFallsBackToSingleHost(t *testing.T) {
	pg := &inventory.PostgresConfig{Host: "postgres-1"}

	if got := resolvePostgresConnectivityHost(pg); got != "postgres-1" {
		t.Fatalf("expected postgres host fallback, got %q", got)
	}
}
