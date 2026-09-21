package quartermaster

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type recordingMaterializationClient struct {
	quartermasterpb.ClusterServiceClient
	materialize *quartermasterpb.MaterializeClusterAccessRequest
	revoke      *quartermasterpb.RevokeMaterializedClusterAccessRequest
}

func (r *recordingMaterializationClient) MaterializeClusterAccess(_ context.Context, req *quartermasterpb.MaterializeClusterAccessRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	r.materialize = req
	return &emptypb.Empty{}, nil
}

func (r *recordingMaterializationClient) RevokeMaterializedClusterAccess(_ context.Context, req *quartermasterpb.RevokeMaterializedClusterAccessRequest, _ ...grpc.CallOption) (*emptypb.Empty, error) {
	r.revoke = req
	return &emptypb.Empty{}, nil
}

func TestClusterAccessProofsUseConfiguredSecret(t *testing.T) {
	t.Setenv("CLUSTER_ACCESS_MATERIALIZATION_SECRET", "ambient-secret")
	const secret = "configured-secret"
	rpc := &recordingMaterializationClient{}
	c := &GRPCClient{cluster: rpc, clusterAccessMaterializationSecret: secret}

	if err := c.MaterializeClusterAccess(context.Background(), &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: "tenant-a", ClusterId: "cluster-a", AuthorizationReference: "sub-1",
	}); err != nil {
		t.Fatalf("MaterializeClusterAccess: %v", err)
	}
	m := rpc.materialize
	want, err := auth.MintClusterAccessMaterializationProof(secret, m.GetTenantId(), m.GetClusterId(), int32(m.GetAccessSource()), m.GetAuthorizationReference(), m.GetSubscriptionStatus(), m.GetAuthorizedAt().AsTime())
	if err != nil || m.GetAuthorizationProof() != want {
		t.Fatalf("materialization proof not signed with the configured secret (err=%v)", err)
	}

	if revokeErr := c.RevokeMaterializedClusterAccess(context.Background(), &quartermasterpb.RevokeMaterializedClusterAccessRequest{
		TenantId: "tenant-a", ClusterId: "cluster-a", AuthorizationReference: "sub-1",
	}); revokeErr != nil {
		t.Fatalf("RevokeMaterializedClusterAccess: %v", revokeErr)
	}
	r := rpc.revoke
	wantRevoke, revokeErr := auth.MintClusterAccessRevocationProof(secret, r.GetTenantId(), r.GetClusterId(), int32(r.GetAccessSource()), r.GetAuthorizationReference(), r.GetAuthorizedAt().AsTime())
	if revokeErr != nil || r.GetAuthorizationProof() != wantRevoke {
		t.Fatalf("revocation proof not signed with the configured secret (err=%v)", revokeErr)
	}
}

func TestClusterAccessProofsRequireSecretBeforeRPC(t *testing.T) {
	t.Setenv("CLUSTER_ACCESS_MATERIALIZATION_SECRET", "ambient-secret")
	rpc := &recordingMaterializationClient{}
	c := &GRPCClient{cluster: rpc}

	err := c.MaterializeClusterAccess(context.Background(), &quartermasterpb.MaterializeClusterAccessRequest{TenantId: "tenant-a", ClusterId: "cluster-a"})
	if !errors.Is(err, auth.ErrClusterAccessProofMissing) {
		t.Fatalf("MaterializeClusterAccess error = %v, want ErrClusterAccessProofMissing", err)
	}
	err = c.RevokeMaterializedClusterAccess(context.Background(), &quartermasterpb.RevokeMaterializedClusterAccessRequest{TenantId: "tenant-a", ClusterId: "cluster-a"})
	if !errors.Is(err, auth.ErrClusterAccessProofMissing) {
		t.Fatalf("RevokeMaterializedClusterAccess error = %v, want ErrClusterAccessProofMissing", err)
	}
	if rpc.materialize != nil || rpc.revoke != nil {
		t.Fatal("an RPC was sent without a materialization secret")
	}
}
