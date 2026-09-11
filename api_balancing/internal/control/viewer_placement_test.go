package control

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

type viewerPreparerFunc func(context.Context, ViewerPlacementRequest) (balancer.PlacementPreparationResult, error)

func (fn viewerPreparerFunc) PrepareViewer(ctx context.Context, req ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, req)
}

func preparedViewer(t *testing.T, request ViewerPlacementRequest) balancer.PlacementPreparationResult {
	t.Helper()
	now := time.Now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	scheme := "https"
	if validPreparedViewerScheme(request.Protocol, "wss") {
		scheme = "wss"
	}
	return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: request.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID),
		SourceGeneration: "generation", ClusterID: "us", NodeID: "us-edge", Protocol: request.Protocol, Endpoint: scheme + "://edge.example/media/" + request.PlaybackID,
		PublicBaseURL: "https://edge.example/media", AttemptID: attempt, ExpiresAt: now.Add(10 * time.Second)}
}

func TestPreparedViewerNormalizesFormatWithoutPreparingAnotherProtocol(t *testing.T) {
	for requested, canonical := range map[string]string{"WHEP": "whep", "wswebrtc": "webrtc", "mews": "wsmp4", "raw_ws": "raw_ws", "hss": "smoothstreaming", "hls_cmaf": "cmaf", "mkv": "mkv"} {
		calls := 0
		response, err := ResolvePreparedLiveViewerEndpoint(t.Context(), viewerPreparerFunc(func(_ context.Context, req ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
			calls++
			if req.Protocol != canonical {
				t.Errorf("requested %s prepared as %s", requested, req.Protocol)
			}
			return preparedViewer(t, req), nil
		}), ViewerPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "internal", PlaybackID: "public", Protocol: requested}, "")
		if err != nil || calls != 1 || response.GetPrimary().GetProtocol() != canonical {
			t.Fatalf("format %s: %v, %v, calls=%d", requested, response, err, calls)
		}
		for protocol, key := range map[string]string{"webrtc": "MIST_WEBRTC", "wsmp4": "MEWS", "cmaf": "HLS_CMAF", "mkv": "WEBM"} {
			if canonical == protocol && response.Primary.Outputs[key] == nil {
				t.Errorf("format %s lost player output key %s", canonical, key)
			}
		}
	}
}

func TestPreparedViewerNegotiatesOnlyUntilAccepted(t *testing.T) {
	for _, firstUnavailable := range []bool{false, true} {
		calls := 0
		response, err := ResolvePreparedLiveViewerEndpoint(context.Background(), viewerPreparerFunc(func(ctx context.Context, request ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
			calls++
			if request.InternalName != "internal" || request.Location != nil {
				t.Fatal("viewer identity/unknown geography changed")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
				t.Fatal("viewer preparation lost total deadline")
			}
			if firstUnavailable && request.Protocol == "webrtc" {
				return balancer.PlacementPreparationResult{}, balancer.ErrPlacementUnavailable
			}
			return preparedViewer(t, request), nil
		}), ViewerPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "live+internal", PlaybackID: "public"}, "")
		want, count := "webrtc", 1
		if firstUnavailable {
			want, count = "hls", 2
		}
		if err != nil || response.Primary.Protocol != want || calls != count || len(response.Fallbacks) != 0 || len(response.Primary.Outputs) != 1 || response.Primary.ClusterId != "us" || response.Primary.BaseUrl != "https://edge.example/media" {
			t.Fatalf("prepared viewer response: %v, %v, calls=%d", response, err, calls)
		}
	}
}

