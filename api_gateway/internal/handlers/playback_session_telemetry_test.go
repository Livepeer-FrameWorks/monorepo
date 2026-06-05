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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/telemetrytoken"

	"github.com/gin-gonic/gin"
)

func newTestSessionHandler(resolver playbackContentResolver, sink triggerSink, allow bool, secret []byte) *PlaybackSessionHandler {
	intake := NewBeaconIntake(resolver, fakeLimiter{allow: allow}, secret, logging.NewLogger())
	return NewPlaybackSessionHandler(intake, sink)
}

func postSession(h *PlaybackSessionHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/playback/telemetry/session", h.Handle)
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/playback/telemetry/session", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	return w
}

func TestPlaybackSession_ServerDerivedAttributionAndDeltas(t *testing.T) {
	resolver := &fakeResolver{
		stream: &commodorepb.ResolvePlaybackIDResponse{
			TenantId:     "11111111-1111-1111-1111-111111111111",
			StreamId:     "22222222-2222-2222-2222-222222222222",
			InternalName: "live+demo",
		},
	}
	sink := &fakeSink{}
	h := newTestSessionHandler(resolver, sink, true, nil)

	// Spoofed ownership ids must be ignored; deltas must carry through verbatim.
	body := `{
		"contentId":"demo","sessionId":"sess-1",
		"tenantId":"evil","streamId":"evil-stream",
		"beaconSeq":3,"isFinal":true,"flushReason":"visibility_hidden",
		"playerType":"hlsjs","protocol":"hls","isLive":true,
		"playedMs":42000,"rebufferMs":1200,"rebufferCount":2,"seekWaitMs":800,
		"frameStatsSupported":true,"framesDecoded":2500,"framesDropped":7,
		"firstFrame":true,"fatalError":false
	}`
	w := postSession(h, body)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	if len(sink.triggers) != 1 {
		t.Fatalf("expected 1 trigger sent, got %d", len(sink.triggers))
	}
	trig := sink.triggers[0]
	if trig.GetTriggerType() != "PLAYBACK_SESSION_QOE" {
		t.Errorf("trigger type = %q", trig.GetTriggerType())
	}
	if trig.GetEventId() == "" {
		t.Error("expected Bridge to mint a canonical event_id")
	}
	q := trig.GetPlaybackSessionQoe()
	if q == nil {
		t.Fatal("expected PlaybackSessionQoe payload")
	}
	if q.GetTenantId() != resolver.stream.TenantId || q.GetStreamId() != resolver.stream.StreamId {
		t.Errorf("attribution not server-derived: tenant=%q stream=%q", q.GetTenantId(), q.GetStreamId())
	}
	if q.GetBeaconSeq() != 3 || !q.GetIsFinal() || q.GetFlushReason() != "visibility_hidden" {
		t.Errorf("delta envelope not carried: seq=%d final=%t reason=%q", q.GetBeaconSeq(), q.GetIsFinal(), q.GetFlushReason())
	}
	if q.GetPlayedMs() != 42000 || q.GetRebufferMs() != 1200 || q.GetRebufferCount() != 2 || q.GetSeekWaitMs() != 800 {
		t.Errorf("rebuffer deltas not carried: played=%d reb=%d cnt=%d seek=%d",
			q.GetPlayedMs(), q.GetRebufferMs(), q.GetRebufferCount(), q.GetSeekWaitMs())
	}
	if !q.GetFrameStatsSupported() || q.GetFramesDecoded() != 2500 || q.GetFramesDropped() != 7 {
		t.Errorf("frame deltas not carried: supported=%t dec=%d drop=%d",
			q.GetFrameStatsSupported(), q.GetFramesDecoded(), q.GetFramesDropped())
	}
	if !q.GetFirstFrame() || q.GetFatalError() {
		t.Errorf("terminal flags not carried: firstFrame=%t fatal=%t", q.GetFirstFrame(), q.GetFatalError())
	}
	if q.GetClusterAttributed() {
		t.Error("cluster_attributed must be false without a telemetry token")
	}
}

func TestPlaybackSession_ArtifactPreferredOverStream(t *testing.T) {
	resolver := &fakeResolver{
		artifact: &commodorepb.ResolveArtifactPlaybackIDResponse{
			Found:           true,
			TenantId:        "11111111-1111-1111-1111-111111111111",
			ArtifactHash:    "abc123",
			ContentType:     "vod",
			OriginClusterId: "cluster-eu",
		},
	}
	sink := &fakeSink{}
	h := newTestSessionHandler(resolver, sink, true, nil)

	if w := postSession(h, `{"contentId":"vod-xyz","sessionId":"s","beaconSeq":0,"isFinal":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	q := sink.triggers[0].GetPlaybackSessionQoe()
	if q.GetArtifactHash() != "abc123" {
		t.Errorf("artifact_hash = %q", q.GetArtifactHash())
	}
	if q.GetStreamId() != "" {
		t.Errorf("VOD artifact should have empty stream_id, got %q", q.GetStreamId())
	}
	if q.GetContentType() != "vod" {
		t.Errorf("content_type should fall back to attribution, got %q", q.GetContentType())
	}
	if q.GetOriginClusterId() != "cluster-eu" {
		t.Errorf("origin_cluster_id = %q (should be stamped from Commodore)", q.GetOriginClusterId())
	}
}

func TestPlaybackSession_UnresolvableDroppedAndRateLimited(t *testing.T) {
	// Unresolvable content → no trigger.
	sink := &fakeSink{}
	h := newTestSessionHandler(&fakeResolver{}, sink, true, nil)
	if w := postSession(h, `{"contentId":"nope","sessionId":"s","isFinal":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("unresolvable: expected 204, got %d", w.Code)
	}
	if len(sink.triggers) != 0 {
		t.Errorf("unresolvable content must not send, got %d", len(sink.triggers))
	}

	// Rate-limited → no trigger.
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	sink2 := &fakeSink{}
	h2 := newTestSessionHandler(resolver, sink2, false, nil)
	if w := postSession(h2, `{"contentId":"demo","sessionId":"s","isFinal":true}`); w.Code != http.StatusNoContent {
		t.Fatalf("rate-limited: expected 204, got %d", w.Code)
	}
	if len(sink2.triggers) != 0 {
		t.Errorf("rate-limited request must not send, got %d", len(sink2.triggers))
	}
}

func TestPlaybackSession_ValidTokenAttributesCluster(t *testing.T) {
	secret := []byte("platform-telemetry-secret")
	resolver := &fakeResolver{stream: &commodorepb.ResolvePlaybackIDResponse{TenantId: "11111111-1111-1111-1111-111111111111"}}
	sink := &fakeSink{}
	h := newTestSessionHandler(resolver, sink, true, secret)

	tok, err := telemetrytoken.Sign(secret, telemetrytoken.Claims{
		ContentID:        "demo",
		NodeID:           "edge-nyc-1",
		ServingClusterID: "cluster-us-east",
	}, time.Minute, time.Now())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	body := `{"contentId":"demo","sessionId":"s","isFinal":true,"telemetryToken":"` + tok + `"}`
	if w := postSession(h, body); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}
	q := sink.triggers[0].GetPlaybackSessionQoe()
	if !q.GetClusterAttributed() {
		t.Error("valid token should set cluster_attributed=true")
	}
	if q.GetNodeId() != "edge-nyc-1" || q.GetServingClusterId() != "cluster-us-east" {
		t.Errorf("node/cluster not taken from token: node=%q cluster=%q", q.GetNodeId(), q.GetServingClusterId())
	}
}
