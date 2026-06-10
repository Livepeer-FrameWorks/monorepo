package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"frameworks/api_sidecar/internal/config"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// parseMode / modeToString are the canonical mapping between Mist's wire
// strings and the proto enum. They must round-trip for the three real modes and
// fail-closed on anything else.
func TestParseModeAndModeToString(t *testing.T) {
	roundTrip := []struct {
		s    string
		mode ipcpb.NodeOperationalMode
	}{
		{"normal", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL},
		{"draining", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING},
		{"maintenance", ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE},
	}
	for _, tc := range roundTrip {
		got, ok := parseMode(tc.s)
		if !ok || got != tc.mode {
			t.Fatalf("parseMode(%q) = (%v,%v), want (%v,true)", tc.s, got, ok, tc.mode)
		}
		if back := modeToString(tc.mode); back != tc.s {
			t.Fatalf("modeToString(%v) = %q, want %q", tc.mode, back, tc.s)
		}
	}

	// parseMode lowercases its input, so Mist's casing variations still map.
	if got, ok := parseMode("DRAINING"); !ok || got != ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING {
		t.Fatalf("parseMode(DRAINING) = (%v,%v), want draining", got, ok)
	}
	if _, ok := parseMode("bogus"); ok {
		t.Fatal("parseMode must reject unknown mode")
	}
	// Unknown/unspecified enum renders as the safe default "normal".
	if s := modeToString(ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_UNSPECIFIED); s != "normal" {
		t.Fatalf("modeToString(UNSPECIFIED) = %q, want normal", s)
	}
}

func TestHandleGetNodeMode(t *testing.T) {
	setupTriggerTest(t, "tenant-mode")

	cases := []struct {
		seed ipcpb.NodeOperationalMode
		want string
	}{
		{ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_NORMAL, "normal"},
		{ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_DRAINING, "draining"},
		{ipcpb.NodeOperationalMode_NODE_OPERATIONAL_MODE_MAINTENANCE, "maintenance"},
	}
	for _, tc := range cases {
		config.ApplySeed(&ipcpb.ConfigSeed{TenantId: "tenant-mode", OperationalMode: tc.seed}, nil)

		ctx, rec := newWebhookContext("")
		HandleGetNodeMode(ctx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("invalid JSON response: %v", err)
		}
		if resp["mode"] != tc.want {
			t.Fatalf("mode = %v, want %q", resp["mode"], tc.want)
		}
		if _, ok := resp["node_id"]; !ok {
			t.Fatal("response missing node_id")
		}
	}
}

func TestHandleSetNodeMode(t *testing.T) {
	setupTriggerTest(t, "tenant-mode")

	t.Run("invalid JSON is rejected", func(t *testing.T) {
		ctx, rec := newWebhookContext("{not json")
		HandleSetNodeMode(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("missing mode field is rejected", func(t *testing.T) {
		ctx, rec := newWebhookContext(`{"reason":"x"}`)
		HandleSetNodeMode(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("invalid mode value is rejected", func(t *testing.T) {
		ctx, rec := newWebhookContext(`{"mode":"sleepy"}`)
		HandleSetNodeMode(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	// A valid request with no control stream connected cannot reach Foghorn, so
	// the handler surfaces 503 rather than pretending the change was applied.
	t.Run("valid mode without control stream is unavailable", func(t *testing.T) {
		ctx, rec := newWebhookContext(`{"mode":"draining","reason":"drain for maintenance"}`)
		HandleSetNodeMode(ctx)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})
}

// THUMBNAIL_UPDATED parses a newline payload (stream name, then thumbnail paths)
// and asks Foghorn for upload URLs. Every path returns 200/OK: an incomplete
// payload or an unreachable control stream are both logged-and-swallowed because
// thumbnails are best-effort.
func TestHandleThumbnailUpdated(t *testing.T) {
	setupTriggerTest(t, "tenant-mode")

	t.Run("incomplete payload is swallowed", func(t *testing.T) {
		ctx, rec := newWebhookContext("live+stream-1") // stream name only, no paths
		HandleThumbnailUpdated(ctx)
		if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
			t.Fatalf("status=%d body=%q, want 200/OK", rec.Code, rec.Body.String())
		}
	})

	t.Run("valid payload, no control stream, still acks", func(t *testing.T) {
		ctx, rec := newWebhookContext("live+stream-1\n/thumbs/a.jpg\n\n/thumbs/b.jpg")
		HandleThumbnailUpdated(ctx)
		if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
			t.Fatalf("status=%d body=%q, want 200/OK", rec.Code, rec.Body.String())
		}
	})
}
