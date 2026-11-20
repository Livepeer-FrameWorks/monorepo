package mediaauthority

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/proto"
)

func TestManagedRecordingPreservesSignedChapterPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		interval int32
	}{{"fixed_interval", 86400}, {"none", 0}} {
		t.Run(tc.mode, func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", key.PublicKey()), key); err != nil {
				t.Fatal(err)
			}
			const tenant = "10000000-0000-0000-0000-000000000001"
			const stream = "20000000-0000-0000-0000-000000000001"
			id := sharedauthority.LiveStreamAuthorityID(stream)
			object := sealedObjectFixture(t, key, id, &mediapb.LiveStreamSecret{AuthorityId: id, TenantId: tenant, NativeSourceSpec: "/media/source.mp4", NativeSourceKind: "file", NativeAlwaysOn: true, NativePlacementCount: 1, NativeAllowedClusterIds: []string{"cell-a"}}, nil)
			live := object.GetLiveStream()
			live.IngestMode = "mist_native"
			live.RecordingEnabled = true
			live.PublisherConfigurationVersion = 1
			live.DvrChapterMode = tc.mode
			live.DvrChapterIntervalSeconds = tc.interval
			payload, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			until := store.now().Add(time.Hour)
			mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until", "local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).AddRow(id, 2, payload, digest[:], until, until, true, false, 3))
			policy := &sharedpb.DVRPolicy{DefaultWindowSeconds: 1800, MaxWindowSeconds: 3600}
			owner := &mediapb.TenantAuthority{SchemaVersion: sharedauthority.SchemaVersion, TenantId: tenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cell-a"}}, DvrPolicy: policy}
			raw, err := proto.Marshal(owner)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs(tenant).WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).AddRow(raw, sum[:], until, until, 3, true))
			set, err := store.ManagedStreams(context.Background(), "cell-a")
			if err != nil || !set.Marked || !set.Complete || set.Contexts[stream] == nil {
				t.Fatalf("managed set unavailable: %+v err=%v", set, err)
			}
			got := set.Contexts[stream]
			if got.GetDvrChapterMode() != tc.mode || got.GetDvrChapterIntervalSeconds() != tc.interval {
				t.Errorf("signed policy %s/%d became %q/%d in managed DVR context", tc.mode, tc.interval, got.GetDvrChapterMode(), got.GetDvrChapterIntervalSeconds())
			}
			if !proto.Equal(got.GetDvrPolicy(), policy) {
				t.Error("tenant DVR window policy changed")
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManagedShadowRejectsDifferentChapterPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     string
		interval int32
	}{
		{"mode", "none", 0},
		{"interval", "fixed_interval", 7200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", key.PublicKey()), key); err != nil {
				t.Fatal(err)
			}
			const tenant = "10000000-0000-0000-0000-000000000001"
			const stream = "20000000-0000-0000-0000-000000000001"
			id := sharedauthority.LiveStreamAuthorityID(stream)
			secret := &mediapb.LiveStreamSecret{AuthorityId: id, TenantId: tenant, NativeSourceSpec: "/media/source.mp4", NativeSourceKind: "file", NativeAlwaysOn: true, NativePlacementCount: 1, NativeAllowedClusterIds: []string{"cell-a"}}
			object := sealedObjectFixture(t, key, id, secret, nil)
			live := object.GetLiveStream()
			live.IngestMode = "mist_native"
			live.RecordingEnabled = true
			live.PublisherConfigurationVersion = 1
			live.DvrChapterMode = "fixed_interval"
			live.DvrChapterIntervalSeconds = 86400
			payload, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			until := store.now().Add(time.Hour)
			mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).AddRow(payload, digest[:], until, until, id, 2, false, false, 3))
			owner := &mediapb.TenantAuthority{SchemaVersion: sharedauthority.SchemaVersion, TenantId: tenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cell-a"}}}
			raw, err := proto.Marshal(owner)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(raw)
			mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).AddRow(raw, sum[:], until, until, 3, true))
			row := &commodorepb.ManagedStreamRow{StreamId: stream, PlaybackId: object.GetPlaybackId(), InternalName: object.GetInternalName(), TenantId: tenant, IngestMode: "mist_native", SourceSpec: secret.GetNativeSourceSpec(), SourceKind: secret.GetNativeSourceKind(), AlwaysOn: true, PlacementCount: 1, AllowedClusterIds: []string{"cell-a"}}
			connected := &commodorepb.ResolveStreamContextResponse{Admitted: true, StreamId: stream, PlaybackId: object.GetPlaybackId(), InternalName: object.GetInternalName(), TenantId: tenant, UserId: object.GetUserId(), IngestMode: "mist_native", IsRecordingEnabled: true, ProcessesJson: live.GetProcessesJson(), DvrProcessesJson: live.GetDvrProcessesJson(), BillingModel: localBillingModel(owner.GetBillingModel()), AuthorityClusterPeers: TenantClusterPeers(owner), TenantResourceLimits: clusterResourceLimits(owner, "cell-a"), DvrChapterMode: tc.mode, DvrChapterIntervalSeconds: tc.interval}
			got, err := store.PromoteManagedStreamIfMatching(context.Background(), "cell-a", row, connected)
			if err != nil || got != ManagedStreamPromotionMismatch {
				t.Fatalf("different chapter interval must reject shadow promotion before writes: outcome=%s err=%v", got, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
