package control

import (
	"context"
	"os"
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

// setMockValidator sets the package-level validateBootstrapTokenFn for tests
// and returns a cleanup function that restores it to nil.
func setMockValidator(t *testing.T, resp *pb.ValidateBootstrapTokenResponse) {
	t.Helper()
	validateBootstrapTokenFn = func(_ context.Context, _ string) (*pb.ValidateBootstrapTokenResponse, error) {
		return resp, nil
	}
	t.Cleanup(func() { validateBootstrapTokenFn = nil })
}

func TestPreRegisterEdge_ValidToken(t *testing.T) {
	t.Setenv("CLUSTER_ID", "us_west_1")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")

	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid:     true,
		Kind:      "edge_node",
		ClusterId: "us_west_1",
	})

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_validtoken",
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

	if resp.ClusterSlug != "us-west-1" {
		t.Errorf("expected cluster_slug %q, got %q", "us-west-1", resp.ClusterSlug)
	}

	if resp.ClusterId != "us_west_1" {
		t.Errorf("expected cluster_id %q, got %q", "us_west_1", resp.ClusterId)
	}

	expectedEdge := "edge-" + resp.NodeId + ".us-west-1.example.com"
	if resp.EdgeDomain != expectedEdge {
		t.Errorf("expected edge_domain %q, got %q", expectedEdge, resp.EdgeDomain)
	}

	if resp.PoolDomain != "edge.us-west-1.example.com" {
		t.Errorf("expected pool_domain %q, got %q", "edge.us-west-1.example.com", resp.PoolDomain)
	}

	if resp.FoghornGrpcAddr != "foghorn.us-west-1.example.com:18008" {
		t.Errorf("expected foghorn_grpc_addr %q, got %q", "foghorn.us-west-1.example.com:18008", resp.FoghornGrpcAddr)
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

func TestPreRegisterEdge_InvalidToken(t *testing.T) {
	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid:  false,
		Reason: "not_found",
	})

	srv := &EdgeProvisioningServer{}
	_, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if !strings.Contains(err.Error(), "invalid enrollment token") {
		t.Errorf("expected 'invalid enrollment token' error, got: %v", err)
	}
}

func TestPreRegisterEdge_WrongTokenKind(t *testing.T) {
	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid: true,
		Kind:  "service",
	})

	srv := &EdgeProvisioningServer{}
	_, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_servicetoken",
	})
	if err == nil {
		t.Fatal("expected error for wrong token kind")
	}
	if !strings.Contains(err.Error(), "not valid for edge enrollment") {
		t.Errorf("expected 'not valid for edge enrollment' error, got: %v", err)
	}
}

func TestPreRegisterEdge_NoValidatorNoQM(t *testing.T) {
	// Both validateBootstrapTokenFn and quartermasterClient are nil
	validateBootstrapTokenFn = nil
	SetQuartermasterClient(nil)
	t.Cleanup(func() { validateBootstrapTokenFn = nil })

	srv := &EdgeProvisioningServer{}
	_, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_anytoken",
	})
	if err == nil {
		t.Fatal("expected error when QM client unavailable")
	}
	if !strings.Contains(err.Error(), "enrollment service unavailable") {
		t.Errorf("expected 'enrollment service unavailable' error, got: %v", err)
	}
}

func TestPreRegisterEdge_DefaultCluster(t *testing.T) {
	os.Unsetenv("CLUSTER_ID")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "frameworks.network")

	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid: true,
		Kind:  "edge_node",
	})

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_nobound",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ClusterSlug != "default" {
		t.Errorf("expected cluster_slug %q, got %q", "default", resp.ClusterSlug)
	}
	if resp.ClusterId != "default" {
		t.Errorf("expected cluster_id %q, got %q", "default", resp.ClusterId)
	}

	if !strings.HasSuffix(resp.EdgeDomain, ".default.frameworks.network") {
		t.Errorf("expected edge_domain to end with .default.frameworks.network, got %q", resp.EdgeDomain)
	}
}

func TestPreRegisterEdge_TokenBoundCluster(t *testing.T) {
	t.Setenv("CLUSTER_ID", "env-cluster")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")

	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid:     true,
		Kind:      "edge_node",
		ClusterId: "token-bound-cluster",
	})

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
		EnrollmentToken: "bt_bound",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ClusterId != "token-bound-cluster" {
		t.Errorf("expected cluster_id %q, got %q", "token-bound-cluster", resp.ClusterId)
	}
}

func TestPreRegisterEdge_UniqueNodeIDs(t *testing.T) {
	t.Setenv("CLUSTER_ID", "test")
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")

	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid: true,
		Kind:  "edge_node",
	})

	srv := &EdgeProvisioningServer{}
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{
			EnrollmentToken: "bt_unique",
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

func TestPreRegisterEdge_EmptySanitizedClusterSlugFallsBackToDefault(t *testing.T) {
	t.Setenv("NAVIGATOR_ROOT_DOMAIN", "example.com")

	setMockValidator(t, &pb.ValidateBootstrapTokenResponse{
		Valid:     true,
		Kind:      "edge_node",
		ClusterId: "___",
	})

	srv := &EdgeProvisioningServer{}
	resp, err := srv.PreRegisterEdge(context.Background(), &pb.PreRegisterEdgeRequest{EnrollmentToken: "bt_slug"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ClusterSlug != "default" {
		t.Fatalf("expected default cluster slug, got %q", resp.ClusterSlug)
	}
	if !strings.Contains(resp.FoghornGrpcAddr, "foghorn.default.example.com:18008") {
		t.Fatalf("expected fallback foghorn addr with default slug, got %q", resp.FoghornGrpcAddr)
	}
}
