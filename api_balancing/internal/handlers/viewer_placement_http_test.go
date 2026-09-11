package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestPreparedViewerHTTPResolvesRequestedManifestBeforePreparation(t *testing.T) {
	for _, tc := range []struct {
		path, protocol, endpoint string
		status                   int
	}{
		{"public/hls/index.m3u8", "hls", "https://us.example/hls/public/index.m3u8", http.StatusTemporaryRedirect},
		{"public/cmaf/index.mpd", "dash", "https://us.example/cmaf/public/index.mpd", http.StatusTemporaryRedirect},
		{"public/cmaf/index.m3u8", "cmaf", "https://us.example/cmaf/public/index.m3u8", http.StatusTemporaryRedirect},
		{"public/WHEP", "whep", "https://us.example/whep/public", http.StatusTemporaryRedirect},
		{"public.mpd", "dash", "https://us.example/cmaf/public/index.mpd", http.StatusTemporaryRedirect},
		{"public.html", "mist_html", "https://us.example/public.html", http.StatusTemporaryRedirect},
		{"public/all", "webrtc", "wss://us.example/webrtc/public", http.StatusOK},
		{"public", "webrtc", "wss://us.example/webrtc/public", http.StatusOK},
		{"public/hls/index.mpd", "", "", http.StatusBadRequest},
		{"public/whep/index.m3u8", "", "", http.StatusBadRequest},
		{"public/cmaf/../index.mpd", "", "", http.StatusBadRequest},
		{"public/unknown", "", "", http.StatusBadRequest},
	} {
		t.Run(tc.path, func(t *testing.T) {
			setupPreparedViewerHTTP(t, false)
			calls := 0
			SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, req control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				if req.Protocol != tc.protocol || req.PlaybackID != "public" || req.TenantID != "owner" || req.StreamID != "stream" || req.InternalName != "internal" || req.Location != nil {
					t.Fatalf("HTTP identity or requested manifest lost: %+v", req)
				}
				return preparedHTTPViewer(t, req, tc.endpoint), nil
			}))
			c, w := playbackCtxArms(t, tc.path)
			c.Request.URL.RawQuery = "lat=40.7&lon=-74"
			c.Request.Header.Set("X-Latitude", "40.7")
			c.Request.Header.Set("X-Longitude", "-74")
			HandleGenericViewerPlayback(c)
			if w.Code != tc.status {
				t.Fatalf("HTTP status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == http.StatusBadRequest {
				if calls != 0 || w.Header().Get("Location") != "" {
					t.Fatal("conflicting manifest prepared or redirected media")
				}
				return
			}
			if calls != 1 {
				t.Fatalf("prepared %d destinations", calls)
			}
			if tc.status == http.StatusOK {
				var response struct {
					Primary struct {
						URL    string `json:"url"`
						NodeID string `json:"nodeId"`
					} `json:"primary"`
					Fallbacks []json.RawMessage `json:"fallbacks"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Primary.NodeID != "us-edge" || len(response.Fallbacks) != 0 {
					t.Fatalf("JSON response=%s err=%v", w.Body.String(), err)
				}
				return
			}
			u, err := url.Parse(w.Header().Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			u.RawQuery = ""
			if u.String() != tc.endpoint {
				t.Fatalf("HTTP changed prepared destination: got %s want %s", u, tc.endpoint)
			}
		})
	}
}

func TestPreparedViewerHTTPDeniesBeforePreparationAndFailsWithoutFallback(t *testing.T) {
	for _, denied := range []bool{true, false} {
		t.Run(map[bool]string{true: "billing-denied", false: "placement-unavailable"}[denied], func(t *testing.T) {
			setupPreparedViewerHTTP(t, denied)
			calls := 0
			SetViewerPlacementPreparer(viewerPlacementFunc(func(context.Context, control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				return balancer.PlacementPreparationResult{}, errors.New("private destination unavailable")
			}))
			c, w := playbackCtxArms(t, "public/hls")
			HandleGenericViewerPlayback(c)
			want, wantCalls := http.StatusServiceUnavailable, 1
			if denied {
				want, wantCalls = http.StatusForbidden, 0
			}
			if w.Code != want || calls != wantCalls || w.Header().Get("Location") != "" {
				t.Fatalf("failed placement bypassed: status=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
		})
	}
}

func setupPreparedViewerHTTP(t *testing.T, denied bool, override ...*commodoreBalancingFake) {
	t.Helper()
	balancingTestEnv(t)
	t.Cleanup(control.SetupTestRegistry("", nil))
	previous, oldLB, oldProcessor, oldGeo := viewerPlacementPreparer, lb, triggerProcessor, geoipReader
	lb, triggerProcessor, geoipReader = nil, nil, nil
	t.Cleanup(func() {
		viewerPlacementPreparer, lb, triggerProcessor, geoipReader = previous, oldLB, oldProcessor, oldGeo
	})
	fake := &commodoreBalancingFake{playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
		return &commodorepb.ResolvePlaybackIDResponse{InternalName: "live+internal", TenantId: "owner", StreamId: "stream"}, nil
	}}
	if len(override) > 0 {
		fake = override[0]
	}
	startBalancingCommodoreFake(t, fake)
	startQuartermasterFake(t, &fakeTenantService{validate: func(context.Context, *quartermasterpb.ValidateTenantRequest) (*quartermasterpb.ValidateTenantResponse, error) {
		return &quartermasterpb.ValidateTenantResponse{Valid: !denied, IsActive: !denied, TenantId: "owner", BillingModel: "postpaid"}, nil
	}})
}

func preparedHTTPViewer(t *testing.T, req control.ViewerPlacementRequest, endpoint string) balancer.PlacementPreparationResult {
	t.Helper()
	now := time.Now()
	attempt, err := placement.NewPreparationAttemptID(now)
	if err != nil {
		t.Fatal(err)
	}
	return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: req.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(req.StreamID), SourceGeneration: "source", NodeID: "us-edge", ClusterID: "us", Protocol: req.Protocol, Endpoint: endpoint, PublicBaseURL: "https://us.example", AttemptID: attempt, ExpiresAt: now.Add(10 * time.Second)}
}
