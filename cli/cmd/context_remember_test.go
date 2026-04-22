package cmd

import (
	"os"
	"path/filepath"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func withTempConfig(t *testing.T, ctxName string) (restore func()) {
	t.Helper()
	dir := t.TempDir()
	prevXDG, hadXDG := os.LookupEnv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)

	cfg := fwcfg.Config{
		Current:  ctxName,
		Contexts: map[string]fwcfg.Context{ctxName: {Name: ctxName}},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	prevRuntime := fwcfg.GetRuntimeOverrides()
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	return func() {
		fwcfg.SetRuntimeOverrides(prevRuntime)
		if hadXDG {
			os.Setenv("XDG_CONFIG_HOME", prevXDG)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	}
}

func TestRememberLastManifest_persistsOnActiveContext(t *testing.T) {
	restore := withTempConfig(t, "test-ctx")
	defer restore()

	path := filepath.Join(t.TempDir(), "cluster.yaml")
	rememberLastManifest(nil, path)

	cfg, err := fwcfg.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if got := cfg.Contexts["test-ctx"].LastManifestPath; got != path {
		t.Errorf("LastManifestPath = %q, want %q", got, path)
	}
}

func TestRememberLastManifest_emptyIsNoOp(t *testing.T) {
	restore := withTempConfig(t, "test-ctx")
	defer restore()

	rememberLastManifest(nil, "")

	cfg, _ := fwcfg.Load()
	if got := cfg.Contexts["test-ctx"].LastManifestPath; got != "" {
		t.Errorf("empty path should not persist, got %q", got)
	}
}

func TestRememberSystemTenantID_persistsOnActiveContext(t *testing.T) {
	restore := withTempConfig(t, "test-ctx")
	defer restore()

	rememberSystemTenantID(nil, "abc-123")

	cfg, _ := fwcfg.Load()
	if got := cfg.Contexts["test-ctx"].SystemTenantID; got != "abc-123" {
		t.Errorf("SystemTenantID = %q, want %q", got, "abc-123")
	}
}

func TestRememberInActiveContext_noContextIsSafeNoOp(t *testing.T) {
	dir := t.TempDir()
	prev, had := os.LookupEnv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_CONFIG_HOME", prev)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	})

	rememberLastManifest(nil, "/some/path")
	rememberSystemTenantID(nil, "some-uuid")
}

func TestTenantIDFromContext_readsSystemTenantID(t *testing.T) {
	restore := withTempConfig(t, "test-ctx")
	defer restore()

	rememberSystemTenantID(nil, "tenant-xyz")

	got := tenantIDFromContext()
	if got != "tenant-xyz" {
		t.Errorf("tenantIDFromContext = %q, want %q", got, "tenant-xyz")
	}
}

func TestClusterIDFromContext_readsClusterID(t *testing.T) {
	dir := t.TempDir()
	prev, had := os.LookupEnv("XDG_CONFIG_HOME")
	os.Setenv("XDG_CONFIG_HOME", dir)
	t.Cleanup(func() {
		if had {
			os.Setenv("XDG_CONFIG_HOME", prev)
		} else {
			os.Unsetenv("XDG_CONFIG_HOME")
		}
	})

	cfg := fwcfg.Config{
		Current:  "c1",
		Contexts: map[string]fwcfg.Context{"c1": {Name: "c1", ClusterID: "cluster-abc"}},
	}
	if err := fwcfg.Save(cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})
	t.Cleanup(func() { fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{}) })

	if got := clusterIDFromContext(); got != "cluster-abc" {
		t.Errorf("clusterIDFromContext = %q, want %q", got, "cluster-abc")
	}
}
