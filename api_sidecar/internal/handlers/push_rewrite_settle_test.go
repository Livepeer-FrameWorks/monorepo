package handlers

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type settledPushAnswer struct {
	triggerUUID   string
	pid           int64
	relayedAccept bool
	delivered     bool
	forwarded     bool
}

func captureSettledPushAnswers(t *testing.T) *[]settledPushAnswer {
	t.Helper()
	var settled []settledPushAnswer
	prev := settlePushRewriteAnswer
	settlePushRewriteAnswer = func(trigger *ipcpb.MistTrigger, result *control.MistTriggerResult, relayedAccept, delivered bool, _ logging.Logger) {
		settled = append(settled, settledPushAnswer{
			triggerUUID: trigger.GetPushRewrite().GetTriggerUuid(), pid: trigger.GetPushRewrite().GetPid(),
			relayedAccept: relayedAccept, delivered: delivered, forwarded: result.Forwarded,
		})
	}
	t.Cleanup(func() { settlePushRewriteAnswer = prev })
	return &settled
}

// Every PUSH_REWRITE answer is settled with what Mist was told and whether Mist could receive it, so
// an admission Foghorn committed behind a non-accept is reported for Foghorn to end.
func TestHandlePushRewriteSettlesEveryAnswer(t *testing.T) {
	setupTriggerTest(t, "tenant-settle")
	const body = "rtmp://ingest/app\nexample.com\nstream-key"

	cases := []struct {
		name   string
		result *control.MistTriggerResult
		err    error
		cancel bool
		status int
		want   settledPushAnswer
	}{
		{
			name:   "admission wait timed out after the forward",
			result: &control.MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, Forwarded: true},
			err:    errors.New("timeout waiting for MistTrigger response"),
			status: http.StatusServiceUnavailable,
			want:   settledPushAnswer{triggerUUID: "test-trigger-uuid", pid: 4242, delivered: true, forwarded: true},
		},
		{
			name:   "accepted push",
			result: &control.MistTriggerResult{Response: "live+stream", Action: ipcpb.MistTriggerAction_MIST_TRIGGER_ACTION_VALUE, IngestGeneration: "gen", Forwarded: true},
			status: http.StatusOK,
			want:   settledPushAnswer{triggerUUID: "test-trigger-uuid", pid: 4242, relayedAccept: true, delivered: true, forwarded: true},
		},
		{
			name:   "Mist request gone before the answer",
			result: &control.MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_TIMEOUT, Forwarded: true},
			err:    context.Canceled,
			cancel: true,
			status: http.StatusServiceUnavailable,
			want:   settledPushAnswer{triggerUUID: "test-trigger-uuid", pid: 4242, delivered: false, forwarded: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settled := captureSettledPushAnswers(t)
			stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				return tc.result, tc.err
			})
			ctx, rec := newWebhookContext(body)
			ctx.Request.Header.Set("X-PID", "4242")
			if tc.cancel {
				requestCtx, cancel := context.WithCancel(ctx.Request.Context())
				cancel()
				ctx.Request = ctx.Request.WithContext(requestCtx)
			}
			HandlePushRewrite(ctx)
			assertStatus(t, rec, tc.status)
			if len(*settled) != 1 || (*settled)[0] != tc.want {
				t.Fatalf("settled answers = %+v, want [%+v]", *settled, tc.want)
			}
		})
	}
}
