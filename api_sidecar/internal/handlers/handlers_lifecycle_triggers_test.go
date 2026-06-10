package handlers

import (
	"errors"
	"net/http"
	"testing"

	"frameworks/api_sidecar/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/gin-gonic/gin"
)

// These tests cover the non-blocking MistServer lifecycle triggers. The
// load-bearing contract they share is the ack discipline toward MistServer:
//
//   - Durable triggers (STREAM_END, PUSH_END, PUSH_INPUT_CLOSE, RECORDING_END,
//     RECORDING_SEGMENT) must NOT acknowledge until the event is safely on the
//     WAL. A WAL/enqueue failure returns 503 so Mist *retries* — losing one of
//     these silently would corrupt Foghorn's admission / billing state.
//   - Fire-and-forget triggers (STREAM_BUFFER, LIVE_TRACK_LIST) are pure
//     telemetry. A forward failure is swallowed and the handler still returns
//     200, because making Mist retry a buffer-health sample buys nothing.
//
// Each test asserts that distinction directly rather than just driving lines.

// validRecordingEndBody is a full RECORDING_END payload (stream, target, output,
// bytes, secondsWriting, timeStarted, timeEnded, mediaDurationMs, firstPacket,
// lastPacket, machine exit reason, human exit reason, final track-summary JSON).
// processing+ prefix also exercises the local processing-end signal branch.
const validRecordingEndBody = "processing+job1\n/tmp/out.mkv\nMistOutMKV\n4096\n12\n1700000000\n1700000012\n12000\n0\n12000\nCLEAN_EOF\nclean end-of-file\n{\"tracks\":[{\"idx\":0,\"id\":7,\"selected\":true,\"type\":\"video\",\"codec\":\"H264\",\"width\":1280,\"height\":720,\"firstms\":0,\"lastms\":12000,\"bps\":800000,\"rate\":30}]}"

// recordingEndBodyNoPath is identical but with an empty file-path field. A
// non-empty path makes HandleRecordingEnd spawn a fire-and-forget S3-sync
// goroutine (background I/O, out of scope here) that reads the package-global
// logger; the per-test harness rewrites that global, so leaving the goroutine
// running would race it. The empty path skips the goroutine while still
// exercising the durable forward and processing-end signal.
const recordingEndBodyNoPath = "processing+job1\n\nMistOutMKV\n4096\n12\n1700000000\n1700000012\n12000\n0\n12000\nCLEAN_EOF\nclean end-of-file\n{\"tracks\":[{\"idx\":0,\"id\":7,\"selected\":true,\"type\":\"video\",\"codec\":\"H264\",\"width\":1280,\"height\":720,\"firstms\":0,\"lastms\":12000,\"bps\":800000,\"rate\":30}]}"

