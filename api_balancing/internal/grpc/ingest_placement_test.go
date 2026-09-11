package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ingestPlacementFunc func(context.Context, control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error)

func (fn ingestPlacementFunc) PrepareIngest(ctx context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, request)
}

func TestResolveIngestEndpointPreparedPathCannotFallBackToLegacyNode(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			seedGRPCIngestNode(t, sm, "legacy-node", "legacy.example:18090")
			fake := &commodoreIngestFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "push", ClusterPeers: ingestTestPeers()}, nil
			}}
			startCommodoreIngestFake(t, fake)
			server := newIngestGRPCServer()
			calls := 0
			server.SetIngestPlacementPreparer(ingestPlacementFunc(func(_ context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				if request.TenantID != "tenant" || request.StreamID != "stream" || request.InternalName != "internal" || request.Protocol != "srt" {
					t.Fatal("gRPC lost exact identity")
				}
				if outcome == "failed" {
					return balancer.PlacementPreparationResult{}, errors.New("policy denied")
				}
				now := time.Now()
				attempt, err := placement.NewPreparationAttemptID(now)
				return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: request.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID),
					NodeID: "prepared-node", ClusterID: "prepared-cluster", Protocol: "srt", PublicBaseURL: "https://prepared.example:8443",
					Endpoint: "srt://prepared.example:9889?streamid=$", AttemptID: attempt, ExpiresAt: now.Add(10 * time.Second)}, err
			}))
			if outcome == "missing" {
				server.SetIngestPlacementPreparer(nil)
			}
			response, err := server.ResolveIngestEndpoint(t.Context(), &sharedpb.IngestEndpointRequest{StreamKey: "private-key", Protocol: sharedpb.IngestProtocol_INGEST_PROTOCOL_SRT})
			if outcome == "accepted" {
				if err != nil || response.Primary.NodeId != "prepared-node" || response.Primary.GetSrtUrl() != "srt://prepared.example:9889?streamid=private-key" || response.Primary.GetWhipUrl() != "" || calls != 1 {
					t.Fatalf("gRPC did not use prepared endpoint: %v, %v", response, err)
				}
			} else if status.Code(err) != codes.Unavailable || response != nil {
				t.Fatalf("gRPC escaped policy path: %v, %v", response, err)
			}
			if fake.validateKeyHits.Load() != 0 {
				t.Fatal("prepared resolution claimed ownership")
			}
		})
	}
}
