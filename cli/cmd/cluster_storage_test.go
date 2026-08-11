package cmd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/spf13/cobra"
)

func descHosts(names ...string) []inventory.Host {
	hosts := make([]inventory.Host, 0, len(names))
	for _, n := range names {
		hosts = append(hosts, inventory.Host{Name: n, ExternalIP: n})
	}
	return hosts
}

// deployedDescriptorConsensus requires every replica to agree; a split cell is refused, and a cell running without S3
// is refused (nothing to adopt).
func TestDeployedDescriptorConsensus(t *testing.T) {
	orig := readDeployedFoghornDescriptorFn
	defer func() { readDeployedFoghornDescriptorFn = orig }()

	// All replicas agree → consensus returned (region compared on effective value).
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{host: h.ExternalIP, bucket: "media-us", endpoint: "https://r2", region: "", prefix: "cell-a"}, nil
	}
	got, gErr := deployedDescriptorConsensus(context.Background(), nil, descHosts("a", "b"))
	if gErr != nil {
		t.Fatalf("agreeing replicas must yield consensus, got: %v", gErr)
	}
	if got.bucket != "media-us" || got.prefix != "cell-a" {
		t.Fatalf("unexpected consensus: %+v", got)
	}

	// Disagreeing replicas → refused.
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		if h.ExternalIP == "b" {
			return deployedDescriptor{host: "b", bucket: "media-us-2", endpoint: "https://r2", prefix: "cell-a"}, nil
		}
		return deployedDescriptor{host: h.ExternalIP, bucket: "media-us", endpoint: "https://r2", prefix: "cell-a"}, nil
	}
	if _, err := deployedDescriptorConsensus(context.Background(), nil, descHosts("a", "b")); err == nil {
		t.Fatalf("disagreeing replicas must be refused")
	}

	// No deployed bucket → refused (S3 not configured).
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{host: h.ExternalIP}, nil
	}
	if _, err := deployedDescriptorConsensus(context.Background(), nil, descHosts("a")); err == nil {
		t.Fatalf("a Foghorn with no deployed bucket must be refused")
	}

	// A replica read error → propagated.
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{}, errors.New("ssh refused")
	}
	if _, err := deployedDescriptorConsensus(context.Background(), nil, descHosts("a")); err == nil {
		t.Fatalf("a replica read error must propagate")
	}

	// PARTIAL HA: host "a" is NOT deployed, host "b" is live with S3 → consensus must come from the live replica, NOT
	// a false greenfield/NotApplicable from reading the absent host first.
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		if h.ExternalIP == "a" {
			return deployedDescriptor{}, fmt.Errorf("absent: %w", errFoghornNotDeployed)
		}
		return deployedDescriptor{host: "b", bucket: "media-us", endpoint: "https://r2", prefix: "cell-a"}, nil
	}
	got, gErr = deployedDescriptorConsensus(context.Background(), nil, descHosts("a", "b"))
	if gErr != nil || got.bucket != "media-us" {
		t.Fatalf("partial-HA (one absent, one live) must yield the live replica's consensus, got %+v err=%v", got, gErr)
	}

	// ALL replicas absent → errFoghornNotDeployed (greenfield).
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, _ inventory.Host, _ string) (deployedDescriptor, error) {
		return deployedDescriptor{}, fmt.Errorf("absent: %w", errFoghornNotDeployed)
	}
	if _, err := deployedDescriptorConsensus(context.Background(), nil, descHosts("a", "b")); !errors.Is(err, errFoghornNotDeployed) {
		t.Fatalf("all-absent must return errFoghornNotDeployed, got %v", err)
	}

	// One deployed-but-UNREACHABLE replica → error (Blocked upstream), not a false greenfield.
	readDeployedFoghornDescriptorFn = func(_ context.Context, _ *ssh.Pool, h inventory.Host, _ string) (deployedDescriptor, error) {
		if h.ExternalIP == "a" {
			return deployedDescriptor{}, fmt.Errorf("absent: %w", errFoghornNotDeployed)
		}
		return deployedDescriptor{}, errors.New("ssh: connection refused")
	}
	if _, err := deployedDescriptorConsensus(context.Background(), nil, descHosts("a", "b")); err == nil || errors.Is(err, errFoghornNotDeployed) {
		t.Fatalf("an unreachable deployed replica must error (not greenfield), got %v", err)
	}
}