func TestNonBlockingDurableTriggersHappyPath(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	tests := []struct {
		name        string
		body        string
		handler     func(*gin.Context)
		wantType    string
		wantStream  string // expected stream name on the parsed trigger, "" to skip
		streamField func(*ipcpb.MistTrigger) string
	}{
		{
			name:       "stream_end",
			body:       "live+stream-1\n100\n200\n3\n1\n2\n42",
			handler:    HandleStreamEnd,
			wantType:   "STREAM_END",
			wantStream: "live+stream-1",
			streamField: func(m *ipcpb.MistTrigger) string {
				return m.GetStreamEnd().GetStreamName()
			},
		},
		{
			name:       "push_end",
			body:       "1\nprocessing+job1\nbefore\nafter\nlogs\nOK",
			handler:    HandlePushEnd,
			wantType:   "PUSH_END",
			wantStream: "processing+job1",
			streamField: func(m *ipcpb.MistTrigger) string {
				return m.GetPushEnd().GetStreamName()
			},
		},
		{
			name:       "push_input_close",
			body:       "live+stream-1\nhost\nbin\n4321\nmreason\nhreason\n{}",
			handler:    HandlePushInputClose,
			wantType:   "PUSH_INPUT_CLOSE",
			wantStream: "live+stream-1",
			streamField: func(m *ipcpb.MistTrigger) string {
				return m.GetPushInputClose().GetStreamName()
			},
		},
		{
			name:       "recording_end",
			body:       recordingEndBodyNoPath,
			handler:    HandleRecordingEnd,
			wantType:   "RECORDING_END",
			wantStream: "processing+job1",
			streamField: func(m *ipcpb.MistTrigger) string {
				return m.GetRecordingComplete().GetStreamName()
			},
		},
		{
			name:       "recording_segment",
			body:       "live+stream-1\n/var/dvr/seg-1.ts\n2000\n100\n2100",
			handler:    HandleRecordingSegment,
			wantType:   "RECORDING_SEGMENT",
			wantStream: "live+stream-1",
			streamField: func(m *ipcpb.MistTrigger) string {
				return m.GetRecordingSegment().GetStreamName()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *ipcpb.MistTrigger
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				got = trigger
				return &control.MistTriggerResult{}, nil
			})

			ctx, rec := newWebhookContext(tt.body)
			tt.handler(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if rec.Body.String() != "OK" {
				t.Fatalf("body = %q, want OK", rec.Body.String())
			}
			if got == nil {
				t.Fatal("expected trigger to be durably forwarded")
			}
			if got.GetTriggerType() != tt.wantType {
				t.Fatalf("trigger_type = %q, want %q", got.GetTriggerType(), tt.wantType)
			}
			if got.GetTenantId() != "tenant-life" {
				t.Fatalf("tenant not applied: %q", got.GetTenantId())
			}
			if tt.wantStream != "" && tt.streamField(got) != tt.wantStream {
				t.Fatalf("stream = %q, want %q", tt.streamField(got), tt.wantStream)
			}
		})
	}
}

// On a WAL enqueue failure these handlers must refuse to acknowledge so Mist
// retries — 503, not 200. This is the inverse of the fire-and-forget contract
// and is the whole reason these triggers are "durable".
func TestNonBlockingDurableTriggersRefuseOnWalFailure(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	tests := []struct {
		name    string
		body    string
		handler func(*gin.Context)
	}{
		{"stream_end", "live+stream-1\n100\n200\n3\n1\n2\n42", HandleStreamEnd},
		{"push_end", "1\nlive+stream-1\nbefore\nafter\nlogs\nOK", HandlePushEnd},
		{"push_input_close", "live+stream-1\nhost\nbin\n4321\nm\nh\n{}", HandlePushInputClose},
		{"recording_end", validRecordingEndBody, HandleRecordingEnd},
		{"recording_segment", "live+stream-1\n/var/dvr/seg-1.ts\n2000\n100\n2100", HandleRecordingSegment},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				return nil, errors.New("wal unavailable")
			})

			ctx, rec := newWebhookContext(tt.body)
			tt.handler(ctx)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 on WAL failure", rec.Code)
			}
		})
	}
}

// A malformed payload is still durably recorded as a raw webhook (parse-failure
// envelope) so Foghorn can observe that Mist fired something we couldn't parse,
// rather than dropping it. The handler returns 200 once that raw record is on
// the WAL.
func TestNonBlockingDurableTriggersDurablyEnqueueParseFailure(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	tests := []struct {
		name     string
		body     string // deliberately too few params to parse
		handler  func(*gin.Context)
		wantType string
	}{
		{"stream_end", "", HandleStreamEnd, "STREAM_END"},
		{"push_end", "1\nlive+stream-1", HandlePushEnd, "PUSH_END"},
		{"push_input_close", "live+stream-1\nhost", HandlePushInputClose, "PUSH_INPUT_CLOSE"},
		{"recording_end", "live+stream-1\n/tmp/out.mkv", HandleRecordingEnd, "RECORDING_END"},
		{"recording_segment", "live+stream-1\n/var/dvr/seg.ts", HandleRecordingSegment, "RECORDING_SEGMENT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *ipcpb.MistTrigger
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				got = trigger
				return &control.MistTriggerResult{}, nil
			})

			ctx, rec := newWebhookContext(tt.body)
			tt.handler(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got == nil {
				t.Fatal("parse failure should still be durably forwarded")
			}
			if got.GetTriggerType() != tt.wantType {
				t.Fatalf("trigger_type = %q, want %q", got.GetTriggerType(), tt.wantType)
			}
			if got.GetRawMistWebhook() == nil {
				t.Fatalf("expected raw-webhook parse-failure envelope, got %T", got.GetTriggerPayload())
			}
			if got.GetRawMistWebhook().GetParseError() == "" {
				t.Fatal("parse-failure envelope must carry the parse error")
			}
		})
	}
}

