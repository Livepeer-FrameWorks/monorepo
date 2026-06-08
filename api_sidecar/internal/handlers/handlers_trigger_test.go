package handlers

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/gin-gonic/gin"
)

func setupTriggerTest(t *testing.T, tenantID string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger = logging.NewLoggerWithService("handlers-test")
	metrics = nil
	config.InitManager(logger)
	config.ApplySeed(&ipcpb.ConfigSeed{TenantId: tenantID}, nil)
}

func newWebhookContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	ctx.Request = req
	return ctx, recorder
}

func stubSendMistTrigger(t *testing.T, fn func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error)) {
	t.Helper()
	originalSend := sendMistTrigger
	originalDurable := sendDurableMistTrigger
	sendMistTrigger = func(trigger *ipcpb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		return fn(trigger)
	}
	sendDurableMistTrigger = func(trigger *ipcpb.MistTrigger) error {
		_, err := fn(trigger)
		return err
	}
	t.Cleanup(func() {
		sendMistTrigger = originalSend
		sendDurableMistTrigger = originalDurable
	})
}

func TestForwardDurableRejectsUnregisteredTriggerType(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	called := false
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		called = true
		return &control.MistTriggerResult{}, nil
	})

	_, err := forwardDurable(string(mist.TriggerThumbnailUpdated), []byte("payload"), &ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerThumbnailUpdated),
	})
	if err == nil {
		t.Fatal("expected unregistered durable trigger to fail")
	}
	if !strings.Contains(err.Error(), "not registered durable") {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("unregistered durable trigger reached WAL sender")
	}
}

func TestForwardDurableAcceptsRegisteredTriggerTypes(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	var sent *ipcpb.MistTrigger
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		sent = trigger
		return &control.MistTriggerResult{}, nil
	})

	durableTypes := []mist.TriggerType{
		mist.TriggerUserEnd,
		mist.TriggerStreamEnd,
		mist.TriggerPushEnd,
		mist.TriggerPushInputClose,
		mist.TriggerRecordingEnd,
		mist.TriggerRecordingSegment,
		mist.TriggerLivepeerSegmentComplete,
		mist.TriggerProcessAVSegmentComplete,
	}
	for _, triggerType := range durableTypes {
		triggerType := triggerType
		t.Run(string(triggerType), func(t *testing.T) {
			sent = nil
			trigger := &ipcpb.MistTrigger{TriggerType: string(triggerType)}
			sourceEventID, err := forwardDurable(string(triggerType), []byte("payload:"+string(triggerType)), trigger)
			if err != nil {
				t.Fatalf("forwardDurable: %v", err)
			}
			if sourceEventID == "" {
				t.Fatal("source_event_id is empty")
			}
			if sent == nil {
				t.Fatal("expected WAL sender call")
			}
			if sent.GetRequestId() != sourceEventID {
				t.Fatalf("request_id = %q, want %q", sent.GetRequestId(), sourceEventID)
			}
			if sent.GetEventId() == "" {
				t.Fatal("event_id is empty")
			}
		})
	}
}

func TestTriggerHandlersApplyTenantContext(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	tests := []struct {
		name     string
		body     string
		handler  func(*gin.Context)
		response string
		raw      bool
	}{
		{
			name:     "push_rewrite",
			body:     "rtmp://ingest/app\nexample.com\nstream-key",
			handler:  HandlePushRewrite,
			response: "allow",
		},
		{
			name:     "play_rewrite",
			body:     "stream-name\n192.0.2.10\nHLS\nhttp://example.com/view",
			handler:  HandlePlayRewrite,
			response: "resolve",
		},
		{
			name:     "user_new",
			body:     "vod+abc123\n192.0.2.20\nconn-1\nHLS\nhttp://example.com/view\nsess-1",
			handler:  HandleUserNew,
			response: "true",
		},
		{
			name:     "user_end",
			body:     "sess-1\nvod+abc123\nHLS\n192.0.2.20\n10\n20\n30\ntag-a",
			handler:  HandleUserEnd,
			response: "OK",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var got *ipcpb.MistTrigger
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				got = trigger
				if test.name == "user_end" {
					return &control.MistTriggerResult{}, nil
				}
				return &control.MistTriggerResult{Response: test.response}, nil
			})

			ctx, recorder := newWebhookContext(test.body)
			test.handler(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}
			if test.name != "user_end" && recorder.Body.String() != test.response {
				t.Fatalf("expected response %q, got %q", test.response, recorder.Body.String())
			}
			if test.name == "user_end" && recorder.Body.String() != "OK" {
				t.Fatalf("expected response %q, got %q", "OK", recorder.Body.String())
			}
			if got == nil {
				t.Fatalf("expected mist trigger to be sent")
			}
			if got.TenantId == nil || got.GetTenantId() != "tenant-39b" {
				t.Fatalf("expected tenant id to be applied, got %v", got.GetTenantId())
			}
		})
	}
}

func TestHandleStreamSourceUsesDVRSourceOverride(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")
	control.RegisterDVRSourceOverride("live+stream-1", "dtsc://source-node/live+stream-1")
	t.Cleanup(func() { control.ClearDVRSourceOverride("live+stream-1") })

	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		t.Fatalf("DVR source override should be resolved locally, forwarded trigger: %+v", trigger)
		return nil, nil
	})

	ctx, recorder := newWebhookContext("live+stream-1")
	HandleStreamSource(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "dtsc://source-node/live+stream-1" {
		t.Fatalf("expected DVR source override response, got %q", got)
	}
}

