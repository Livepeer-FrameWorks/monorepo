package triggers

import (
	"context"
	"slices"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func grantTestReplica(t *testing.T, server *miniredis.Miniredis, instanceID string) *state.StreamStateManager {
	t.Helper()
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	sm := state.NewStreamStateManager()
	if err := sm.EnableRedisSync(context.Background(), state.NewRedisStateStore(client, "cell-a"), instanceID, testLogger()); err != nil {
		t.Fatalf("EnableRedisSync %s: %v", instanceID, err)
	}
	t.Cleanup(sm.Shutdown)
	return sm
}

// A signing key revoked through one Foghorn replica reaches an edge whose
// grant (and control stream) belongs to another replica. The applying
// replica holds no edge for the stream and pushes nothing; it announces the
// apply on the replicas' shared changelog, and the replica holding the edge
// pushes the new grant, without the revoked key, over its own stream.
func TestKeyRevokedOnOneReplicaReachesTheEdgeGrantedByAnother(t *testing.T) {
	server := miniredis.RunT(t)
	replica1 := grantTestReplica(t, server, "foghorn-1")
	replica2 := grantTestReplica(t, server, "foghorn-2")

	p1, mock1, close1, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer close1()
	p2, mock2, close2, _, _ := localAuthorityFixture(t)
	defer close2()

	edges1 := newRecordingGrantDelivery()
	edges1.held = []string{"live+stream-internal"}
	edges1.holders["live+stream-internal"] = []string{"edge-node-1"}
	edges2 := newRecordingGrantDelivery()
	p1.SetPlaybackGrantDelivery(edges1)
	p2.SetPlaybackGrantDelivery(edges2)
	p1.SetPlaybackAuthorityBus(replica1)
	p2.SetPlaybackAuthorityBus(replica2)
	replica1.SetPlaybackAuthorityChangeHandler(p1.HandlePeerPlaybackAuthorityChange)
	replica2.SetPlaybackAuthorityChangeHandler(p2.HandlePeerPlaybackAuthorityChange)

	// Replica 1 reads the revoked authority from the cell database the
	// applying replica committed it to: only kid-2 is still active.
	revoked := &mediaauthoritypb.PlaybackPolicy{
		Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT,
		Jwt: &mediaauthoritypb.PlaybackJwtPolicy{
			ActiveKeys: []*mediaauthoritypb.PlaybackSigningKey{{KeyId: "kid-2", PublicKeyPem: "pem-2"}},
		},
	}
	validUntil := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
	expectLocalPair(mock1, withObjectPolicy(t, objectBytes, revoked, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE), tenantBytes, validUntil, validUntil, true)

	p2.onPlaybackAuthorityApplied(localauthority.ApplyResult{
		Kind: "media_object", InternalName: "stream-internal", TenantID: "10000000-0000-0000-0000-000000000001", Version: 5,
	})

	offers := edges1.waitOffers(t, 1)
	if offers[0].nodeID != "edge-node-1" {
		t.Fatalf("grant pushed to %s", offers[0].nodeID)
	}
	var kids []string
	for _, key := range offers[0].grant.GetPolicy().GetActiveKeys() {
		kids = append(kids, key.GetKid())
	}
	if !slices.Equal(kids, []string{"kid-2"}) {
		t.Fatalf("pushed grant keys = %v, want the revoked key gone", kids)
	}
	edges2.mu.Lock()
	pushedByApplier := len(edges2.offers)
	edges2.mu.Unlock()
	if pushedByApplier != 0 {
		t.Fatalf("the applying replica pushed %d grants to edges it does not hold", pushedByApplier)
	}
	if err := mock1.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := mock2.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
