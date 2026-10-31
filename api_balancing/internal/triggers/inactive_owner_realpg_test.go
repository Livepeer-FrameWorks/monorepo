//go:build schema_verify

package triggers

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"strings"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

// The real projection is populated by signed objects, so retaining the original
// credential in the owner-inactive replacement makes the refusal assertion fail.
func testInactiveOwnerSignedPermissionRefusesOutageIngestAndPreservesPlayback(t *testing.T, db *sql.DB) {
	t.Helper()

	installIngestSessionMintMockWithStream(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.SetNodeConnectionInfo(context.Background(), "edge-node-1", "edge-node-1:18090", "", "cluster-a", nil)
	previousRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(previousRegistry) })
	p, _, closeFixture, tenantBytes, objectBytes := localAuthorityFixture(t)
	defer closeFixture()
	var tenant mediaauthoritypb.TenantAuthority
	var object mediaauthoritypb.MediaObjectAuthority
	if decodeErr := proto.Unmarshal(tenantBytes, &tenant); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if decodeErr := proto.Unmarshal(objectBytes, &object); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	private := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	store, storeErr := localauthority.NewStore(db, "cell-a", sharedauthority.TrustSet{"key": private.Public().(ed25519.PublicKey)})
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	p.SetMediaAuthorityStore(store)
	admitIngestPlacementForTest(t, p)
	now := time.Now().UTC().Add(-time.Minute)
	apply := func(kind mediaauthoritypb.AuthorityKind, id string, version uint64, payload proto.Message) {
		t.Helper()
		envelope, envelopeErr := sharedauthority.NewEnvelope(kind, id, version, now, now.Add(10*time.Minute), now.Add(time.Hour), "key", "cell-a", payload, []*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "fixture"}})
		if envelopeErr != nil {
			t.Fatal(envelopeErr)
		}
		signed, signErr := sharedauthority.Sign(envelope, private)
		if signErr != nil {
			t.Fatal(signErr)
		}
		encoded, encodeErr := proto.Marshal(signed)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if _, applyErr := store.Apply(t.Context(), encoded); applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	id := sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().GetStreamId())
	apply(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_TENANT, tenant.GetTenantId(), 1, &tenant)
	apply(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, id, 1, &object)
	if ready, readyErr := store.MarkPlaybackPairLocalReadReady(t.Context(), tenant.GetTenantId(), 1, id, 1); readyErr != nil || !ready {
		t.Fatalf("playback readiness=%v,%v", ready, readyErr)
	}
	if ready, readyErr := store.MarkIngestPairLocalReady(t.Context(), tenant.GetTenantId(), 1, id, 1); readyErr != nil || !ready {
		t.Fatalf("ingest readiness=%v,%v", ready, readyErr)
	}
	if prior, lookupErr := store.MediaObjectByPublishingCredential(t.Context(), "sk_local"); lookupErr != nil || !prior.IngestReady {
		t.Fatalf("positive credential control=%v,%v", prior, lookupErr)
	}
	trigger := &ipcpb.MistTrigger{NodeId: "edge-node-1", TriggerPayload: &ipcpb.MistTrigger_PushRewrite{PushRewrite: &ipcpb.PushRewriteTrigger{Pid: 4242, TriggerUuid: "inactive-owner-trigger", TriggerUnixMillis: 1, StreamName: "sk_local", PushUrl: "rtmp://example/live/sk_local", ObservedConnector: "RTMP"}}}
	positive, positiveBlocking, positiveErr := p.handlePushRewrite(trigger)
	if positiveErr != nil || positive != "live+stream-internal" || positiveBlocking {
		t.Fatalf("active owner's outage ingest must work: stream=%q blocking=%v err=%v", positive, positiveBlocking, positiveErr)
	}
	object.GetLiveStream().PublishingCredentialSha256 = nil
	apply(mediaauthoritypb.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT, id, 2, &object)
	if ready, err := store.MarkIngestPairLocalReady(t.Context(), tenant.GetTenantId(), 1, id, 2); err != nil || !ready {
		t.Fatalf("inactive object ingest readiness=%v,%v", ready, err)
	}
	if _, lookupErr := store.MediaObjectByPublishingCredential(t.Context(), "sk_local"); !errors.Is(lookupErr, sql.ErrNoRows) {
		t.Fatalf("inactive replacement retained its credential: %v", lookupErr)
	}

	trigger.GetPushRewrite().TriggerUuid = "inactive-owner-new-connection"
	trigger.GetPushRewrite().Pid = 4243
	stream, blocking, pushErr := p.handlePushRewrite(trigger)
	if pushErr == nil || stream != "" || !blocking || !strings.Contains(strings.ToLower(pushErr.Error()), "failed to validate stream key") {
		t.Fatalf("outage ingest: stream=%q blocking=%v err=%v", stream, blocking, pushErr)
	}
	result, found, readErr := p.readReadyLocalPlayback(t.Context(), "playbackkey", true)
	if readErr != nil || !found || result.target == nil {
		t.Fatalf("inactive-owner playback=%+v found=%v err=%v", result, found, readErr)
	}
	if p.commodoreClient != nil {
		t.Fatal("fixture unexpectedly has a connected Commodore")
	}
}
