package control

import (
	"context"
	"frameworks/api_sidecar/internal/storage"
	"slices"
	"testing"
	"time"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/streamident"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func relativeStopFixtureRequest(t *testing.T) *ipcpb.StopSessionsRequest {
	t.Helper()
	request := stopFixtureRequest()
	request.ExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
	return request
}

func TestConfirmedSessionStopLocalBudgetDoesNotTrustRemoteWallClock(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked"}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	authorized := false
	err := stopSessionsEffectWithAuthorization(t.Context(), logging.NewLogger(), relativeStopFixtureRequest(t), func() bool { return true }, func(context.Context) (time.Duration, error) {
		authorized = true
		return 5 * time.Second, nil
	})
	if err != nil || !authorized || len(m.remaining()) != 0 {
		t.Fatalf("valid local execution budget rejected because of remote clock skew: authorized=%v remaining=%v err=%v", authorized, m.remaining(), err)
	}
}

func TestConfirmedSessionStopRechecksGenerationAfterAuthorization(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked"}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	request := relativeStopFixtureRequest(t)
	request.Targets[0].SourceGeneration = "10000000-0000-4000-8000-000000000002"
	t.Cleanup(func() {
		fence, _ := lockIngestFence("live+revoked", false)
		if fence != nil {
			fence.active, fence.evictPending = false, true
			fence.Unlock()
		}
	})
	err := stopSessionsEffectWithAuthorization(t.Context(), logging.NewLogger(), request, func() bool { return true }, func(context.Context) (time.Duration, error) {
		fence, _ := lockIngestFence("live+revoked", true)
		fence.generation, fence.active = "10000000-0000-4000-8000-000000000003", true
		fence.Unlock()
		return 5 * time.Second, nil
	})
	if err == nil || m.stopCalls != 0 || len(m.remaining()) != 1 {
		t.Fatalf("authorization stopped a replacement publisher: remaining=%v calls=%d err=%v", m.remaining(), m.stopCalls, err)
	}
}

func stopFixtureRequest() *ipcpb.StopSessionsRequest {
	return &ipcpb.StopSessionsRequest{
		RequestId: "stop-attempt", TenantId: "suspended-tenant", NodeId: "edge",
		ExpiresAt:          timestamppb.New(time.Now().Add(time.Minute)),
		RemainingTimeoutMs: 10_000,
		StreamNames:        []string{"revoked"}, Targets: []*ipcpb.StopSessionsTarget{{InternalName: "revoked"}},
	}
}

