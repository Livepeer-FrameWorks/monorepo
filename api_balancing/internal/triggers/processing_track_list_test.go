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
// content series: its track-list reports stay out of Periscope, while a live
// stream's still reach it. Foghorn's own track state follows both.
func TestProcessingStreamTrackListStaysOutOfStreamAnalytics(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	const tenant, liveInternal, artifactHash = "tenant-tracks", "stream-tracks", "20261006024432101d684a081632c4"
	p.streamCache.Set(tenant+":"+liveInternal, streamContext{TenantID: tenant, StreamID: "11111111-1111-4111-8111-000000000021"}, time.Minute)
	p.streamCache.Set(tenant+":"+artifactHash, streamContext{TenantID: tenant, ArtifactHash: artifactHash}, time.Minute)

	trackList := func(streamName string) {
		t.Helper()
		tenantID := tenant
		trigger := &ipcpb.MistTrigger{
			TriggerType: string(mist.TriggerLiveTrackList),
			NodeId:      "edge-1",
			TenantId:    &tenantID,
			TriggerPayload: &ipcpb.MistTrigger_TrackList{TrackList: &ipcpb.StreamTrackListTrigger{
				StreamName: streamName,
			}},
		}
		if _, _, err := p.handleLiveTrackList(trigger); err != nil {
			t.Fatalf("handleLiveTrackList(%s): %v", streamName, err)
		}
	}
	trackList("live+" + liveInternal)
	trackList("processing+" + artifactHash)

	forwarded := map[string]bool{}
	for _, trigger := range capture.received() {
		if report := trigger.GetTrackList(); report != nil {
			forwarded[report.GetStreamName()] = true
		}
	}
	if !forwarded["live+"+liveInternal] {
		t.Fatalf("the live stream's track list did not reach Periscope: %v", forwarded)
	}
	if forwarded["processing+"+artifactHash] {
		t.Fatalf("the processing input's track list reached Periscope without content identity: %v", forwarded)
	}
	if state.DefaultManager().GetStreamState(artifactHash) == nil {
		t.Fatalf("Foghorn's track state did not follow the processing input's track list")
	}
}

// A track list for a live stream that does not resolve names no content, so
// Periscope would drop it; Foghorn does not forward it.
func TestUnresolvedLiveStreamTrackListIsNotForwarded(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	capture, client := startDecklogCapture(t)
	p := NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
	p.decklogClient = client
	tenantID := "tenant-tracks-unresolved"
	if _, _, err := p.handleLiveTrackList(&ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerLiveTrackList),
		NodeId:      "edge-1",
		TenantId:    &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_TrackList{TrackList: &ipcpb.StreamTrackListTrigger{
			StreamName: "live+unknown-key",
		}},
	}); err != nil {
		t.Fatalf("handleLiveTrackList: %v", err)
	}
	if got := capture.received(); len(got) != 0 {
		t.Fatalf("an unresolved stream's track list reached Decklog: %v", got[0].GetTrackList())
	}
}
