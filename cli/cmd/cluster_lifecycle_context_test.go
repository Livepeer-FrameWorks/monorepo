package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	fwcfg "frameworks/cli/internal/config"
	fwcredentials "frameworks/cli/internal/credentials"
	"frameworks/cli/pkg/inventory"

	"github.com/spf13/cobra"
)

// lifecycleFixture saves an active production platform context with its own
// gitops manifest, saved Quartermaster endpoint and cluster scope, and writes
// a second (staging) manifest a command can select explicitly.
type lifecycleFixture struct {
	prodManifest, stagingManifest string
	prodQuartermaster             string
}

func newLifecycleFixture(t *testing.T) lifecycleFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv(fwcredentials.EnvUserToken, "operator-jwt")
	for _, k := range []string{"FRAMEWORKS_MANIFEST", "FRAMEWORKS_GITOPS_DIR", "FRAMEWORKS_GITHUB_REPO"} {
		t.Setenv(k, "")
	}
	fwcfg.SetRuntimeOverrides(fwcfg.RuntimeOverrides{})

	writeManifest := func(token string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "secrets.env"), []byte("SERVICE_TOKEN="+token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "cluster.yaml")
		if err := os.WriteFile(path, []byte("version: \"1\"\ntype: cluster\nprofile: production\nhosts:\n  core-1:\n    external_ip: 203.0.113.10\n    user: root\nenv_files:\n  - secrets.env\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	f := lifecycleFixture{
		prodManifest:      writeManifest("production-token"),
		stagingManifest:   writeManifest("staging-token"),
		prodQuartermaster: "quartermaster.prod.example:18002",
	}
	prodEndpoints := fwcfg.DefaultEndpoints()
	prodEndpoints.QuartermasterGRPCAddr = f.prodQuartermaster
	if err := fwcfg.Save(fwcfg.Config{
		Current: "production",
		Contexts: map[string]fwcfg.Context{"production": {
			Name:           "production",
			Persona:        fwcfg.PersonaPlatform,
			ClusterID:      "production-cluster",
			SystemTenantID: "production-system-tenant",
			Endpoints:      prodEndpoints,
			Gitops:         &fwcfg.Gitops{Source: fwcfg.GitopsManifest, ManifestPath: f.prodManifest, Cluster: "production"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f lifecycleFixture) assertStaging(t *testing.T, label string, got fwcfg.Context) {
	t.Helper()
	if got.Auth.ServiceToken != "staging-token" {
		t.Fatalf("%s: service token = %q, want staging-token from the selected manifest", label, got.Auth.ServiceToken)
	}
	if got.ClusterID != "" || got.Endpoints.QuartermasterGRPCAddr == f.prodQuartermaster {
		t.Fatalf("%s: carried the active context's scope: cluster=%q quartermaster=%q", label, got.ClusterID, got.Endpoints.QuartermasterGRPCAddr)
	}
}

func (f lifecycleFixture) stagingRC(source inventory.ManifestSource, overrides bool) *resolvedCluster {
	return &resolvedCluster{Manifest: &inventory.Manifest{EnvFiles: []string{"secrets.env"}}, ManifestPath: f.stagingManifest,
		Source: source, Persona: fwcfg.PersonaPlatform, ContextName: "production", ContextSystemTenantID: "production-system-tenant",
		ClusterOverridesContext: overrides}
}

// The doctor's edge-config check must authenticate against the cluster the
// doctor was pointed at. With production active and staging selected through
// --gitops-dir, or through --cluster on the context's own gitops source, it
// must carry staging's SERVICE_TOKEN and none of production's saved endpoints
// or cluster scope.
func TestDoctorEdgeConfigContextFollowsResolvedManifest(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, tc := range []struct {
		label     string
		source    inventory.ManifestSource
		overrides bool
	}{
		{"--gitops-dir", inventory.SourceGitopsDirFlag, false},
		{"--manifest", inventory.SourceManifestFlag, false},
		{"--cluster on the context's gitops", inventory.SourceContext, true},
	} {
		got, err := lifecycleContextForResolved(context.Background(), f.stagingRC(tc.source, tc.overrides))
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		f.assertStaging(t, tc.label, got)
	}

	rc := f.stagingRC(inventory.SourceContext, false)
	rc.ManifestPath = f.prodManifest
	got, err := lifecycleContextForResolved(context.Background(), rc)
	if err != nil {
		t.Fatalf("context-sourced: %v", err)
	}
	if got.Auth.ServiceToken != "production-token" || got.ClusterID != "production-cluster" {
		t.Fatalf("context-sourced = token %q cluster %q, want the active context", got.Auth.ServiceToken, got.ClusterID)
	}
}

// Release reconciliation against an explicitly selected manifest must not dial
// the active context's saved endpoints or cluster scope.
func TestReconcileContextFollowsResolvedManifest(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, tc := range []struct {
		label     string
		source    inventory.ManifestSource
		overrides bool
	}{
		{"--gitops-dir", inventory.SourceGitopsDirFlag, false},
		{"--cluster on the context's gitops", inventory.SourceContext, true},
	} {
		got, err := reconcileContextForResolved(context.Background(), f.stagingRC(tc.source, tc.overrides))
		if err != nil {
			t.Fatalf("%s: %v", tc.label, err)
		}
		f.assertStaging(t, tc.label, got)
		if got.Auth.JWT != "operator-jwt" {
			t.Fatalf("%s: operator JWT = %q", tc.label, got.Auth.JWT)
		}
	}
}

// cluster nodes / cluster releases subcommands inherit --manifest,
// --gitops-dir and --cluster from the cluster command; the context they
// authenticate with must follow them.
func TestClusterLifecycleAccessFollowsSelectedManifest(t *testing.T) {
	f := newLifecycleFixture(t)
	newCmd := func() *cobra.Command {
		root := newClusterCmd()
		for _, c := range root.Commands() {
			if c.Name() == "nodes" {
				return c
			}
		}
		t.Fatal("cluster nodes command not found")
		return nil
	}

	cmd := newCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Root().PersistentFlags().Set("manifest", f.stagingManifest); err != nil {
		t.Fatal(err)
	}
	got, _, cleanup, err := clusterLifecycleAccess(cmd)
	if err != nil {
		t.Fatalf("--manifest: %v", err)
	}
	cleanup()
	f.assertStaging(t, "--manifest", got)

	cmd = newCmd()
	cmd.SetContext(context.Background())
	got, _, cleanup, err = clusterLifecycleAccess(cmd)
	if err != nil {
		t.Fatalf("active context: %v", err)
	}
	cleanup()
	if got.Auth.ServiceToken != "production-token" || got.ClusterID != "production-cluster" {
		t.Fatalf("no selection = token %q cluster %q, want the active context", got.Auth.ServiceToken, got.ClusterID)
	}
}

// The provision summary's readiness recheck reuses the active context's saved
// system tenant only for that context's own cluster.
func TestProvisionReadinessSystemTenantFollowsSelectedManifest(t *testing.T) {
	f := newLifecycleFixture(t)
	for _, tc := range []struct {
		label     string
		source    inventory.ManifestSource
		overrides bool
		want      any
	}{
		{"context", inventory.SourceContext, false, "production-system-tenant"},
		{"--gitops-dir", inventory.SourceGitopsDirFlag, false, nil},
		{"--cluster on the context's gitops", inventory.SourceContext, true, nil},
	} {
		if got := collectRuntimeForReadinessOnly(f.stagingRC(tc.source, tc.overrides))["system_tenant_id"]; got != tc.want {
			t.Fatalf("%s: system_tenant_id = %v, want %v", tc.label, got, tc.want)
		}
	}
}

// An edge manifest's control-plane context is derived from its cluster
// manifest; the active context's saved endpoints and cluster scope belong to
// another cluster and must not carry over.
func TestEdgeManifestControlPlaneContextIgnoresActiveScope(t *testing.T) {
	f := newLifecycleFixture(t)
	cfg, err := fwcfg.Load()
	if err != nil {
		t.Fatal(err)
	}
	base := cfg.Contexts["production"]
	got, _, err := edgeManifestControlPlaneContext(context.Background(), base, f.stagingManifest, "")
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	f.assertStaging(t, "--cluster-manifest", got)
}
