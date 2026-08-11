package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

type fakeReconcileQM struct {
	cluster *quartermasterpb.InfrastructureCluster
	getErr  error
	adopted []string
}

func (f *fakeReconcileQM) GetCluster(_ context.Context, _ string) (*quartermasterpb.ClusterResponse, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &quartermasterpb.ClusterResponse{Cluster: f.cluster}, nil
}

func (f *fakeReconcileQM) AdoptClusterStorageDescriptor(_ context.Context, clusterID, bucket, endpoint, region, prefix string) (*quartermasterpb.ClusterResponse, error) {
	f.adopted = []string{clusterID, bucket, endpoint, region, prefix}
	// After adoption the prefix is PRESENT (non-NULL), even when empty.
	f.cluster = &quartermasterpb.InfrastructureCluster{S3Bucket: bucket, S3Endpoint: endpoint, S3Region: region, S3Prefix: prefix, S3PrefixPresent: true}
	return &quartermasterpb.ClusterResponse{Cluster: f.cluster}, nil
}

func storageReconcileManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"edge-a": {Name: "edge-a", ExternalIP: "10.0.0.1"},
			"edge-b": {Name: "edge-b", ExternalIP: "10.0.0.2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Deploy: "foghorn", Cluster: "media-us", Hosts: []string{"edge-a", "edge-b"}},
		},
		Clusters: map[string]inventory.ClusterConfig{"media-us": {Name: "media-us"}},
	}
}

func TestStorageAdoptionTransition_Check(t *testing.T) {
	orig := readDeployedFoghornDescriptorFn
	defer func() { readDeployedFoghornDescriptorFn = orig }()

	tr := storageDescriptorAdoption{}
	scope := ReconcileScope{ClusterID: "media-us", Label: "media-us"}
	deployed := deployedDescriptor{bucket: "media-us", endpoint: "https://r2", region: "auto", prefix: "cell-a"}

	agree := func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		d := deployed
		d.host = h.ExternalIP
		return d, nil
	}

	cases := []struct {
		name   string
		qm     *fakeReconcileQM
		reader func(context.Context, *ssh.Pool, inventory.Host, string) (deployedDescriptor, error)
		want   ReconcileStatus
	}{
		{
			name:   "qm empty + replicas agree -> pending",
			qm:     &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}},
			reader: agree,
			want:   ReconcilePending,
		},
		{
			name:   "qm matches deployed (prefix present) -> complete",
			qm:     &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "auto", S3Prefix: "cell-a", S3PrefixPresent: true}},
			reader: agree,
			want:   ReconcileComplete,
		},
		{
			name:   "qm bucket set but prefix NULL (unadopted) -> pending one-time fill",
			qm:     &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "auto", S3Prefix: "", S3PrefixPresent: false}},
			reader: agree,
			want:   ReconcilePending,
		},
		{
			name:   "qm differs (repoint) -> blocked",
			qm:     &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "OTHER", S3Endpoint: "https://r2", S3Region: "auto", S3Prefix: "cell-a", S3PrefixPresent: true}},
			reader: agree,
			want:   ReconcileBlocked,
		},
		{
			name: "replicas disagree -> blocked",
			qm:   &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}},
			reader: func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
				d := deployed
				d.host = h.ExternalIP
				if h.ExternalIP == "10.0.0.2" {
					d.bucket = "media-us-2"
				}
				return d, nil
			},
			want: ReconcileBlocked,
		},
		{
			name: "no deployed S3 + qm empty -> not applicable",
			qm:   &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}},
			reader: func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
				return deployedDescriptor{host: h.ExternalIP}, nil // no bucket
			},
			want: ReconcileNotApplicable,
		},
		{
			name: "greenfield: qm set + foghorn NOT deployed -> complete",
			qm:   &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "auto", S3Prefix: "cell-a", S3PrefixPresent: true}},
			reader: func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ string) (deployedDescriptor, error) {
				return deployedDescriptor{}, fmt.Errorf("not deployed: %w", errFoghornNotDeployed)
			},
			want: ReconcileComplete,
		},
		{
			name: "greenfield but qm prefix NULL (incomplete) -> blocked",
			qm:   &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "auto", S3PrefixPresent: false}},
			reader: func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ string) (deployedDescriptor, error) {
				return deployedDescriptor{}, fmt.Errorf("not deployed: %w", errFoghornNotDeployed)
			},
			want: ReconcileBlocked,
		},
		{
			name: "qm set + deployed foghorn UNREACHABLE -> blocked (not assumed greenfield)",
			qm:   &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "auto", S3Prefix: "cell-a", S3PrefixPresent: true}},
			reader: func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ string) (deployedDescriptor, error) {
				return deployedDescriptor{}, errors.New("ssh: connection refused")
			},
			want: ReconcileBlocked,
		},
		{
			name:   "qm unreachable -> blocked",
			qm:     &fakeReconcileQM{getErr: errors.New("grpc: unavailable")},
			reader: agree,
			want:   ReconcileBlocked,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readDeployedFoghornDescriptorFn = tc.reader
			env := &reconcileEnv{rc: &resolvedCluster{Manifest: storageReconcileManifest()}, qm: tc.qm}
			got := tr.Check(context.Background(), env, scope)
			if got.Status != tc.want {
				t.Fatalf("want %q, got %q (%s)", tc.want, got.Status, got.Detail)
			}
		})
	}
}