func TestPreparedViewerRejectsMismatchedOrAmbiguousOutcomes(t *testing.T) {
	for _, invalid := range []string{"error", "unavailable-explicit", "tenant", "object", "protocol", "generation", "expired", "url-scheme", "base", "refused", "node"} {
		t.Run(invalid, func(t *testing.T) {
			calls := 0
			response, err := ResolvePreparedLiveViewerEndpoint(context.Background(), viewerPreparerFunc(func(_ context.Context, request ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				result := preparedViewer(t, request)
				switch invalid {
				case "error":
					return result, errors.New("ambiguous preparation")
				case "unavailable-explicit":
					return result, balancer.ErrPlacementUnavailable
				case "tenant":
					result.TenantID = "foreign"
				case "object":
					result.ObjectID = "foreign"
				case "protocol":
					result.Protocol = "webrtc"
				case "generation":
					result.SourceGeneration = ""
				case "expired":
					result.ExpiresAt = time.Now().Add(-time.Second)
				case "url-scheme":
					result.Endpoint = "javascript://edge.example/public"
				case "base":
					result.PublicBaseURL = "https://user:secret@edge.example"
				case "refused":
					result.Outcome = balancer.PlacementFull
				case "node":
					result.NodeID = ""
				}
				return result, nil
			}), ViewerPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "internal", PlaybackID: "public", Protocol: "hls"}, "")
			if err == nil || response != nil || calls != 1 {
				t.Fatalf("invalid result returned or retried: %v, %v, calls=%d", response, err, calls)
			}
		})
	}
	if ViewerPlacementLocation(math.NaN(), 0) != nil || ViewerPlacementLocation(0, math.Inf(1)) != nil || ViewerPlacementLocation(91, 0) != nil || ViewerPlacementLocation(0, 0) == nil {
		t.Fatal("unknown/valid geography conflated")
	}
}

func TestPreparedViewerAcceptsEveryLiveRuntimeName(t *testing.T) {
	// A push session, a configured pull input and an in-progress recording all
	// reach this lane under their own Mist runtime name; each names the same
	// signed routing identity once the prefix is removed.
	for _, runtimeName := range []string{"internal", "live+internal", "pull+internal", "dvr+internal"} {
		observed := ""
		response, err := ResolvePreparedLiveViewerEndpoint(t.Context(), viewerPreparerFunc(func(_ context.Context, req ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
			observed = req.InternalName
			return preparedViewer(t, req), nil
		}), ViewerPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: runtimeName, PlaybackID: "public", Protocol: "hls"}, "")
		if err != nil || observed != "internal" || response.GetPrimary().GetNodeId() == "" {
			t.Fatalf("%s prepared as %q: %v, %v", runtimeName, observed, response, err)
		}
	}
}

func TestPreparedViewerRefusesAmbiguousObjectIdentity(t *testing.T) {
	for _, request := range []ViewerPlacementRequest{
		{TenantID: "tenant", InternalName: "internal", PlaybackID: "public", Protocol: "hls"},
		{TenantID: "tenant", StreamID: "stream", ArtifactHash: "hash", InternalName: "internal", PlaybackID: "public", Protocol: "hls"},
		{TenantID: "tenant", ArtifactHash: "hash", InternalName: "internal", PlaybackID: "public", Protocol: "hls"},
	} {
		calls := 0
		_, err := ResolvePreparedLiveViewerEndpoint(t.Context(), viewerPreparerFunc(func(_ context.Context, req ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
			calls++
			return preparedViewer(t, req), nil
		}), request, "")
		if err == nil || calls != 0 {
			t.Fatalf("ambiguous identity reached preparation: %+v, %v, calls=%d", request, err, calls)
		}
	}
}

type storedMediaPermitterFunc func(context.Context, ViewerPlacementRequest) (ViewerPlacementPermission, error)

func (fn storedMediaPermitterFunc) PermitViewer(ctx context.Context, req ViewerPlacementRequest) (ViewerPlacementPermission, error) {
	return fn(ctx, req)
}

