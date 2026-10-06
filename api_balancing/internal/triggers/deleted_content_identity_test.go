package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// Deleting a stream tombstones its local authority while Mist is still closing
// its viewers. Those viewers' USER_END reports still name the stream the
// tombstone identifies, so Periscope can close their sessions.
func TestUserEndAfterStreamDeletionCarriesTheTombstonedStream(t *testing.T) {
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	object.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
	object.GetLiveStream().SealedCellSecrets = nil
	tombstone, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	validUntil := time.Now().Add(time.Hour)
	for range 2 {
		expectLocalPair(mock, tombstone, tenantBytes, validUntil, validUntil, true)
	}
	capture, client := startDecklogCapture(t)
	p.decklogClient = client

	tenantID := object.GetTenantId()
	if _, _, err := p.handleUserEnd(&ipcpb.MistTrigger{
		TriggerType: string(mist.TriggerUserEnd),
		NodeId:      "edge-1",
		TenantId:    &tenantID,
		TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
			SessionId: "mist-viewer", StreamName: "live+stream-internal", Connector: "HLS", Host: "192.0.2.10", Duration: 30,
		}},
	}); err != nil {
		t.Fatalf("handleUserEnd: %v", err)
	}

	var forwarded []*ipcpb.ViewerDisconnectTrigger
	for _, trigger := range capture.received() {
		if disconnect := trigger.GetViewerDisconnect(); disconnect != nil {
			forwarded = append(forwarded, disconnect)
		}
	}
	if len(forwarded) != 1 || forwarded[0].GetStreamId() != object.GetLiveStream().GetStreamId() {
		t.Fatalf("USER_END after deletion forwarded %+v, want stream_id %s", forwarded, object.GetLiveStream().GetStreamId())
	}
}
