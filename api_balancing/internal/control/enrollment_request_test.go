package control

import (
	"context"
	"testing"

	pb "frameworks/pkg/proto"
	"google.golang.org/grpc/metadata"
)

func TestBuildBootstrapEdgeNodeRequest_IncludesTargetClusterAndFingerprint(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-forwarded-for", "198.51.100.10, 10.0.0.1"))
	macs := "macs-hash"
	machine := "machine-hash"

	req := buildBootstrapEdgeNodeRequest(ctx, &pb.Register{
		Fingerprint: &pb.NodeFingerprint{
			LocalIpv4:       []string{"10.0.0.2"},
			LocalIpv6:       []string{"2001:db8::2"},
			MacsSha256:      &macs,
			MachineIdSha256: &machine,
		},
	}, "node-1", "203.0.113.5:443", "tok-1", "cluster-a", []string{"cluster-a", "cluster-b"})

	if req.GetTargetClusterId() != "cluster-a" {
		t.Fatalf("expected target cluster cluster-a, got %q", req.GetTargetClusterId())
	}
	if len(req.GetIps()) != 1 || req.GetIps()[0] != "198.51.100.10" {
		t.Fatalf("expected forwarded IP to be used, got %+v", req.GetIps())
	}
	if req.GetMacsSha256() != macs {
		t.Fatalf("expected macs hash %q, got %q", macs, req.GetMacsSha256())
	}
	if req.GetMachineIdSha256() != machine {
		t.Fatalf("expected machine hash %q, got %q", machine, req.GetMachineIdSha256())
	}
	served := req.GetServedClusterIds()
	if len(served) != 2 || served[0] != "cluster-a" || served[1] != "cluster-b" {
		t.Fatalf("expected served clusters [cluster-a cluster-b], got %v", served)
	}
}

func TestBuildBootstrapEdgeNodeRequest_UsesPeerAddressWhenForwardedMissing(t *testing.T) {
	req := buildBootstrapEdgeNodeRequest(context.Background(), nil, "node-1", "203.0.113.5:443", "tok-1", "", nil)

	if req.GetTargetClusterId() != "" {
		t.Fatalf("expected empty target cluster, got %q", req.GetTargetClusterId())
	}
	if len(req.GetIps()) != 1 || req.GetIps()[0] != "203.0.113.5" {
		t.Fatalf("expected peer host IP, got %+v", req.GetIps())
	}
	if len(req.GetServedClusterIds()) != 0 {
		t.Fatalf("expected no served clusters, got %v", req.GetServedClusterIds())
	}
}
