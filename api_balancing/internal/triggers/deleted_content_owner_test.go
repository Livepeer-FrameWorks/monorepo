package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestDeletedContentDisconnectUsesAuthoritativeOwner(t *testing.T) {
	for _, kind := range []string{"live", "artifact"} {
		for _, assertedTenant := range []string{"", "10000000-0000-0000-0000-000000000001", "cluster-owner"} {
			t.Run(kind+"/"+assertedTenant, func(t *testing.T) {
				state.ResetDefaultManagerForTests()
				t.Cleanup(func() { state.ResetDefaultManagerForTests() })
				p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
				defer closeDB()
				object := &mediaauthoritypb.MediaObjectAuthority{}
				if err := proto.Unmarshal(objectBytes, object); err != nil {
					t.Fatal(err)
				}
				streamID := object.GetLiveStream().GetStreamId()
				object.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE
				name, hash := "live+stream-internal", ""
				if kind == "artifact" {
					name, hash = "vod+stream-internal", "artifact-hash"
					object.ObjectKind = mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
					object.Object = &mediaauthoritypb.MediaObjectAuthority_Artifact{Artifact: &mediaauthoritypb.ArtifactAuthority{
						ArtifactId: "artifact-id", ArtifactHash: hash, ParentStreamId: streamID,
						ArtifactKind: mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER,
					}}
				}
				tombstone, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				validUntil := time.Now().Add(time.Hour)
				for range 3 {
					expectLocalPair(mock, tombstone, tenantBytes, validUntil, validUntil, true)
				}
				capture, client := startDecklogCapture(t)
				p.decklogClient = client
				assertion := assertedTenant
				if _, _, err := p.handleUserEnd(&ipcpb.MistTrigger{
					TriggerType: string(mist.TriggerUserEnd), NodeId: "edge-1", TenantId: &assertion,
					TriggerPayload: &ipcpb.MistTrigger_ViewerDisconnect{ViewerDisconnect: &ipcpb.ViewerDisconnectTrigger{
						SessionId: "mist-viewer", StreamName: name, Connector: "HLS", Host: "192.0.2.10", Duration: 30,
					}},
				}); err != nil {
					t.Fatal(err)
				}
				forwarded := capture.received()
				if len(forwarded) != 1 {
					t.Fatalf("forwarded %d triggers, want one disconnect", len(forwarded))
				}
				got := forwarded[0]
				if got.GetTenantId() != object.GetTenantId() || got.GetViewerDisconnect().GetStreamId() != streamID || got.GetViewerDisconnect().GetArtifactHash() != hash {
					t.Fatalf("disconnect identity tenant=%q stream=%q artifact=%q, want %q/%q/%q", got.GetTenantId(), got.GetViewerDisconnect().GetStreamId(), got.GetViewerDisconnect().GetArtifactHash(), object.GetTenantId(), streamID, hash)
				}
				if decision, handled := p.EvaluateLocalPlaybackPolicy(context.Background(), "PlaybackKey", name, &ipcpb.ViewerConnectTrigger{}); !handled || decision != "false" {
					t.Fatalf("deleted content admitted playback: decision=%q handled=%v", decision, handled)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
