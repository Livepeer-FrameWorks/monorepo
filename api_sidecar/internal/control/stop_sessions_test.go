package control

import (
	"context"
	"slices"
	"testing"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func stopFixtureRequest() *ipcpb.StopSessionsRequest {
	return &ipcpb.StopSessionsRequest{
		RequestId: "stop-attempt", TenantId: "suspended-tenant", NodeId: "edge",
		ExpiresAt:   timestamppb.New(time.Now().Add(time.Minute)),
		StreamNames: []string{"revoked"}, Targets: []*ipcpb.StopSessionsTarget{{InternalName: "revoked"}},
	}
}

func TestConfirmedSessionStopRetriesMistFailureBeforeSuccess(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked", "Vrevoked": "live+revoked", "Vother": "live+other"}, failStops: 1}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	if err := stopSessionsEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("temporary Mist failure was acknowledged as successful revocation")
	}
	if got := m.remaining(); !slices.Contains(got, "Irevoked") {
		t.Fatal("failed stop unexpectedly removed the publisher")
	}
	if err := stopSessionsEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err != nil {
		t.Fatalf("retry after Mist recovery: %v", err)
	}
	if got := m.remaining(); !slices.Equal(got, []string{"Vother"}) {
		t.Fatalf("verified stop changed sibling scope or left revoked sessions: %v", got)
	}
}

func TestConfirmedSessionStopDoesNotTrustSuccessfulAPIWithoutEffect(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked"}, ignoreStops: true}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	current := func() bool {
		calls++
		if calls == 4 {
			cancel()
		}
		return true
	}
	err := stopSessionsEffect(ctx, logging.NewLogger(), req, current)
	if err == nil {
		t.Fatalf("successful stop API with live publisher was considered verified: %v", err)
	}
	if m.stopCalls != 1 || !slices.Contains(m.remaining(), "Irevoked") {
		t.Fatal("test did not preserve the failed Mist effect")
	}
}

func TestConfirmedSessionStopRejectsExpiredAndReplacementGeneration(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked"}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	req.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
	if err := stopSessionsEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("expired command was executed")
	}
	req = stopFixtureRequest()
	fence, _ := lockIngestFence("live+revoked", true)
	fence.generation, fence.active = "replacement", true
	fence.Unlock()
	t.Cleanup(func() {
		fence, _ := lockIngestFence("live+revoked", false)
		fence.active, fence.evictPending = false, true
		fence.Unlock()
	})
	req.Targets[0].SourceGeneration = "original"
	if err := stopSessionsEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("old generation stopped a replacement publisher")
	}
	if m.stopCalls != 0 {
		t.Fatal("invalid command reached Mist")
	}
}
