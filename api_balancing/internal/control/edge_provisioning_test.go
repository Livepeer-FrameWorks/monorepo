package control

import (
	"context"
	"os"
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

func TestPreRegisterEdge_ValidToken(t *testing.T) {
	t.Setenv("CLUSTER_ID", "us_west_1")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")
	t.Setenv("FOGHORN_EXTERNAL_ADDR", "foghorn.example.com:18008")

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "tok-abc-123",
		ExternalIp:      "1.2.3.4",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.NodeId == "" {
		t.Fatal("expected non-empty node_id")
	}
	if len(resp.NodeId) != 12 {
		t.Errorf("expected 12-char hex node_id, got %q (len %d)", resp.NodeId, len(resp.NodeId))
	}

	// Cluster slug should sanitize underscores to hyphens
	if resp.ClusterSlug != "us-west-1" {
		t.Errorf("expected cluster_slug %q, got %q", "us-west-1", resp.ClusterSlug)
	}

	expectedEdge := "edge-" + resp.NodeId + ".us-west-1.example.com"
	if resp.EdgeDomain != expectedEdge {
		t.Errorf("expected edge_domain %q, got %q", expectedEdge, resp.EdgeDomain)
	}

	if resp.PoolDomain != "edge.us-west-1.example.com" {
		t.Errorf("expected pool_domain %q, got %q", "edge.us-west-1.example.com", resp.PoolDomain)
	}

	if resp.FoghornGrpcAddr != "foghorn.example.com:18008" {
		t.Errorf("expected foghorn_grpc_addr %q, got %q", "foghorn.example.com:18008", resp.FoghornGrpcAddr)
	}
}

func TestPreRegisterEdge_EmptyToken(t *testing.T) {
	srv := &EdgeProvisioningServer{}
	_, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "",
	})
	if err == nil {
		t.Fatal("expected error for empty token")
	}
	if !strings.Contains(err.Error(), "enrollment_token is required") {
		t.Errorf("expected enrollment_token error, got: %v", err)
	}
}

func TestPreRegisterEdge_WhitespaceOnlyToken(t *testing.T) {
	srv := &EdgeProvisioningServer{}
	_, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "   ",
	})
	if err == nil {
		t.Fatal("expected error for whitespace-only token")
	}
}

func TestPreRegisterEdge_DefaultCluster(t *testing.T) {
	// Unset CLUSTER_ID to test fallback
	os.Unsetenv("CLUSTER_ID")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "frameworks.network")

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "tok-xyz",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ClusterSlug != "default" {
		t.Errorf("expected cluster_slug %q, got %q", "default", resp.ClusterSlug)
	}

	if !strings.HasSuffix(resp.EdgeDomain, ".default.frameworks.network") {
		t.Errorf("expected edge_domain to end with .default.frameworks.network, got %q", resp.EdgeDomain)
	}
}

func TestPreRegisterEdge_UniqueNodeIDs(t *testing.T) {
	t.Setenv("CLUSTER_ID", "test")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")

	srv := &EdgeProvisioningServer{}
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
			EnrollmentToken: "tok",
		})
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if seen[resp.NodeId] {
			t.Fatalf("duplicate node_id %q on iteration %d", resp.NodeId, i)
		}
		seen[resp.NodeId] = true
	}
}
