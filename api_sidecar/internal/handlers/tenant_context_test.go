package handlers

import (
	"frameworks/api_sidecar/internal/control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
)

func TestNodeOwnerIsNotTriggerResourceTenant(t *testing.T) {
	setupTriggerTest(t, "node-owner")
	var forwarded *ipcpb.MistTrigger
	stubSendMistTrigger(t, func(trigger *ipcpb.MistTrigger) (*control.MistTriggerResult, error) {
		forwarded = trigger
		return &control.MistTriggerResult{}, nil
	})
	ctx, _ := newWebhookContext(processAVPayload("video", "5", "250", "250", "aac", "libopus", "0"))
	HandleProcessAVSegmentComplete(ctx)
	if forwarded == nil || forwarded.GetTenantId() != "" {
		t.Fatalf("node owner leaked into resource assertion: %+v", forwarded)
	}
}
