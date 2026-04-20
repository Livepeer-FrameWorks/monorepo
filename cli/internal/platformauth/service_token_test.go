package platformauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwcfg "frameworks/cli/internal/config"
)

func TestResolveManifestServiceToken_NoGitopsContext(t *testing.T) {
	_, err := ResolveManifestServiceToken(context.Background(), fwcfg.Context{}, fwcfg.Config{})
	if err == nil {
		t.Fatal("expected error when context has no gitops source")
	}
	if !strings.Contains(err.Error(), "frameworks setup") {
		t.Errorf("error should point operator at 'frameworks setup', got: %v", err)
	}
}

func TestResolveManifestServiceToken_ReturnsTokenFromEnvFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "secrets.env"), "SERVICE_TOKEN=abc123\n")
	manifestPath := filepath.Join(dir, "cluster.yaml")
	writeFile(t, manifestPath, "version: \"1\"\ntype: cluster\nenv_files:\n  - secrets.env\n")

	ctxCfg := fwcfg.Context{
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: manifestPath,
		},
	}

	token, err := ResolveManifestServiceToken(context.Background(), ctxCfg, fwcfg.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "abc123" {
		t.Errorf("token = %q, want abc123", token)
	}
}

func TestResolveManifestServiceToken_MissingToken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "other.env"), "JWT_SECRET=x\n")
	manifestPath := filepath.Join(dir, "cluster.yaml")
	writeFile(t, manifestPath, "version: \"1\"\ntype: cluster\nenv_files:\n  - other.env\n")

	ctxCfg := fwcfg.Context{
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: manifestPath,
		},
	}

	_, err := ResolveManifestServiceToken(context.Background(), ctxCfg, fwcfg.Config{})
	if err == nil {
		t.Fatal("expected error when SERVICE_TOKEN is absent from env_files")
	}
	if !strings.Contains(err.Error(), "SERVICE_TOKEN missing") {
		t.Errorf("error should name the missing key, got: %v", err)
	}
}

// TestResolveManifestServiceToken_WorksWithHostsFileManifest: a manifest
// that declares hosts with names only and offloads ExternalIP/SSH to a
// separate hosts_file must still let admin/edge resolve SERVICE_TOKEN.
// Full manifest validation would fail here because ExternalIP is empty.
func TestResolveManifestServiceToken_WorksWithHostsFileManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "secrets.env"), "SERVICE_TOKEN=hostsfile-token\n")
	manifestPath := filepath.Join(dir, "cluster.yaml")
	writeFile(t, manifestPath, `version: "1"
type: cluster
hosts_file: hosts.yaml
hosts:
  host-a: {}
  host-b: {}
env_files:
  - secrets.env
`)

	ctxCfg := fwcfg.Context{
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: manifestPath,
		},
	}

	token, err := ResolveManifestServiceToken(context.Background(), ctxCfg, fwcfg.Config{})
	if err != nil {
		t.Fatalf("resolver must not require hosts_file to be loaded: %v", err)
	}
	if token != "hostsfile-token" {
		t.Errorf("token = %q, want hostsfile-token", token)
	}
}

// TestResolveManifestServiceToken_IgnoresEnvOverrides: FRAMEWORKS_MANIFEST
// and friends must NOT redirect the resolver. Only the active context's
// persisted gitops settings decide which manifest provides SERVICE_TOKEN.
func TestResolveManifestServiceToken_IgnoresEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ctx.env"), "SERVICE_TOKEN=from-context\n")
	ctxManifest := filepath.Join(dir, "ctx-cluster.yaml")
	writeFile(t, ctxManifest, "version: \"1\"\ntype: cluster\nenv_files:\n  - ctx.env\n")

	shadowDir := t.TempDir()
	writeFile(t, filepath.Join(shadowDir, "shadow.env"), "SERVICE_TOKEN=shadow\n")
	shadowManifest := filepath.Join(shadowDir, "cluster.yaml")
	writeFile(t, shadowManifest, "version: \"1\"\ntype: cluster\nenv_files:\n  - shadow.env\n")

	t.Setenv("FRAMEWORKS_MANIFEST", shadowManifest)

	ctxCfg := fwcfg.Context{
		Gitops: &fwcfg.Gitops{
			Source:       fwcfg.GitopsManifest,
			ManifestPath: ctxManifest,
		},
	}

	token, err := ResolveManifestServiceToken(context.Background(), ctxCfg, fwcfg.Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "from-context" {
		t.Errorf("env override leaked through; token = %q, want from-context", token)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
