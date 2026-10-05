package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/inventory"
)

// The doctor's edge-config check must authenticate against the cluster the
// doctor was pointed at. With production active and staging passed through
// --gitops-dir, it must carry staging's SERVICE_TOKEN and none of production's
// saved endpoints or cluster scope.
func TestDoctorEdgeConfigContextFollowsResolvedManifest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	writeManifest := func(token string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("SERVICE_TOKEN="+token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "cluster.yaml")
		if err := os.WriteFile(path, []byte("version: \"1\"\ntype: cluster\nenv_files:\n  - secrets.env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	prodManifest := writeManifest("production-token")
	stagingManifest := writeManifest("staging-token")

	prodEndpoints := fwcfg.DefaultEndpoints()
	prodEndpoints.QuartermasterGRPCAddr = "quartermaster.prod.example:18002"
	if err := fwcfg.Save(fwcfg.Config{
		Current: "production",
		Contexts: map[string]fwcfg.Context{"production": {
			Name:      "production",
			Persona:   fwcfg.PersonaPlatform,
			ClusterID: "production-cluster",
			Endpoints: prodEndpoints,
			Gitops:    &fwcfg.Gitops{Source: fwcfg.GitopsManifest, ManifestPath: prodManifest},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	staging := &inventory.Manifest{EnvFiles: []string{"secrets.env"}}
	rc := &resolvedCluster{Manifest: staging, ManifestPath: stagingManifest, Source: inventory.SourceGitopsDirFlag,
		Persona: fwcfg.PersonaPlatform, ContextName: "production"}
	got, err := doctorEdgeConfigContext(context.Background(), rc)
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	if got.Auth.ServiceToken != "staging-token" {
		t.Fatalf("service token = %q, want staging-token from the --gitops-dir manifest", got.Auth.ServiceToken)
	}
	if got.ClusterID != "" || got.Endpoints.QuartermasterGRPCAddr == prodEndpoints.QuartermasterGRPCAddr {
		t.Fatalf("carried the active context's scope: cluster=%q quartermaster=%q", got.ClusterID, got.Endpoints.QuartermasterGRPCAddr)
	}

	rc.Source = inventory.SourceContext
	rc.ManifestPath = prodManifest
	got, err = doctorEdgeConfigContext(context.Background(), rc)
	if err != nil {
		t.Fatalf("context-sourced: %v", err)
	}
	if got.Auth.ServiceToken != "production-token" || got.ClusterID != "production-cluster" {
		t.Fatalf("context-sourced doctor = token %q cluster %q, want the active context", got.Auth.ServiceToken, got.ClusterID)
	}
}
