package playbackgrant

import (
	"testing"

	"frameworks/api_sidecar/internal/playbackgrant/playbackgranttest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// After a restart, viewers behind one address are several live Mist sessions
// the edge cannot tell apart by token; each of their tokens is still answered
// locally rather than sending the whole address to Foghorn.
func TestRebuiltSessionsBehindOneAddressAreAnsweredLocally(t *testing.T) {
	store, _ := newTestStore(t, &recordingMist{}, nil)
	store.rebuildFrom([]mist.ViewerSession{
		{SessionID: "a", Host: "192.0.2.1", Stream: "live+s", Protocol: "HLS"},
		{SessionID: "b", Host: "192.0.2.1", Stream: "live+s", Protocol: "HLS"},
	})
	store.ApplyGrant(playbackgranttest.Grant("live+s", ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC, nil, "pb"))
	for _, token := range []string{"t1", "t2"} {
		if _, ok := store.ServeAdmitted("pb", "192.0.2.1", "HLS", "http://e/hls/pb/0.ts?tkn="+token); !ok {
			t.Fatalf("token %s from the shared address was not answered locally", token)
		}
	}
	if _, ok := store.ServeAdmitted("pb", "192.0.2.9", "HLS", "http://e/hls/pb/0.ts?tkn=t3"); ok {
		t.Fatal("an address with no live session was answered locally")
	}
}
