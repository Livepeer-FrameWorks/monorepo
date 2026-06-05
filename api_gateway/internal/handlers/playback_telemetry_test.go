package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"

	"github.com/gin-gonic/gin"
)

type fakeResolver struct {
	artifact *commodorepb.ResolveArtifactPlaybackIDResponse
	stream   *commodorepb.ResolvePlaybackIDResponse
}

func (f *fakeResolver) ResolveArtifactPlaybackID(_ context.Context, _ string) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
	if f.artifact == nil {
		return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: false}, nil
	}
	return f.artifact, nil
}

func (f *fakeResolver) ResolvePlaybackID(_ context.Context, _ string) (*commodorepb.ResolvePlaybackIDResponse, error) {
	if f.stream == nil {
		return &commodorepb.ResolvePlaybackIDResponse{}, nil
	}
	return f.stream, nil
}

type fakeSink struct {
	triggers []*ipcpb.MistTrigger
}

func (f *fakeSink) SendTriggerContext(_ context.Context, trigger *ipcpb.MistTrigger) error {
	f.triggers = append(f.triggers, trigger)
	return nil
}

type fakeLimiter struct{ allow bool }

func (f fakeLimiter) Allow(_ string, _, _ int) (bool, int, int) {
	return f.allow, 0, 0
}

func newTestBootHandler(resolver playbackContentResolver, sink triggerSink, allow bool) *PlaybackTelemetryHandler {
	return newTestBootHandlerWithSecret(resolver, sink, allow, nil)
}

func newTestBootHandlerWithSecret(resolver playbackContentResolver, sink triggerSink, allow bool, secret []byte) *PlaybackTelemetryHandler {
	intake := NewBeaconIntake(resolver, fakeLimiter{allow: allow}, secret, logging.NewLogger())
	return NewPlaybackTelemetryHandler(intake, sink)
}

func postBoot(h *PlaybackTelemetryHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/playback/telemetry/boot", h.Handle)
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/playback/telemetry/boot", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestPlaybackTelemetry_LiveStreamServerDerivedAttribution(t *testing.T) {
	resolver := &fakeResolver{
		stream: &commodorepb.ResolvePlaybackIDResponse{
			TenantId:     "11111111-1111-1111-1111-111111111111",
			StreamId:     "22222222-2222-2222-2222-222222222222",
			InternalName: "live+demo",
		},
	}
	sink := &fakeSink{}
	h := newTestBootHandler(resolver, sink, true)

	// Body includes a spoofed tenantId/streamId/eventId — all must be ignored.
	body := `{
		"contentId":"demo",
		"traceId":"trace-1","sessionId":"sess-1",
		"tenantId":"evil","streamId":"evil-stream","eventId":"evil-event",
		"outcome":"success","playerType":"hlsjs","protocol":"hls",
		"totalTtfMs":1500,
		"spans":{"gatewayResolveMs":30,"mistHydrateMs":20,"playerSelectMs":10,"connectMs":40,"prebufferMs":60},
		"resources":[{"kind":"manifest","url":"https://edge/x.m3u8?jwt=secret&t=1","ttfbMs":8,"durationMs":18,"transferSize":800}]
	}`
	w := postBoot(h, body)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if len(sink.triggers) != 1 {
		t.Fatalf("expected 1 trigger sent, got %d", len(sink.triggers))
	}
	trig := sink.triggers[0]
	if trig.GetTriggerType() != "PLAYBACK_BOOT_TRACE" {
		t.Errorf("trigger type = %q", trig.GetTriggerType())
	}
	if trig.GetEventId() == "" {
		t.Error("expected Bridge to mint a canonical event_id")
	}
	if trig.GetEventId() == "evil-event" {
		t.Error("client-supplied event_id must be ignored")
	}
	boot := trig.GetPlaybackBootTrace()
	if boot == nil {
		t.Fatal("expected PlaybackBootTrace payload")
	}
	if boot.GetTenantId() != resolver.stream.TenantId {
		t.Errorf("tenant_id = %q, want server-derived %q", boot.GetTenantId(), resolver.stream.TenantId)
	}
	if boot.GetStreamId() != resolver.stream.StreamId {
		t.Errorf("stream_id = %q, want server-derived %q", boot.GetStreamId(), resolver.stream.StreamId)
	}
	if boot.GetClusterAttributed() {
		t.Error("cluster_attributed must be false without a telemetry token")
	}
	if boot.GetNodeId() != "" || boot.GetServingClusterId() != "" {
		t.Error("serving node/cluster must be empty without a telemetry token")
	}
	if boot.GetTotalTtfMs() != 1500 || boot.GetGatewayResolveMs() != 30 {
		t.Errorf("spans not carried through: ttf=%d gw=%d", boot.GetTotalTtfMs(), boot.GetGatewayResolveMs())
	}
	if len(boot.GetResources()) != 1 || boot.GetResources()[0].GetKind() != "manifest" {
		t.Errorf("resources not carried through: %+v", boot.GetResources())
	}
	if got := boot.GetResources()[0].GetUrl(); strings.Contains(got, "jwt") || strings.Contains(got, "?") {
		t.Errorf("resource URL must be query/fragment stripped server-side, got %q", got)
	}
}

