package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A processing+ stream is the internal transcode input of an artifact, not a
// content series: its buffer reports stay out of Periscope, while a live
// stream's still reach it.
func TestProcessingStreamBufferStaysOutOfStreamAnalytics(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	const tenant, liveInternal, artifactHash = "tenant-buffer", "stream-buffer", "20261006024432101d684a081632c3"
	p.streamCache.Set(tenant+":"+liveInternal, streamContext{TenantID: tenant, StreamID: "11111111-1111-4111-8111-000000000020"}, time.Minute)
	p.streamCache.Set(tenant+":"+artifactHash, streamContext{TenantID: tenant, ArtifactHash: artifactHash}, time.Minute)

	buffer := func(streamName string) {
		t.Helper()
		tenantID := tenant
		trigger := &ipcpb.MistTrigger{
			TriggerType: string(mist.TriggerStreamBuffer),
			NodeId:      "edge-1",
			TenantId:    &tenantID,
			TriggerPayload: &ipcpb.MistTrigger_StreamBuffer{StreamBuffer: &ipcpb.StreamBufferTrigger{
				StreamName: streamName, BufferState: "FULL",
			}},
		}
		if _, _, err := p.handleStreamBuffer(trigger); err != nil {
			t.Fatalf("handleStreamBuffer(%s): %v", streamName, err)
		}
	}
	buffer("live+" + liveInternal)
	buffer("processing+" + artifactHash)

	forwarded := map[string]bool{}
	for _, trigger := range capture.received() {
		if report := trigger.GetStreamBuffer(); report != nil {
			forwarded[report.GetStreamName()] = true
		}
	}
	if !forwarded["live+"+liveInternal] {
		t.Fatalf("the live stream's buffer report did not reach Periscope: %v", forwarded)
	}
	if forwarded["processing+"+artifactHash] {
		t.Fatalf("the processing input's buffer report reached Periscope without content identity: %v", forwarded)
	}
}
