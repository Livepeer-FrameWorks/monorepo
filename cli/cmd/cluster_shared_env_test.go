package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestPreparedSharedEnvPersistsGeneratedDevelopmentAuthority(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(manifestPath, []byte("profile: dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := &inventory.Manifest{Profile: "dev"}
	first := &resolvedCluster{Manifest: manifest, ManifestPath: manifestPath, SourcePersistsManifest: true}
	firstEnv, err := first.PreparedSharedEnv()
	if err != nil {
		t.Fatal(err)
	}
	second := &resolvedCluster{Manifest: manifest, ManifestPath: manifestPath, SourcePersistsManifest: true}
	secondEnv, err := second.PreparedSharedEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"MEDIA_AUTHORITY_SIGNING_PRIVATE_KEY_PEM_B64", "MEDIA_AUTHORITY_SEAL_ROOT_SECRET", "FOGHORN_STATE_ENCRYPTION_KEY"} {
		if firstEnv[key] == "" || firstEnv[key] != secondEnv[key] {
			t.Fatalf("generated %s was not stable across invocations", key)
		}
	}
	info, err := os.Stat(filepath.Join(dir, devGeneratedSecretsRelativePath))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("generated secret mode = %o, want 600", info.Mode().Perm())
	}
}

func TestPreparedSharedEnvRefusesEphemeralDevelopmentSource(t *testing.T) {
	rc := &resolvedCluster{Manifest: &inventory.Manifest{Profile: "dev"}}
	if _, err := rc.PreparedSharedEnv(); err == nil {
		t.Fatal("ephemeral development source received process-local generated secrets")
	}
}