func TestStorageAdoptionTransition_ApplyAndVerify(t *testing.T) {
	orig := readDeployedFoghornDescriptorFn
	defer func() { readDeployedFoghornDescriptorFn = orig }()
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{host: h.ExternalIP, bucket: "media-us", endpoint: "https://r2", region: "auto", prefix: "cell-a"}, nil
	}
	tr := storageDescriptorAdoption{}
	scope := ReconcileScope{ClusterID: "media-us"}
	qm := &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}}
	env := &reconcileEnv{cmd: &cobra.Command{}, rc: &resolvedCluster{Manifest: storageReconcileManifest()}, qm: qm, operatorJWT: "op-jwt"}

	if err := tr.Apply(context.Background(), env, scope); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(qm.adopted) != 5 || qm.adopted[1] != "media-us" || qm.adopted[4] != "cell-a" {
		t.Fatalf("adopt args: %v", qm.adopted)
	}
	if err := tr.Verify(context.Background(), env, scope); err != nil {
		t.Fatalf("verify after apply: %v", err)
	}

	// Apply without an operator JWT is refused.
	env.operatorJWT = ""
	if err := tr.Apply(context.Background(), env, scope); err == nil {
		t.Fatalf("apply without operator JWT must be refused")
	}
}

// TestRunReleaseTransition_StorageAdoptionResumable is the command-level integration test: it drives
// runReleaseTransition end to end over the seams for a legacy cell (empty QM, prefix NULL), multiple replicas, a
// simulated temporary-GitOps-checkout discard, and a successful resume that repairs the manifest from the authority.
func TestRunReleaseTransition_StorageAdoptionResumable(t *testing.T) {
	orig := readDeployedFoghornDescriptorFn
	defer func() { readDeployedFoghornDescriptorFn = orig }()
	// Two replicas that agree on the deployed backend.
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{host: h.ExternalIP, bucket: "media-us", endpoint: "https://r2", region: "auto", prefix: "cell-a"}, nil
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	m := storageReconcileManifest()
	if err := inventory.Save(path, m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	rc := &resolvedCluster{Manifest: m, ManifestPath: path}
	qm := &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}} // legacy: empty QM row
	env := &reconcileEnv{cmd: &cobra.Command{}, rc: rc, qm: qm, operatorJWT: "op-jwt"}
	tr := storageDescriptorAdoption{}

	// First run: Pending → Apply → adopt into QM + record on the manifest.
	if err := runReleaseTransition(env.cmd, env, tr, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if len(qm.adopted) != 5 || qm.adopted[1] != "media-us" {
		t.Fatalf("expected adopt, got %v", qm.adopted)
	}
	if rc.Manifest.Clusters["media-us"].S3Bucket != "media-us" {
		t.Fatalf("manifest descriptor not recorded after apply")
	}

	// Simulate a temporary/GitOps checkout whose manifest write was discarded: the descriptor is gone from the manifest
	// even though QM (the authority) still has it.
	cc := rc.Manifest.Clusters["media-us"]
	cc.S3Bucket, cc.S3Endpoint, cc.S3Region, cc.S3Prefix = "", "", "", nil
	rc.Manifest.Clusters["media-us"] = cc

	// Resume: Check is now Complete (QM already adopted), but ConvergeSideState must repair the stale manifest so the
	// downstream manifest-based Foghorn gate agrees.
	qm.adopted = nil
	if err := runReleaseTransition(env.cmd, env, tr, false); err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if qm.adopted != nil {
		t.Fatalf("resume must NOT re-adopt (idempotent), got %v", qm.adopted)
	}
	if rc.Manifest.Clusters["media-us"].S3Bucket != "media-us" {
		t.Fatalf("resume did not repair the stale manifest from the authority")
	}

	// Dry-run of a Pending transition without an operator JWT must fail the same way the real run would: dry-run still
	// runs every gate. Fresh empty QM → Pending.
	envNoJWT := &reconcileEnv{cmd: &cobra.Command{}, rc: rc, qm: &fakeReconcileQM{cluster: &quartermasterpb.InfrastructureCluster{}}, operatorJWT: ""}
	if err := runReleaseTransition(envNoJWT.cmd, envNoJWT, tr, true); err == nil {
		t.Fatalf("dry-run of a pending transition without an operator JWT must error")
	}
}
