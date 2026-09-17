package handlers

import (
	"context"
	"math"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

type viewerPlacementFunc func(context.Context, control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error)

const viewerOutputsJSON = `{"HTTP":"https://us.example/$.html","WebRTC":"https://us.example/webrtc/$","WHEP":"https://us.example/whep/$","HLS":"https://us.example/hls/$/index.m3u8","DASH":"https://us.example/cmaf/$/index.mpd","HLS (CMAF)":"https://us.example/cmaf/$/index.m3u8"}`

func (fn viewerPlacementFunc) PrepareViewer(ctx context.Context, req control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, req)
}

func TestHTTPViewerPlacementProtocolPreservesExplicitFormat(t *testing.T) {
	for raw, want := range map[string]string{"WHEP": "whep", "wswebrtc": "webrtc", "mews": "wsmp4", "m3u8": "hls", "mpd": "dash", "hss": "smoothstreaming", "ll-hls": "cmaf", "any": "", "all": "", "": ""} {
		got, err := requestedViewerPlacementProtocol(raw)
		if err != nil || got != want {
			t.Errorf("protocol %q: %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := requestedViewerPlacementProtocol("unknown"); err == nil {
		t.Fatal("unknown protocol became automatic negotiation")
	}
}

func TestHTTPViewerUsesPreparedDestinationWithoutLegacyBalancer(t *testing.T) {
	previous := viewerPlacementPreparer
	t.Cleanup(func() { viewerPlacementPreparer = previous })
	calls := 0
	SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
		calls++
		if request.TenantID != "tenant" || request.StreamID != "stream" || request.InternalName != "internal" || request.Protocol != "hls" || request.Location != nil {
			t.Fatal("HTTP viewer context differs")
		}
		now := time.Now()
		attempt, err := placement.NewPreparationAttemptID(now)
		if err != nil {
			t.Fatal(err)
		}
		return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: "tenant", ObjectID: sharedauthority.LiveStreamAuthorityID("stream"), SourceGeneration: "generation", NodeID: "us-edge", ClusterID: "us", Protocol: "hls", Endpoint: "https://us.example/hls/public/index.m3u8", PublicBaseURL: "https://us.example", OutputsJSON: viewerOutputsJSON, AttemptID: attempt, ExpiresAt: now.Add(time.Second)}, nil
	}))
	request := &sharedpb.ViewerEndpointRequest{ContentId: "public", Protocol: "hls"}
	response, err := resolveLiveViewerEndpoint(context.Background(), request, math.NaN(), math.NaN(), "live+internal", "tenant", "stream", "eu", nil, "eu", "", false)
	if err != nil || response.Primary.NodeId != "us-edge" || calls != 1 || len(response.Fallbacks) != 0 {
		t.Fatalf("HTTP prepared routing: %v, %v", response, err)
	}
	SetViewerPlacementPreparer(nil)
	if response, err = resolveLiveViewerEndpoint(context.Background(), request, 0, 0, "internal", "tenant", "stream", "eu", nil, "eu", "", false); err == nil || response != nil {
		t.Fatal("cleared viewer adapter restored legacy routing")
	}
}
