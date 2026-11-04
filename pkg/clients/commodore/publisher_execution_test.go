package commodore

import (
	"context"
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type executionProtocolClient struct {
	pb.InternalServiceClient
	validation *pb.ValidateStreamKeyResponse
	renewal    *pb.SyncActiveIngestPlacementResponse
	request    *pb.ValidateStreamKeyRequest
}

func (c *executionProtocolClient) ValidateStreamKey(_ context.Context, req *pb.ValidateStreamKeyRequest, _ ...grpc.CallOption) (*pb.ValidateStreamKeyResponse, error) {
	c.request = req
	return c.validation, nil
}
func (c *executionProtocolClient) SyncActiveIngestPlacement(context.Context, *pb.SyncActiveIngestPlacementRequest, ...grpc.CallOption) (*pb.SyncActiveIngestPlacementResponse, error) {
	return c.renewal, nil
}
func (c *executionProtocolClient) RenewPublisherLeases(context.Context, *pb.SyncActiveIngestPlacementRequest, ...grpc.CallOption) (*pb.SyncActiveIngestPlacementResponse, error) {
	return c.renewal, nil
}

func TestPublisherExecutionClientRequiresServerFencingAcknowledgement(t *testing.T) {
	transport := &executionProtocolClient{validation: &pb.ValidateStreamKeyResponse{Valid: true}, renewal: &pb.SyncActiveIngestPlacementResponse{Renewed: 1}}
	client := &GRPCClient{internal: transport}
	if _, err := client.ValidateStreamKeyForPublisherClaim(t.Context(), "credential", "cluster", "execution", "node", "cell"); status.Code(err) != codes.Unimplemented {
		t.Fatalf("cold claim accepted an old server ignoring execution metadata: %v", err)
	}
	if transport.request.GetNodeId() != "node" || transport.request.GetControlCellId() != "cell" || transport.request.GetClaimToken() != "execution" {
		t.Fatal("cold claim lost execution identity")
	}
	entry := &pb.ActiveIngestStream{TenantId: "tenant", InternalName: "stream", ClusterId: "cluster", ClaimToken: "execution", NodeId: "node", ControlCellId: "cell"}
	if _, err := client.SyncActiveIngestPlacement(t.Context(), "", []*pb.ActiveIngestStream{entry}, nil); status.Code(err) != codes.Unimplemented {
		t.Fatalf("outage renewal accepted an unfenced server: %v", err)
	}
	if _, err := client.RenewPublisherLeases(t.Context(), []*pb.ActiveIngestStream{entry}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("strict renewal accepted an unfenced server: %v", err)
	}
	if _, err := client.SyncActiveIngestPlacement(t.Context(), "", nil, []*pb.ActiveIngestStream{entry}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("terminal release accepted an unfenced server: %v", err)
	}
	transport.validation.PublisherExecutionFenced = true
	transport.renewal.ExecutionFencesApplied = true
	if _, err := client.ValidateStreamKeyForPublisherClaim(t.Context(), "credential", "cluster", "execution", "node", "cell"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SyncActiveIngestPlacement(t.Context(), "", []*pb.ActiveIngestStream{entry}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RenewPublisherLeases(t.Context(), []*pb.ActiveIngestStream{entry}); err != nil {
		t.Fatal(err)
	}
}
