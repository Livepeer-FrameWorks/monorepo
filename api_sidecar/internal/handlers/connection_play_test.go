package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestHandleConnPlayExplicitAdmission(t *testing.T) {
	setupTriggerTest(t, "infrastructure-owner-not-stream-tenant")
	for _, tc := range []struct {
		name   string
		result *control.MistTriggerResult
		err    error
		status int
		allow  bool
	}{
		{"allow", &control.MistTriggerResult{Response: "true"}, nil, 200, true},
		{"typed allow", &control.MistTriggerResult{Response: "true", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE}, nil, 200, true},
		{"empty", &control.MistTriggerResult{}, nil, 200, false},
		{"rewrite", &control.MistTriggerResult{Response: "dtsc://another/live+stream"}, nil, 200, false},
		{"keep", &control.MistTriggerResult{Response: "true", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_KEEP}, nil, 200, false},
		{"abort", &control.MistTriggerResult{Response: "true", Abort: true}, nil, 200, false},
		{"error code", &control.MistTriggerResult{Response: "true", ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL}, nil, 200, false},
		{"missing", nil, nil, 503, false},
		{"failed", nil, errors.New("private service details"), 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				if !trigger.Blocking || trigger.GetTenantId() != "" || trigger.GetConnectionPlay().GetHost() != "192.0.2.1" {
					t.Fatal("source hook changed identity or blocking semantics")
				}
				return tc.result, tc.err
			})
			ctx, recorder := newWebhookContext("live+stream\n192.0.2.1\nDTSC\ndtsc://source/live+stream")
			HandleConnPlay(ctx)
			if recorder.Code != tc.status || (recorder.Body.String() == "true") != tc.allow || strings.Contains(recorder.Body.String(), "private") {
				t.Fatalf("unexpected admission response: %d %q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestHandleConnPlayBoundaries(t *testing.T) {
	setupTriggerTest(t, "tenant")
	stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		t.Fatal("viewer or malformed source hook reached source admission")
		return nil, nil
	})
	for _, body := range []string{"invalid", strings.Repeat("x", 16<<10+1), "live+stream\n192.0.2.1\nHLS\nhttps://source/hls/live+stream/index.m3u8"} {
		ctx, recorder := newWebhookContext(body)
		HandleConnPlay(ctx)
		if strings.Contains(body, "HLS") {
			if recorder.Code != http.StatusOK || recorder.Body.String() != "true" {
				t.Fatal("viewer hook was not passed through")
			}
		} else if recorder.Code != http.StatusBadRequest {
			t.Fatal("malformed hook was not rejected")
		}
	}
}

func TestHandleConnPlayPreservesCancellation(t *testing.T) {
	setupTriggerTest(t, "tenant")
	original := sendMistTrigger
	t.Cleanup(func() { sendMistTrigger = original })
	sendMistTrigger = func(ctx context.Context, _ *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		if ctx.Err() == nil {
			t.Fatal("cancelled HTTP request was detached")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 3*time.Second {
			t.Fatal("source admission has no bounded deadline")
		}
		return &control.MistTriggerResult{Response: "true"}, nil
	}
	ctx, recorder := newWebhookContext("live+stream\n192.0.2.1\nDTSC\ndtsc://source/live+stream")
	cancelled, cancel := context.WithCancel(ctx.Request.Context())
	cancel()
	ctx.Request = ctx.Request.WithContext(cancelled)
	HandleConnPlay(ctx)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("late allow survived request cancellation")
	}
}
