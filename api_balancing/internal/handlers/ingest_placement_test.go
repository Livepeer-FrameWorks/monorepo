package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type frontDoorPlacementFunc func(context.Context, control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error)

func (fn frontDoorPlacementFunc) PrepareIngest(ctx context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
	return fn(ctx, request)
}

func TestIngestFrontDoorPreparedPathCannotFallBackToLegacyNode(t *testing.T) {
	for _, outcome := range []string{"accepted", "failed", "missing"} {
		t.Run(outcome, func(t *testing.T) {
			sm := state.ResetDefaultManagerForTests()
			seedIngestTestNode(t, sm, "legacy-node", "legacy.example:18090")
			fake := &ingestCommodoreFake{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
				return admittedStreamContext(), nil
			}}
			startIngestCommodoreFake(t, fake)
			router := newIngestTestRouter(t)
			previous := ingestPlacementPreparer
			t.Cleanup(func() { ingestPlacementPreparer = previous })
			calls := 0
			SetIngestPlacementPreparer(frontDoorPlacementFunc(func(_ context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				if request.TenantID != "tenant-1" || request.StreamID != "stream-1" || request.InternalName != "internal-1" || request.Protocol != "whip" {
					t.Fatal("HTTP lost exact stream/protocol identity")
				}
				if outcome == "failed" {
					return balancer.PlacementPreparationResult{}, errors.New("policy denied")
				}
				now := time.Now()
				attempt, err := placement.NewPreparationAttemptID(now)
				return balancer.PlacementPreparationResult{Outcome: balancer.PlacementAccepted, TenantID: request.TenantID, ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID),
					NodeID: "prepared-node", ClusterID: "prepared-cluster", Protocol: "whip", PublicBaseURL: "https://prepared.example:8443",
					Endpoint: "https://prepared.example:8443/proxy/webrtc/$", AttemptID: attempt, ExpiresAt: now.Add(10 * time.Second)}, err
			}))
			if outcome == "missing" {
				SetIngestPlacementPreparer(nil)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/ingest/private-key", nil))
			if outcome == "accepted" {
				if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != "https://prepared.example:8443/proxy/webrtc/private-key" || calls != 1 {
					t.Fatalf("HTTP did not use prepared endpoint: %d %s", rec.Code, rec.Body.String())
				}
			} else if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
				t.Fatalf("HTTP escaped policy path: %d %s", rec.Code, rec.Header().Get("Location"))
			}
			if rec.Header().Get("Cache-Control") != "no-store" || fake.validateKeyHits.Load() != 0 {
				t.Fatal("prepared resolution lost credential privacy or claimed ownership")
			}
		})
	}
}
