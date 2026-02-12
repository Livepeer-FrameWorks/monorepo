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
	}, "node-1", "203.0.113.5:443", "tok-1", "cluster-a")

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
}

func TestBuildBootstrapEdgeNodeRequest_UsesPeerAddressWhenForwardedMissing(t *testing.T) {
	req := buildBootstrapEdgeNodeRequest(context.Background(), nil, "node-1", "203.0.113.5:443", "tok-1", "")

	if req.GetTargetClusterId() != "" {
		t.Fatalf("expected empty target cluster, got %q", req.GetTargetClusterId())
	}
	if len(req.GetIps()) != 1 || req.GetIps()[0] != "203.0.113.5" {
		t.Fatalf("expected peer host IP, got %+v", req.GetIps())
	}
}
