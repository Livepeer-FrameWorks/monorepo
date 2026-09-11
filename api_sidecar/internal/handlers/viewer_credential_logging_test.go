package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"frameworks/api_sidecar/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestPlayRewriteDoesNotLogViewerCredentials(t *testing.T) {
	const canary = "private-viewer-canary-8392"
	for _, body := range []string{
		"public\n203.0.113.1\nHLS\nhttps://edge/live?jwt=" + canary,
		"public\nhttps://edge/live?jwt=" + canary,
	} {
		t.Run(body, func(t *testing.T) {
			setupTriggerTest(t, "tenant")
			logger.SetLevel(logrus.DebugLevel)
			logs := logrustest.NewLocal(logger)
			stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				if !strings.Contains(trigger.GetPlayRewrite().GetRequestUrl(), canary) {
					t.Fatal("admission lost the viewer credential")
				}
				return &control.MistTriggerResult{Response: "live+stream"}, nil
			})
			ctx, _ := newWebhookContext(body)
			HandlePlayRewrite(ctx)
			for _, entry := range logs.AllEntries() {
				if strings.Contains(entry.Message, canary) || strings.Contains(fmt.Sprint(entry.Data), canary) {
					t.Fatal("viewer credential retained in a Helmsman log")
				}
			}
		})
	}
}

func TestUserNewMissingFoghornReplyIsUnavailable(t *testing.T) {
	for _, failure := range []error{nil, errors.New("foghorn unavailable")} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			setupTriggerTest(t, "tenant")
			stubSendMistTrigger(t, func(*ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
				return nil, failure
			})
			ctx, recorder := newWebhookContext("live+stream\n203.0.113.1\nprivate-viewer-token\nHLS\nhttps://edge/hls/live/index.m3u8\nsession")
			HandleUserNew(ctx)
			assertStatus(t, recorder, http.StatusServiceUnavailable)
			if strings.Contains(recorder.Body.String(), "private-viewer-token") {
				t.Fatal("unavailable admission exposed a credential")
			}
		})
	}
}
