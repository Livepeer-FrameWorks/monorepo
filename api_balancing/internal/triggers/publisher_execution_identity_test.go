package triggers

import (
	"errors"
	"testing"

	"frameworks/api_balancing/internal/ingesterrors"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestPublisherRejectsNoncanonicalExecutionBeforeClaim(t *testing.T) {
	for _, token := range []string{" execution", "execution ", "\texecution\n"} {
		t.Run(token, func(t *testing.T) {
			p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
			trigger := &ipcpb.MistTrigger{NodeId: "node", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{TriggerUuid: token, Pid: 1, TriggerUnixMillis: 1, StreamName: "credential"}}}
			_, abort, err := p.ProcessPublisher(t.Context(), trigger)
			var typed *ingesterrors.IngestError
			if !abort || !errors.As(err, &typed) {
				t.Fatalf("noncanonical execution must be a terminal malformed request: %v", err)
			}
			retryable, explicit := typed.RetryableOverride()
			if !explicit || retryable {
				t.Fatalf("noncanonical execution reached authority admission: %v", err)
			}
		})
	}
}