// parseDeployedS3Env handles both docker-inspect env lines (KEY=val) and native systemd EnvironmentFile lines
// (KEY="val", optionally export-prefixed).
func TestParseDeployedS3Env(t *testing.T) {
	docker := "PATH=/usr/bin\nSTORAGE_S3_BUCKET=media-us\nSTORAGE_S3_ENDPOINT=https://r2\nSTORAGE_S3_REGION=auto\nSTORAGE_S3_PREFIX=cell-a\n"
	d := parseDeployedS3Env("h1", docker)
	if d.bucket != "media-us" || d.endpoint != "https://r2" || d.region != "auto" || d.prefix != "cell-a" {
		t.Fatalf("docker parse: %+v", d)
	}

	native := "export STORAGE_S3_BUCKET=\"media-eu\"\nSTORAGE_S3_ENDPOINT='https://eu.example'\nSTORAGE_S3_REGION=\nSTORAGE_S3_PREFIX=\"cell-b\"\n# comment\n"
	n := parseDeployedS3Env("h2", native)
	if n.bucket != "media-eu" || n.endpoint != "https://eu.example" || n.region != "" || n.prefix != "cell-b" {
		t.Fatalf("native parse: %+v", n)
	}
}

type fakeAdoptQM struct {
	resp    *quartermasterpb.ClusterResponse
	err     error
	gotArgs []string
}

func (f *fakeAdoptQM) AdoptClusterStorageDescriptor(_ context.Context, clusterID, bucket, endpoint, region, prefix string) (*quartermasterpb.ClusterResponse, error) {
	f.gotArgs = []string{clusterID, bucket, endpoint, region, prefix}
	return f.resp, f.err
}

// adoptDeployedDescriptor sends the deployed tuple to Quartermaster and verifies the read-back.
func TestAdoptDeployedDescriptor(t *testing.T) {
	cmd := &cobra.Command{}
	d := deployedDescriptor{host: "a", bucket: "media-us", endpoint: "https://r2", region: "", prefix: "cell-a"}

	// Read-back matches (QM echoes us-east-1 for the empty region) → success.
	ok := &fakeAdoptQM{resp: &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{
		S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "us-east-1", S3Prefix: "cell-a",
	}}}
	if err := adoptDeployedDescriptor(context.Background(), cmd, ok, "media-us", d); err != nil {
		t.Fatalf("matching read-back must succeed, got: %v", err)
	}
	if ok.gotArgs[1] != "media-us" || ok.gotArgs[4] != "cell-a" {
		t.Fatalf("wrong args sent to QM: %v", ok.gotArgs)
	}

	// RPC error (e.g. immutable repoint) → propagated.
	bad := &fakeAdoptQM{err: errors.New("s3 descriptor is immutable once set")}
	if err := adoptDeployedDescriptor(context.Background(), cmd, bad, "media-us", d); err == nil {
		t.Fatalf("RPC error must propagate")
	}

	// Read-back diverges from what we adopted → error (the write did not land as expected).
	drift := &fakeAdoptQM{resp: &quartermasterpb.ClusterResponse{Cluster: &quartermasterpb.InfrastructureCluster{
		S3Bucket: "media-us", S3Endpoint: "https://r2", S3Region: "us-east-1", S3Prefix: "OTHER",
	}}}
	if err := adoptDeployedDescriptor(context.Background(), cmd, drift, "media-us", d); err == nil {
		t.Fatalf("read-back mismatch must error")
	}
}