// And when the WAL itself rejects the parse-failure record, the handler still
// refuses to ack (503) — a parse failure we can't even record must be retried.
func TestNonBlockingDurableTriggersParseFailureWalRefusal(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return nil, errors.New("wal unavailable")
	})

	ctx, rec := newWebhookContext("1\nlive+stream-1") // too few params for PUSH_END
	HandlePushEnd(ctx)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when parse-failure record cannot be persisted", rec.Code)
	}
}

func TestHandleStreamBufferFireAndForget(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	t.Run("success enriches and forwards", func(t *testing.T) {
		var got *ipcpb.MistTrigger
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			got = trigger
			return &control.MistTriggerResult{}, nil
		})

		body := "live+stream-1\nFULL\n{\"health\":{\"buffer\":1500,\"jitter\":40,\"issues\":\"VeryLowBuffer\"}}"
		ctx, rec := newWebhookContext(body)
		HandleStreamBuffer(ctx)

		if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
			t.Fatalf("status=%d body=%q, want 200/OK", rec.Code, rec.Body.String())
		}
		sb := got.GetStreamBuffer()
		if sb == nil {
			t.Fatal("stream buffer not forwarded")
		}
		// enrichStreamBufferTrigger should have stamped derived fields.
		if sb.HasIssues == nil || !sb.GetHasIssues() {
			t.Fatal("expected enrichment to flag issues from MistIssues")
		}
		if got.GetTenantId() != "tenant-life" {
			t.Fatalf("tenant not applied: %q", got.GetTenantId())
		}
	})

	t.Run("forward error is swallowed", func(t *testing.T) {
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			// Telemetry forward failed; handler must still ack so Mist does
			// not retry a buffer-health sample.
			return &control.MistTriggerResult{}, errors.New("foghorn down")
		})

		ctx, rec := newWebhookContext("live+stream-1\nFULL")
		HandleStreamBuffer(ctx)

		if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
			t.Fatalf("status=%d body=%q, want 200/OK on forward error", rec.Code, rec.Body.String())
		}
	})

	t.Run("parse error is not forwarded", func(t *testing.T) {
		called := false
		stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
			called = true
			return &control.MistTriggerResult{}, nil
		})

		ctx, rec := newWebhookContext("live+stream-1") // 1 param, needs >=2
		HandleStreamBuffer(ctx)

		if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
			t.Fatalf("status=%d body=%q, want 200/OK", rec.Code, rec.Body.String())
		}
		if called {
			t.Fatal("malformed STREAM_BUFFER must not be forwarded")
		}
	})
}

// LIVE_TRACK_LIST forwards via control.SendMistTrigger directly (not the
// stub seam), so with no control stream connected it always lands on the
// forward-error branch. The contract under test is purely the response
// discipline: every path returns 200/OK regardless of parse or forward outcome.
func TestHandleLiveTrackListAlwaysAcks(t *testing.T) {
	setupTriggerTest(t, "tenant-life")

	cases := []struct {
		name string
		body string
	}{
		{"valid body, no stream -> forward error", "live+stream-1\n{\"track0\":{\"codec\":\"H264\",\"type\":\"video\"}}"},
		{"malformed body -> parse error", "live+stream-1"},
		{"empty body", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ctx, rec := newWebhookContext(tc.body)
			HandleLiveTrackList(ctx)
			if rec.Code != http.StatusOK || rec.Body.String() != "OK" {
				t.Fatalf("status=%d body=%q, want 200/OK", rec.Code, rec.Body.String())
			}
		})
	}
}
