package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// An uploaded VOD being played has a tenant and an artifact but no stream. Its
// lifecycle report is forwarded under the artifact Foghorn resolved, like its
// viewer and QoE events, instead of being dropped as a report missing its
// stream_id.
func TestUploadedVODLifecycleIsForwardedUnderItsArtifact(t *testing.T) {
	installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	const tenant = "tenant-vod"
	const artifactHash = "20261005175800a1b2c3d4e5f60718"
	p.streamCache.Set(tenant+":vodInternal", streamContext{TenantID: tenant, ArtifactHash: artifactHash}, time.Minute)

	tenantID, inputs := tenant, uint32(1)
	if _, _, err := p.handleStreamLifecycleUpdate(&ipcpb.MistTrigger{
		TriggerType: "STREAM_LIFECYCLE_UPDATE",
		NodeId:      "edge-1",
		TriggerPayload: &ipcpb.MistTrigger_StreamLifecycleUpdate{StreamLifecycleUpdate: &ipcpb.StreamLifecycleUpdate{
			TenantId: &tenantID, NodeId: "edge-1", InternalName: "vod+vodInternal", Status: "live", TotalInputs: &inputs,
		}},
	}); err != nil {
		t.Fatalf("handleStreamLifecycleUpdate: %v", err)
	}

	var forwarded []*ipcpb.MistTrigger
	for _, trigger := range capture.received() {
		if trigger.GetStreamLifecycleUpdate() != nil {
			forwarded = append(forwarded, trigger)
		}
	}
	if len(forwarded) != 1 {
		t.Fatalf("uploaded VOD lifecycle forwarded %d time(s), want 1", len(forwarded))
	}
	if got := forwarded[0].GetArtifactHash(); got != artifactHash {
		t.Fatalf("forwarded artifact_hash = %q, want %q", got, artifactHash)
	}
	if got := forwarded[0].GetStreamId(); got != "" {
		t.Fatalf("uploaded VOD lifecycle carries stream_id %q", got)
	}
}