func TestPlaybackTelemetry_ArtifactPreferredOverStream(t *testing.T) {
	resolver := &fakeResolver{
		artifact: &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			TenantId:        "11111111-1111-1111-1111-111111111111",
			ArtifactHash:    "abc123",
			ContentType:     "clip",
			OriginClusterId: "cluster-eu",
		},
	}
	sink := &fakeSink{}
	h := newTestBootHandler(resolver, sink, true)

	w := postBoot(h, `{"contentId":"clip-xyz","outcome":"success"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	boot := sink.triggers[0].GetPlaybackBootTrace()
	if boot.GetArtifactHash() != "abc123" {
		t.Errorf("artifact_hash = %q", boot.GetArtifactHash())
	}
	if boot.GetStreamId() != "" {
		t.Errorf("VOD artifact should have empty stream_id, got %q", boot.GetStreamId())
	}
	if boot.GetOriginClusterId() != "cluster-eu" {
		t.Errorf("origin_cluster_id = %q (should be stamped from Commodore)", boot.GetOriginClusterId())
	}
}

func TestPlaybackTelemetry_UnresolvableContentDropped(t *testing.T) {
	sink := &fakeSink{}
	h := newTestBootHandler(&fakeResolver{}, sink, true)
	w := postBoot(h, `{"contentId":"nope","outcome":"success"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if len(sink.triggers) != 0 {
		t.Errorf("expected no trigger for unresolvable content, got %d", len(sink.triggers))
	}
}

func TestPlaybackTelemetry_RateLimitedDropped(t *testing.T) {
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	sink := &fakeSink{}
	h := newTestBootHandler(resolver, sink, false) // limiter denies

	w := postBoot(h, `{"contentId":"demo","outcome":"success"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if len(sink.triggers) != 0 {
		t.Errorf("rate-limited request must not send, got %d", len(sink.triggers))
	}
}

func TestPlaybackTelemetry_ValidTokenAttributesCluster(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	sink := &fakeSink{}
	h := newTestBootHandlerWithSecret(resolver, sink, true, secret)

	tok, err := telemetrytoken.Sign(secret, telemetrytoken.Claims{
		ContentID:        "demo",
		NodeID:           "edge-nyc-1",
		ServingClusterID: "cluster-us-east",
	}, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	body := `{"contentId":"demo","outcome":"success","telemetryToken":"` + tok + `"}`
	if w := postBoot(h, body); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	boot := sink.triggers[0].GetPlaybackBootTrace()
	if !boot.GetClusterAttributed() {
		t.Error("valid token should set cluster_attributed=true")
	}
	if boot.GetNodeId() != "edge-nyc-1" || boot.GetServingClusterId() != "cluster-us-east" {
		t.Errorf("node/cluster not taken from token: node=%q cluster=%q", boot.GetNodeId(), boot.GetServingClusterId())
	}
}

func TestPlaybackTelemetry_TokenContentMismatchIgnored(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	sink := &fakeSink{}
	h := newTestBootHandlerWithSecret(resolver, sink, true, secret)

	// Token minted for a different content id must not attribute this beacon.
	tok, _ := telemetrytoken.Sign(secret, telemetrytoken.Claims{ContentID: "other", NodeID: "n", ServingClusterID: "c"}, time.Minute, time.Now())
	body := `{"contentId":"demo","outcome":"success","telemetryToken":"` + tok + `"}`
	if w := postBoot(h, body); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	boot := sink.triggers[0].GetPlaybackBootTrace()
	if boot.GetClusterAttributed() {
		t.Error("content-id-mismatched token must not attribute the cluster")
	}
}

func TestPlaybackTelemetry_InvalidBodyRejected(t *testing.T) {
	sink := &fakeSink{}
	h := newTestBootHandler(&fakeResolver{}, sink, true)
	w := postBoot(h, `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if len(sink.triggers) != 0 {
		t.Errorf("invalid body must not send")
	}
}