func TestStoredMediaPlacementKeepsStorageOrderAndDropsRefusedDestinations(t *testing.T) {
	nodes := []state.ArtifactNodeInfo{
		{NodeID: "warm-a", ClusterID: "us"},
		{NodeID: "refused", ClusterID: "private"},
		{NodeID: "warm-b", ClusterID: "us"},
	}
	deps := &PlaybackDependencies{StoredMediaPlacement: storedMediaPermitterFunc(func(_ context.Context, req ViewerPlacementRequest) (ViewerPlacementPermission, error) {
		if req.ArtifactHash != "hash-1" || req.StreamID != "" || req.InternalName != "internal" || req.PlaybackID != "public" || req.Protocol != "hls" {
			t.Fatalf("stored media placement lost its signed identity: %+v", req)
		}
		return ViewerPlacementPermission{ObjectID: "artifact:1", SourceGeneration: "artifact:digest", ExpiresAt: time.Now().Add(time.Minute),
			Destinations: []ViewerPlacementDestination{{ClusterID: "us", NodeID: "warm-b"}, {ClusterID: "us", NodeID: "warm-a"}}}, nil
	})}

	permitted, err := enforceStoredMediaPlacement(t.Context(), deps, "tenant", "vod+internal", "public", "hash-1", nodes)
	if err != nil {
		t.Fatal(err)
	}
	// Policy decides eligibility; storage affinity still decides the order.
	if len(permitted) != 2 || permitted[0].NodeID != "warm-a" || permitted[1].NodeID != "warm-b" {
		t.Fatalf("policy reordered or dropped permitted storage candidates: %+v", permitted)
	}
}

func TestStoredMediaPlacementRefusalIsNotAMissingArtifact(t *testing.T) {
	nodes := []state.ArtifactNodeInfo{{NodeID: "warm-a", ClusterID: "private"}}
	for _, permitter := range []ViewerPlacementPermitter{
		storedMediaPermitterFunc(func(context.Context, ViewerPlacementRequest) (ViewerPlacementPermission, error) {
			return ViewerPlacementPermission{Destinations: []ViewerPlacementDestination{{ClusterID: "us", NodeID: "other"}}}, nil
		}),
		storedMediaPermitterFunc(func(context.Context, ViewerPlacementRequest) (ViewerPlacementPermission, error) {
			return ViewerPlacementPermission{}, errors.New("policy unavailable")
		}),
	} {
		deps := &PlaybackDependencies{StoredMediaPlacement: permitter}
		permitted, err := enforceStoredMediaPlacement(t.Context(), deps, "tenant", "internal", "public", "hash-1", nodes)
		if !errors.Is(err, ErrStoredMediaPlacementUnavailable) || permitted != nil {
			t.Fatalf("refused stored media did not surface as a placement refusal: %+v, %v", permitted, err)
		}
	}
}

func TestStoredMediaPlacementWithoutAnInstalledPolicyLeavesCandidates(t *testing.T) {
	nodes := []state.ArtifactNodeInfo{{NodeID: "warm-a", ClusterID: "us"}}
	permitted, err := enforceStoredMediaPlacement(t.Context(), &PlaybackDependencies{}, "tenant", "internal", "public", "hash-1", nodes)
	if err != nil || len(permitted) != 1 {
		t.Fatalf("uninstalled policy changed stored media candidates: %+v, %v", permitted, err)
	}
}

// Once a replica has installed the serving policy it must never serve stored
// media unfiltered again. A cleared permitter is a broken enforcing replica, not
// a return to legacy routing, so it refuses rather than falling open.
func TestStoredMediaPlacementRefusesOnceRequiredEvenWithNoPermitter(t *testing.T) {
	nodes := []state.ArtifactNodeInfo{{NodeID: "warm-a", ClusterID: "us"}}
	deps := &PlaybackDependencies{StoredMediaPlacementRequired: true}
	permitted, err := enforceStoredMediaPlacement(t.Context(), deps, "tenant", "internal", "public", "hash-1", nodes)
	if !errors.Is(err, ErrStoredMediaPlacementUnavailable) || permitted != nil {
		t.Fatalf("required policy fell open with no permitter installed: %+v, %v", permitted, err)
	}
}
