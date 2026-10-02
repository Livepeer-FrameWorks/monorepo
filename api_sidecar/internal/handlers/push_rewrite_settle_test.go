package handlers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/gin-gonic/gin"
)

type settledPushAnswer struct {
	triggerUUID   string
	pid           int64
	relayedAccept bool
	delivered     bool
	forwarded     bool
}

// captureSettledPushAnswers records each settle; the handler settles off the request path, so the
// returned wait blocks until n settles arrived or a deadline passed.
func captureSettledPushAnswers(t *testing.T) func(n int) []settledPushAnswer {
	t.Helper()
	var mu sync.Mutex
	var settled []settledPushAnswer
	prev := settlePushRewriteAnswer
	settlePushRewriteAnswer = func(trigger *ipcpb.MistTrigger, result *control.MistTriggerResult, relayedAccept, delivered bool, _ logging.Logger) {
		mu.Lock()
		defer mu.Unlock()
		settled = append(settled, settledPushAnswer{
			triggerUUID: trigger.GetPushRewrite().GetTriggerUuid(), pid: trigger.GetPushRewrite().GetPid(),
			relayedAccept: relayedAccept, delivered: delivered, forwarded: result.Forwarded,
		})
	}
	t.Cleanup(func() { settlePushRewriteAnswer = prev })
	return func(n int) []settledPushAnswer {
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			got := append([]settledPushAnswer(nil), settled...)
			mu.Unlock()
			if len(got) >= n || time.Now().After(deadline) {
				return got
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
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
			waitSettled := captureSettledPushAnswers(t)
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
			if settled := waitSettled(1); len(settled) != 1 || settled[0] != tc.want {
				t.Fatalf("settled answers = %+v, want [%+v]", settled, tc.want)
			}
		})
	}
}

// Mist gives up on a blocking trigger after its own timeout and delivers the same execution again,
// so a refusal must reach Mist without waiting for the settle's durable report. A served answer
// that waited would make the settle report the execution abandoned while Mist re-delivers it, and
// Foghorn would then refuse the re-delivery as an ended session.
func TestHandlePushRewriteAnswersMistBeforeSettling(t *testing.T) {
	setupTriggerTest(t, "tenant-settle-order")
	stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return &control.MistTriggerResult{Abort: true, ErrorCode: ipcpb.IngestErrorCode_INGEST_ERROR_INTERNAL, Forwarded: true}, nil
	})
	router := gin.New()
	router.POST("/webhooks/mist/push_rewrite", HandlePushRewrite)
	server := httptest.NewServer(router)
	// Registered first so it runs last: closing the server waits for the handler, which may be
	// blocked in the settle until release is closed.
	t.Cleanup(server.Close)

	release := make(chan struct{})
	settled := make(chan settledPushAnswer, 1)
	prev := settlePushRewriteAnswer
	settlePushRewriteAnswer = func(trigger *ipcpb.MistTrigger, result *control.MistTriggerResult, relayedAccept, delivered bool, _ logging.Logger) {
		<-release
		settled <- settledPushAnswer{
			triggerUUID: trigger.GetPushRewrite().GetTriggerUuid(), pid: trigger.GetPushRewrite().GetPid(),
			relayedAccept: relayedAccept, delivered: delivered, forwarded: result.Forwarded,
		}
	}
	t.Cleanup(func() { settlePushRewriteAnswer = prev })
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/webhooks/mist/push_rewrite", strings.NewReader("rtmp://ingest/app\nexample.com\nstream-key"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Trigger-UUID", "test-trigger-uuid")
	req.Header.Set("X-PID", "4242")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Mist did not receive the answer while the settle was still running: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}

	close(release)
	select {
	case got := <-settled:
		want := settledPushAnswer{triggerUUID: "test-trigger-uuid", pid: 4242, delivered: true, forwarded: true}
		if got != want {
			t.Fatalf("settled answer = %+v, want %+v", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer was never settled")
	}
}