func TestConfirmedSessionStopFindsPublisherWithoutViewers(t *testing.T) {
	const generation = "10000000-0000-4000-8000-000000000002"
	const replacement = "10000000-0000-4000-8000-000000000003"
	runtime, err := streamident.BoundRuntimeName("revoked", streamident.RuntimePublisher, generation)
	if err != nil {
		t.Fatal(err)
	}
	nextRuntime, err := streamident.BoundRuntimeName("revoked", streamident.RuntimePublisher, replacement)
	if err != nil {
		t.Fatal(err)
	}
	m := &sessionMist{sessions: map[string]string{"Irevoked": runtime, "Ireplacement": nextRuntime, "Vother": "live+other"}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	for name, owner := range map[string]string{runtime: generation, nextRuntime: replacement} {
		fence, _ := lockIngestFence(name, true)
		fence.generation, fence.active = owner, true
		fence.Unlock()
		t.Cleanup(func() {
			fence, _ := lockIngestFence(name, false)
			if fence != nil {
				fence.active, fence.evictPending = false, true
				fence.Unlock()
			}
		})
	}
	req := stopFixtureRequest()
	req.Targets[0].SourceGeneration = generation
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err != nil {
		t.Fatalf("stop publisher without viewers: %v", err)
	}
	if got := m.remaining(); !slices.Equal(got, []string{"Ireplacement", "Vother"}) {
		t.Fatalf("publisher without viewers survived or replacement scope changed: %v", got)
	}
}

func TestConfirmedSessionStopRetriesMistFailureBeforeSuccess(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked", "Vrevoked": "live+revoked", "Vother": "live+other"}, failStops: 1}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("temporary Mist failure was acknowledged as successful revocation")
	}
	if got := m.remaining(); !slices.Contains(got, "Irevoked") {
		t.Fatal("failed stop unexpectedly removed the publisher")
	}
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err != nil {
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
	err := stopFixtureEffect(ctx, logging.NewLogger(), req, current)
	if err == nil {
		t.Fatalf("successful stop API with live publisher was considered verified: %v", err)
	}
	if m.stopCalls != 1 || !slices.Contains(m.remaining(), "Irevoked") {
		t.Fatal("test did not preserve the failed Mist effect")
	}
}

func TestConfirmedSessionStopRejectsMissingBudgetAndReplacementGeneration(t *testing.T) {
	m := &sessionMist{sessions: map[string]string{"Irevoked": "live+revoked"}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	req.RemainingTimeoutMs = 0
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("command without a local budget was executed")
	}
	req = stopFixtureRequest()
	fence, _ := lockIngestFence("live+revoked", true)
	fence.generation, fence.active = "10000000-0000-4000-8000-000000000003", true
	fence.Unlock()
	t.Cleanup(func() {
		fence, _ := lockIngestFence("live+revoked", false)
		fence.active, fence.evictPending = false, true
		fence.Unlock()
	})
	req.Targets[0].SourceGeneration = "10000000-0000-4000-8000-000000000002"
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err == nil {
		t.Fatal("old generation stopped a replacement publisher")
	}
	if m.stopCalls != 0 {
		t.Fatal("invalid command reached Mist")
	}
}

func stopFixtureEffect(ctx context.Context, logger logging.Logger, request *ipcpb.StopSessionsRequest, current func() bool) error {
	return stopSessionsEffectWithAuthorization(ctx, logger, request, current, func(context.Context) (time.Duration, error) { return 5 * time.Second, nil })
}

func TestClaimedStopRejectsDelayedAdmissionAfterRestart(t *testing.T) {
	resetControlState(t)
	path := t.TempDir() + "/ingest"
	store, err := storage.NewIngestGenerationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ingestGenerationStoreMu.Lock()
	previous := ingestGenerationStore
	ingestGenerationStore = store
	ingestGenerationStoreMu.Unlock()
	t.Cleanup(func() {
		ingestGenerationStoreMu.Lock()
		ingestGenerationStore = previous
		ingestGenerationStoreMu.Unlock()
	})
	const generation = "10000000-0000-4000-8000-000000000009"
	runtime, err := streamident.BoundRuntimeName("revoked", streamident.RuntimePublisher, generation)
	if err != nil {
		t.Fatal(err)
	}
	m := &sessionMist{sessions: map[string]string{}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	req := stopFixtureRequest()
	req.Targets[0].SourceGeneration = generation
	req.Targets[0].ConnectorPid = 123
	req.Targets[0].FenceAdmission = true
	if stopErr := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); stopErr != nil {
		t.Fatal(stopErr)
	}
	reopened, err := storage.NewIngestGenerationStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ingestGenerationStoreMu.Lock()
	ingestGenerationStore = reopened
	ingestGenerationStoreMu.Unlock()
	if err := recordAdmittedIngestGeneration(runtime, generation, 123, "late"); err == nil {
		t.Fatal("delayed admission revived a publisher after the stop was acknowledged and Helmsman restarted")
	}
	if err := recordAdmittedIngestGeneration("live+revoked", generation, 123, "late-legacy"); err == nil {
		t.Fatal("delayed legacy-runtime admission bypassed the persisted generation rejection")
	}
}

func TestClaimedStopPreservesReplacementViewers(t *testing.T) {
	resetControlState(t)
	store, err := storage.NewIngestGenerationStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ingestGenerationStoreMu.Lock()
	previous := ingestGenerationStore
	ingestGenerationStore = store
	ingestGenerationStoreMu.Unlock()
	t.Cleanup(func() {
		ingestGenerationStoreMu.Lock()
		ingestGenerationStore = previous
		ingestGenerationStoreMu.Unlock()
	})
	old, next := "10000000-0000-4000-8000-000000000019", "10000000-0000-4000-8000-000000000029"
	oldRuntime, _ := streamident.BoundRuntimeName("revoked", streamident.RuntimePublisher, old)
	nextRuntime, _ := streamident.BoundRuntimeName("revoked", streamident.RuntimePublisher, next)
	m := &sessionMist{sessions: map[string]string{"Iold": oldRuntime, "Vreplacement": nextRuntime}}
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: m.serve(t)})
	if err := recordAdmittedIngestGeneration(oldRuntime, old, 123, "old"); err != nil {
		t.Fatal(err)
	}
	if err := recordAdmittedIngestGeneration(nextRuntime, next, 124, "next"); err != nil {
		t.Fatal(err)
	}
	req := stopFixtureRequest()
	req.Targets[0].SourceGeneration = old
	req.Targets[0].ConnectorPid = 123
	req.Targets[0].FenceAdmission = true
	if err := stopFixtureEffect(t.Context(), logging.NewLogger(), req, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if got := m.remaining(); !slices.Equal(got, []string{"Vreplacement"}) {
		t.Fatalf("old publisher stop affected replacement viewers: %v", got)
	}
}
