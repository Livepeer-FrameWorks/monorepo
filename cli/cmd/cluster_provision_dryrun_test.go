package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// A cluster env file that cannot be loaded means the real run would fail
// before deploying, so the dry-run reports a failure instead of an
// inconclusive annotation that lets it pass.
func TestDryRunFailsWhenClusterEnvFilesDoNotLoad(t *testing.T) {
	manifest := contractProductionManifest(t)
	cluster := manifest.Clusters[contractTestClusterID]
	cluster.EnvFiles = []string{"missing-cluster.env"}
	manifest.Clusters[contractTestClusterID] = cluster
	sharedEnv := testLoadSharedEnv(t, manifest)
	rc := &resolvedCluster{Manifest: manifest, ManifestPath: filepath.Join(t.TempDir(), "cluster.yaml")}

	annotate, contract, cleanup := buildDryRunTaskCompare(context.Background(), &cobra.Command{}, rc, manifest, "", sharedEnv)
	defer cleanup()
	suffix := annotate(contractTask("commodore"))
	if !strings.Contains(suffix, "would fail") || !strings.Contains(suffix, "cluster env_files") {
		t.Fatalf("annotation = %q, want a failure naming the cluster env_files load", suffix)
	}
	if err := contract(); err == nil || !strings.Contains(err.Error(), "missing-cluster.env") {
		t.Fatalf("dry-run result = %v, want the env-load failure", err)
	}
}
