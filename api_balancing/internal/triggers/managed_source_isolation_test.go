package triggers

import (
	"crypto/ecdh"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/appconfig"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestManagedSourceOutageIsIsolatedFromOtherTenants(t *testing.T) {
	for _, lookup := range []string{"stream-internal", "PlaybackKey", "spent-stream"} {
		t.Run(lookup, func(t *testing.T) {
			settings := &appconfig.Foghorn{BalancerCapabilitySecret: "test-secret"}
			settings.PublicBaseURL = "https://foghorn.example"
			t.Cleanup(appconfig.Install(func() *appconfig.Foghorn { return settings }))
			sm := state.ResetDefaultManagerForTests()
			t.Cleanup(sm.Shutdown)
			sm.SetNodeConnectionInfo(t.Context(), "edge", "", "owner", "cluster-a", nil)
			prior := control.StreamRegistryInstance
			control.SetStreamRegistry(nil)
			t.Cleanup(func() { control.SetStreamRegistry(prior) })
			p, mock, closeDB, tenantBytes, objectBytes := localAuthorityFixture(t)
			t.Cleanup(closeDB)
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			keyID := sharedauthority.SealRecipientKeyID("cell-a", key.PublicKey())
			if err = p.mediaAuthorityStore.SetSealPrivateKey(keyID, key); err != nil {
				t.Fatal(err)
			}
			object := &mediapb.MediaObjectAuthority{}
			if err = proto.Unmarshal(objectBytes, object); err != nil {
				t.Fatal(err)
			}
			object.GetLiveStream().IngestMode = "mist_native"
			authorityID := sharedauthority.LiveStreamAuthorityID(object.GetLiveStream().StreamId)
			secret, err := proto.Marshal(&mediapb.LiveStreamSecret{AuthorityId: authorityID, TenantId: object.TenantId, NativeSourceSpec: "/media/paid.mp4", NativeSourceKind: "file", NativeAllowedClusterIds: []string{"cluster-a"}})
			if err != nil {
				t.Fatal(err)
			}
			box, err := sharedauthority.SealSecret("cell-a", authorityID, sharedauthority.SealRecipient{KeyID: keyID, PublicKey: key.PublicKey()}, secret)
			if err != nil {
				t.Fatal(err)
			}
			object.GetLiveStream().SealedCellSecrets = []*mediapb.SealedCellSecret{box}
			objectBytes, err = proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			spent := proto.Clone(object).(*mediapb.MediaObjectAuthority)
			spent.TenantId = "spent-tenant"
			spent.GetLiveStream().StreamId = "30000000-0000-0000-0000-000000000002"
			spent.InternalName = "spent-stream"
			spent.PlaybackId = "spent-playback"
			spentBytes, err := proto.Marshal(spent)
			if err != nil {
				t.Fatal(err)
			}
			spentTenant := &mediapb.TenantAuthority{TenantId: "spent-tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingModel: mediapb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED, DecisionReason: sharedauthority.ReasonPrepaidBalanceUnavailable}
			spentTenantBytes, err := proto.Marshal(spentTenant)
			if err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(time.Hour)
			mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until", "local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).
				AddRow(authorityID, 2, objectBytes, localPayloadDigest(objectBytes), until, until, true, false, 2).
				AddRow(sharedauthority.LiveStreamAuthorityID(spent.GetLiveStream().StreamId), 2, spentBytes, localPayloadDigest(spentBytes), until, until, true, false, 2))
			for _, tenant := range []struct {
				id      string
				payload []byte
			}{{object.TenantId, tenantBytes}, {spent.TenantId, spentTenantBytes}} {
				mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs(tenant.id).WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).AddRow(tenant.payload, localPayloadDigest(tenant.payload), until, until, 2, true))
			}
			got, abort, err := p.handleStreamSource(&ipcpb.MistTrigger{NodeId: "edge", TriggerType: "STREAM_SOURCE", TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: lookup}}})
			if err != nil || abort {
				t.Fatalf("source lookup failed: got=%s abort=%v err=%v", got, abort, err)
			}
			if lookup == "spent-stream" {
				if got != control.OfflineUnavailable {
					t.Fatalf("spent tenant unexpectedly resolved: %s", got)
				}
			} else if !strings.HasPrefix(got, "balance:https://foghorn.example/") {
				t.Fatalf("another tenant's spent balance blocked a valid local source: %s", got)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
