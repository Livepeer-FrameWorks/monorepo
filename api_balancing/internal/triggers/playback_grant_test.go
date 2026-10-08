package triggers

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"

	"google.golang.org/protobuf/proto"
)

type grantOffer struct {
	nodeID string
	grant  *ipcpb.PlaybackGrant
}

type recordingGrantDelivery struct {
	mu      sync.Mutex
	offers  []grantOffer
	offered chan struct{}
	holders map[string][]string
	held    []string
}

func newRecordingGrantDelivery() *recordingGrantDelivery {
	return &recordingGrantDelivery{offered: make(chan struct{}, 16), holders: map[string][]string{}}
}

func (d *recordingGrantDelivery) Offer(nodeID string, grant *ipcpb.PlaybackGrant) error {
	d.mu.Lock()
	d.offers = append(d.offers, grantOffer{nodeID: nodeID, grant: proto.CloneOf(grant)})
	d.mu.Unlock()
	d.offered <- struct{}{}
	return nil
}

func (d *recordingGrantDelivery) Holders(internalName string) []string {
	return d.holders[internalName]
}

func (d *recordingGrantDelivery) HeldStreams(string, string) []string { return d.held }

func (d *recordingGrantDelivery) waitOffers(t *testing.T, n int) []grantOffer {
	t.Helper()
	for range n {
		select {
		case <-d.offered:
		case <-time.After(5 * time.Second):
			t.Fatalf("waited for %d grant offers", n)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.offers)
}

func withObjectPolicy(t *testing.T, objectBytes []byte, policy *mediaauthoritypb.PlaybackPolicy, lifecycle mediaauthoritypb.AuthorityLifecycle) []byte {
	t.Helper()
	object := &mediaauthoritypb.MediaObjectAuthority{}
	if err := proto.Unmarshal(objectBytes, object); err != nil {
		t.Fatal(err)
	}
	object.PlaybackPolicy = policy
	object.Lifecycle = lifecycle
	out, err := proto.MarshalOptions{Deterministic: true}.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Foghorn admits a viewer on signed authority and gives the serving edge the
// stream's grant in the same exchange. Its validity is the authority's own:
// the earlier of the object's and the tenant's hard validity.
func TestPlayRewriteOffersTheStreamsGrantToTheServingEdge(t *testing.T) {
	objectValid := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	tenantValid := time.Now().Add(time.Hour).Truncate(time.Second)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalPair(mock, objectBytes, tenantBytes, objectValid, tenantValid, true)
	expectLocalTenant(mock, tenantBytes, tenantValid, true)
	connected, cleanup, _ := setupCommodoreClientWithStub(t, nil, nil)
	t.Cleanup(cleanup)
	p.SetCommodoreClient(connected)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "cluster-a", nil)
	admitViewerPlacementForTest(t, p)
	delivery := newRecordingGrantDelivery()
	p.SetPlaybackGrantDelivery(delivery)

	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PlayRewrite{PlayRewrite: &ipcpb.ViewerResolveTrigger{
		RequestedStream: "PlaybackKey", ViewerHost: "192.0.2.10", OutputType: "HLS", RequestUrl: "https://edge/hls/PlaybackKey/index.m3u8",
	}}}
	if response, abort, err := p.handlePlayRewrite(trigger); err != nil || abort || response != "live+stream-internal" {
		t.Fatalf("PLAY_REWRITE = %q %v %v", response, abort, err)
	}
	offers := delivery.waitOffers(t, 1)
	if len(offers) != 1 || offers[0].nodeID != "edge-node-1" {
		t.Fatalf("offers = %+v, want one to the serving edge", offers)
	}
	grant := offers[0].grant
	if grant.GetInternalName() != "live+stream-internal" || !slices.Equal(grant.GetRequestedNames(), []string{"PlaybackKey"}) ||
		grant.GetTenantId() == "" || grant.GetObjectAuthorityVersion() != 4 || grant.GetTenantAuthorityVersion() != 8 ||
		grant.GetPolicy().GetKind() != ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_PUBLIC {
		t.Fatalf("grant = %+v", grant)
	}
	if got := grant.GetValidUntil().AsTime(); !got.Equal(tenantValid) {
		t.Fatalf("valid_until = %v, want the earlier (tenant) validity %v", got, tenantValid)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An authority apply reaches every edge holding the stream's grant with one
// push: a renewal extends the validity, a policy change carries the new
// policy (a revoked signing key is absent from the active keys), and a
// tombstone revokes the grant.
func TestAuthorityApplyPushesTheNewGrantToHoldingEdges(t *testing.T) {
	jwtPolicy := &mediaauthoritypb.PlaybackPolicy{
		Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT,
		Jwt: &mediaauthoritypb.PlaybackJwtPolicy{
			AllowedKeyIds: []string{"kid-2"},
			ActiveKeys:    []*mediaauthoritypb.PlaybackSigningKey{{KeyId: "kid-2", PublicKeyPem: "pem-2"}},
		},
		AllowedOrigins: []string{"https://site.example"},
	}
	webhookPolicy := &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK}
	for _, tc := range []struct {
		name      string
		policy    *mediaauthoritypb.PlaybackPolicy
		lifecycle mediaauthoritypb.AuthorityLifecycle
		check     func(t *testing.T, grant *ipcpb.PlaybackGrant, validUntil time.Time)
	}{
		{"renewal", &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			func(t *testing.T, grant *ipcpb.PlaybackGrant, validUntil time.Time) {
				if grant.GetRevoked() || !grant.GetValidUntil().AsTime().Equal(validUntil) {
					t.Fatalf("renewed grant = %+v, want valid until %v", grant, validUntil)
				}
			}},
		{"key revoked", jwtPolicy, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			func(t *testing.T, grant *ipcpb.PlaybackGrant, _ time.Time) {
				policy := grant.GetPolicy()
				if policy.GetKind() != ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_JWT || len(policy.GetActiveKeys()) != 1 ||
					policy.GetActiveKeys()[0].GetKid() != "kid-2" || !slices.Equal(policy.GetAllowedKids(), []string{"kid-2"}) ||
					!slices.Equal(policy.GetAllowedOrigins(), []string{"https://site.example"}) {
					t.Fatalf("policy = %+v", policy)
				}
			}},
		{"webhook policy", webhookPolicy, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			func(t *testing.T, grant *ipcpb.PlaybackGrant, _ time.Time) {
				if grant.GetPolicy().GetKind() != ipcpb.PlaybackGrantPolicyKind_PLAYBACK_GRANT_POLICY_KIND_CONNECTED {
					t.Fatalf("webhook policy reached the edge as %v", grant.GetPolicy().GetKind())
				}
			}},
		{"tombstone", nil, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE,
			func(t *testing.T, grant *ipcpb.PlaybackGrant, _ time.Time) {
				if !grant.GetRevoked() || grant.GetInternalName() != "live+stream-internal" {
					t.Fatalf("tombstoned grant = %+v", grant)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validUntil := time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second)
			p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
			defer closeDB()
			expectLocalPair(mock, withObjectPolicy(t, objectBytes, tc.policy, tc.lifecycle), tenantBytes, validUntil, validUntil, true)
			delivery := newRecordingGrantDelivery()
			delivery.held = []string{"live+stream-internal"}
			delivery.holders["live+stream-internal"] = []string{"edge-node-1", "edge-node-2"}
			p.SetPlaybackGrantDelivery(delivery)

			p.onPlaybackAuthorityApplied(localauthority.ApplyResult{Kind: "media_object", InternalName: "stream-internal", Version: 5})
			offers := delivery.waitOffers(t, 2)
			if offers[0].nodeID != "edge-node-1" || offers[1].nodeID != "edge-node-2" {
				t.Fatalf("offers went to %s and %s", offers[0].nodeID, offers[1].nodeID)
			}
			tc.check(t, offers[0].grant, validUntil)
		})
	}
}

// A tenant apply (billing, suspension, cluster grants) reaches the edges of
// every stream of the tenant this instance granted.
func TestTenantApplyPushesEachGrantedStream(t *testing.T) {
	validUntil := time.Now().Add(time.Hour).Truncate(time.Second)
	p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeDB()
	expectLocalPair(mock, objectBytes, tenantBytes, validUntil, validUntil, true)
	delivery := newRecordingGrantDelivery()
	delivery.held = []string{"live+stream-internal"}
	delivery.holders["live+stream-internal"] = []string{"edge-node-1"}
	p.SetPlaybackGrantDelivery(delivery)
	p.onPlaybackAuthorityApplied(localauthority.ApplyResult{Kind: "tenant", TenantID: "10000000-0000-0000-0000-000000000001"})
	if offers := delivery.waitOffers(t, 1); offers[0].grant.GetInternalName() != "live+stream-internal" {
		t.Fatalf("tenant apply pushed %+v", offers[0].grant)
	}
}
