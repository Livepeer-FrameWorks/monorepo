package grpc

import (
	"context"
	"math"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/triggers"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type viewerPlacementFunc func(context.Context, control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error)

const viewerOutputsJSON = `{"HTTP":"https://us.example/$.html","WebRTC":"https://us.example/webrtc/$","WHEP":"https://us.example/whep/$","HLS":"https://us.example/hls/$/index.m3u8","DASH":"https://us.example/cmaf/$/index.mpd","HLS (CMAF)":"https://us.example/cmaf/$/index.m3u8"}`

func (fn viewerPlacementFunc) PrepareViewer(ctx context.Context, req control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, req)
}

func TestGRPCPreparedViewerDoesNotUseCoordinatorMetadata(t *testing.T) {
	t.Cleanup(control.SetupTestRegistry("", nil))
	sm, _ := newViewerHappyManager(t)
	if err := sm.UpdateStreamFromBuffer("live+internal", "internal", "eu-edge", "tenant", "DRY", ""); err != nil {
		t.Fatal(err)
	}
	startViewerHappyCommodoreFake(t, &commodoreViewerHappyFake{
		playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
			return &commodorepb.ResolvePlaybackIDResponse{InternalName: "internal", TenantId: "tenant", StreamId: "stream"}, nil
		},
	})
	s := &FoghornGRPCServer{logger: logrus.New(), cacheInvalidator: &billingCacheViewerHappy{status: &triggers.BillingStatus{TenantID: "tenant", BillingModel: "postpaid"}}}
	s.SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, req control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
		now := time.Now()
		attempt, err := placement.NewPreparationAttemptID(now)
		if err != nil {
			t.Fatal(err)
		}
		return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: req.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(req.StreamID), SourceGeneration: "generation", NodeID: "us-edge", ClusterID: "us", Protocol: req.Protocol, Endpoint: "https://us.example/hls/public/index.m3u8", PublicBaseURL: "https://us.example", OutputsJSON: viewerOutputsJSON, AttemptID: attempt, ExpiresAt: now.Add(time.Second)}, nil
	}))
	response, err := s.ResolveViewerEndpoint(t.Context(), &sharedpb.ViewerEndpointRequest{ContentId: "public", Protocol: "hls"})
	if err != nil || response.GetPrimary().GetNodeId() != "us-edge" || response.GetMetadata().GetBufferState() != "" || response.GetMetadata().GetStatus() != "live" {
		t.Fatalf("coordinator state replaced prepared metadata: %v, %v", response, err)
	}
}

func TestGRPCViewerUsesPreparedDestinationWithoutLegacyBalancer(t *testing.T) {
	s := &FoghornGRPCServer{}
	calls := 0
	s.SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
		calls++
		if request.TenantID != "tenant" || request.StreamID != "stream" || request.InternalName != "internal" || request.Protocol != "hls" || request.Location != nil {
			t.Fatal("gRPC viewer context differs")
		}
		now := time.Now()
		attempt, err := placement.NewPreparationAttemptID(now)
		if err != nil {
			t.Fatal(err)
		}
		return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), SourceGeneration: "generation", NodeID: "us-edge", ClusterID: "us", Protocol: "hls", Endpoint: "https://us.example/hls/public/index.m3u8", PublicBaseURL: "https://us.example", OutputsJSON: viewerOutputsJSON, AttemptID: attempt, ExpiresAt: now.Add(time.Second)}, nil
	}))
	request := &sharedpb.ViewerEndpointRequest{ContentId: "public", Protocol: "hls"}
	response, err := s.resolveLiveViewerEndpoint(t.Context(), request, math.NaN(), math.NaN(), "live+internal", "tenant", "stream", "eu", nil, "eu", "", false)
	if err != nil || response.Primary.NodeId != "us-edge" || calls != 1 || len(response.Fallbacks) != 0 {
		t.Fatalf("gRPC prepared routing: %v, %v", response, err)
	}
	s.SetViewerPlacementPreparer(nil)
	if response, err = s.resolveLiveViewerEndpoint(t.Context(), request, 0, 0, "internal", "tenant", "stream", "eu", nil, "eu", "", false); status.Code(err) != codes.Unavailable || response != nil {
		t.Fatal("cleared viewer adapter restored legacy routing")
	}
}
