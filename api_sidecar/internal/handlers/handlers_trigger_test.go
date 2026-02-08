package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
)

func setupTriggerTest(t *testing.T, tenantID string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	logger = logging.NewLoggerWithService("handlers-test")
	metrics = nil
	config.InitManager(logger)
	config.ApplySeed(&pb.ConfigSeed{TenantId: tenantID})
}

func newWebhookContext(body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	ctx.Request = req
	return ctx, recorder
}

func stubSendMistTrigger(t *testing.T, fn func(*pb.MistTrigger) (*control.MistTriggerResult, error)) {
	t.Helper()
	original := sendMistTrigger
	sendMistTrigger = func(trigger *pb.MistTrigger, _ logging.Logger) (*control.MistTriggerResult, error) {
		return fn(trigger)
	}
	t.Cleanup(func() {
		sendMistTrigger = original
	})
}

func TestTriggerHandlersApplyTenantContext(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	tests := []struct {
		name     string
		body     string
		handler  func(*gin.Context)
		response string
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
			var got *pb.MistTrigger
			stubSendMistTrigger(t, func(trigger *pb.MistTrigger) (*control.MistTriggerResult, error) {
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

func TestTriggerHandlersRejectMalformedPayloads(t *testing.T) {
	setupTriggerTest(t, "tenant-39b")

	tests := []struct {
		name     string
		body     string
		handler  func(*gin.Context)
		response string
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
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			called := false
			stubSendMistTrigger(t, func(trigger *pb.MistTrigger) (*control.MistTriggerResult, error) {
				called = true
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
			if called {
				t.Fatalf("expected trigger not to be forwarded")
			}
		})
	}
}