func TestHandleStreamSourceUsesLocalSourceOverride(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")
	setProcessingSourceOverride("vod+clip-runtime", "/var/lib/frameworks/edge-storage/clips/stream/hash.mkv")
	t.Cleanup(func() { clearProcessingSourceOverride("vod+clip-runtime") })

	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		t.Fatalf("local source override should be resolved locally, forwarded trigger: %+v", trigger)
		return nil, nil
	})

	ctx, recorder := newWebhookContext("vod+clip-runtime")
	HandleStreamSource(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if got := recorder.Body.String(); got != "/var/lib/frameworks/edge-storage/clips/stream/hash.mkv" {
		t.Fatalf("expected local source override response, got %q", got)
	}
}

func TestTriggerHandlersRejectMalformedPayloads(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	tests := []struct {
		name     string
		body     string
		handler  func(*gin.Context)
		response string
		raw      bool
	}{
		{
			name:     "push_rewrite",
			body:     "rtmp://ingest/app\nexample.com",
			handler:  HandlePushRewrite,
			response: "",
		},
		{
			name:     "play_rewrite",
			body:     "stream-name\n192.0.2.10\nHLS",
			handler:  HandlePlayRewrite,
			response: "",
		},
		{
			name:     "user_new",
			body:     "vod+abc123\n192.0.2.20\nconn-1\nHLS",
			handler:  HandleUserNew,
			response: "false",
		},
		{
			name:     "user_end",
			body:     "sess-1\nvod+abc123\nHLS\n192.0.2.20",
			handler:  HandleUserEnd,
			response: "OK",
			raw:      true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			called := false
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				called = true
				if test.raw {
					if trigger.GetRawMistWebhook() == nil {
						t.Fatalf("expected raw Mist webhook payload, got %T", trigger.GetTriggerPayload())
					}
					if string(trigger.GetRawMistWebhook().GetPayloadRaw()) != test.body {
						t.Fatalf("raw payload mismatch: got %q", string(trigger.GetRawMistWebhook().GetPayloadRaw()))
					}
				}
				return &control.MistTriggerResult{}, nil
			})

			ctx, recorder := newWebhookContext(test.body)
			test.handler(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}
			if recorder.Body.String() != test.response {
				t.Fatalf("expected response %q, got %q", test.response, recorder.Body.String())
			}
			if called != test.raw {
				t.Fatalf("forwarded=%v, want %v", called, test.raw)
			}
		})
	}
}

func TestDurableTriggerHandlerRefusesWalFailure(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		return nil, errors.New("wal unavailable")
	})

	ctx, recorder := newWebhookContext("sess-1\nvod+abc123\nHLS\n192.0.2.20\n10\n20\n30\ntag-a")
	HandleUserEnd(ctx)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", recorder.Code)
	}
}

func TestUserViewerHandlersIgnoreNonPlaybackConnectors(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	tests := []struct {
		name     string
		body     string
		handler  func(*gin.Context)
		response string
	}{
		{
			name:     "user_new_thumbvtt",
			body:     "live+stream-1\n192.0.2.20\nconn-1\nThumbVTT\nhttp://example.com/thumbs.vtt\nsess-1",
			handler:  HandleUserNew,
			response: "true",
		},
		{
			name:     "user_end_thumbvtt",
			body:     "sess-1\nlive+stream-1\nThumbVTT\n192.0.2.20\n17\n0\n0\ntags",
			handler:  HandleUserEnd,
			response: "OK",
		},
		{
			name:     "user_new_http",
			body:     "live+stream-1\n192.0.2.20\nconn-2\nHTTP\nhttp://example.com/poster.jpg\nsess-2",
			handler:  HandleUserNew,
			response: "true",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			forwarded := false
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				forwarded = true
				return &control.MistTriggerResult{Response: "forwarded"}, nil
			})

			ctx, recorder := newWebhookContext(test.body)
			test.handler(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("expected status 200, got %d", recorder.Code)
			}
			if recorder.Body.String() != test.response {
				t.Fatalf("expected response %q, got %q", test.response, recorder.Body.String())
			}
			if forwarded {
				t.Fatalf("expected non-viewer connector not to be forwarded")
			}
		})
	}
}

func TestHandlePlayRewriteAllowsLocalProcessingJob(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	const streamName = "processing+artifact123"
	pendingJobsMu.Lock()
	pendingJobs[streamName] = make(chan ProcessingPushEndEvent, 1)
	pendingJobsMu.Unlock()
	t.Cleanup(func() {
		pendingJobsMu.Lock()
		delete(pendingJobs, streamName)
		pendingJobsMu.Unlock()
	})

	forwarded := false
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		forwarded = true
		return &control.MistTriggerResult{Response: "should-not-forward"}, nil
	})

	ctx, recorder := newWebhookContext(streamName + "\n172.18.0.10\nJSON\nhttp://mistserver/json_" + streamName + ".js")
	HandlePlayRewrite(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != streamName {
		t.Fatalf("expected response %q, got %q", streamName, recorder.Body.String())
	}
	if forwarded {
		t.Fatal("expected local processing PLAY_REWRITE to bypass Foghorn")
	}
}

func TestHandleStreamProcessUsesLocalOverride(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	const streamName = "processing+artifact123"
	const override = `[{"process":"AV","codec":"H264","track_select":"video=maxbps"}]`

	setProcessingProcessOverride(streamName, override)
	t.Cleanup(func() {
		clearProcessingProcessOverride(streamName)
	})

	forwarded := false
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		forwarded = true
		return &control.MistTriggerResult{Response: "should-not-be-used"}, nil
	})

	ctx, recorder := newWebhookContext(streamName)
	HandleStreamProcess(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", recorder.Code)
	}
	if recorder.Body.String() != override {
		t.Fatalf("expected override response %q, got %q", override, recorder.Body.String())
	}
	if forwarded {
		t.Fatal("expected local override to bypass Foghorn forwarding")
	}
}
