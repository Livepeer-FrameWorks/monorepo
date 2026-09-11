package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

type deniedManagedEffects struct{ t *testing.T }

func (m deniedManagedEffects) PopulateStreamContext(*commodorepb.ResolveStreamContextResponse) {
	m.t.Fatal("placement-denied stream populated caches")
}

func (m deniedManagedEffects) EnsureManagedStreamDVR(context.Context, *commodorepb.ResolveStreamContextResponse, string) {
	m.t.Fatal("placement-denied stream started DVR")
}

func TestManagedPlacementDenialPrecedesMaterializationAndApply(t *testing.T) {
	testDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testDB.Close() })
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	store, err := localauthority.NewStore(testDB, "cell", sharedauthority.TrustSet{"test": key})
	if err != nil {
		t.Fatal(err)
	}
	previousStore, previousEffects := LocalMediaAuthorityStore(), managedStreamMaterializer
	SetLocalMediaAuthorityStore(store)
	SetManagedStreamMaterializer(deniedManagedEffects{t: t})
	t.Cleanup(func() {
		SetLocalMediaAuthorityStore(previousStore)
		SetManagedStreamMaterializer(previousEffects)
	})
	const tenantID = "10000000-0000-0000-0000-000000000001"
	const streamID = "20000000-0000-0000-0000-000000000001"
	authorityID := sharedauthority.LiveStreamAuthorityID(streamID)
	tenant := &mediapb.TenantAuthority{SchemaVersion: 2, TenantId: tenantID,
		Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		MediaPlacement:         &pb.PolicySet{Revision: 1, Ingest: &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{ClusterIds: []string{"source"}}}}}},
		EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "source", ControlCellId: "cell", ClusterClass: "tenant_private", OwnerTenantId: tenantID, SubscriptionStatus: "active", MediaConsent: &pb.CapacityConsent{AllowIngest: true}}},
	}
	object := &mediapb.MediaObjectAuthority{SchemaVersion: 2, TenantId: tenantID, InternalName: "internal", ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, PlacementTenantRevision: 1, MediaPlacement: &pb.PolicySet{},
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: streamID, IngestMode: "mist_native"}},
	}
	tenantPayload, err := proto.Marshal(tenant)
	if err != nil {
		t.Fatal(err)
	}
	objectPayload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	tenantHash, objectHash := sha256.Sum256(tenantPayload), sha256.Sum256(objectPayload)
	row := &commodorepb.ManagedStreamRow{TenantId: tenantID, StreamId: streamID, InternalName: "internal", IngestMode: "mist_native", SourceSpec: "file:/input.ts", AllowedClusterIds: []string{"source"}, PlacementCount: 1}
	streamCtx := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: tenantID, StreamId: streamID, InternalName: "internal", IngestMode: "mist_native", IsRecordingEnabled: true}
	startFakeCommodoreServer(t, &fakeCommodoreInternal{streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
		return streamCtx, nil
	}})
	for _, path := range []string{"local", "connected", "apply"} {
		t.Run(path, func(t *testing.T) {
			now := time.Now()
			if path == "connected" {
				mock.ExpectQuery("SELECT").WithArgs("internal").WillReturnError(sql.ErrNoRows)
			}
			mock.ExpectQuery("SELECT object_authority.payload AS object_payload").WithArgs(tenantID, authorityID, "internal").WillReturnRows(sqlmock.NewRows([]string{
				"object_payload", "object_payload_sha256", "object_refresh_after", "object_valid_until", "object_authority_id", "object_authority_version", "object_read_ready", "object_ingest_ready", "object_source_ready",
				"tenant_payload", "tenant_payload_sha256", "tenant_refresh_after", "tenant_valid_until", "tenant_authority_version", "tenant_read_ready", "tenant_ingest_ready", "tenant_source_ready",
			}).AddRow(objectPayload, objectHash[:], now, now.Add(time.Minute), authorityID, int64(1), true, true, true, tenantPayload, tenantHash[:], now, now.Add(time.Minute), int64(1), true, true, true))
			if path == "apply" {
				if err := sendApplyManagedStream(context.Background(), logging.NewLogger(), "source", "node", row, streamCtx); err == nil || err.Error() != "managed-stream placement does not permit apply" {
					t.Fatalf("Apply bypassed placement: %v", err)
				}
			} else {
				var local map[string]*commodorepb.ResolveStreamContextResponse
				if path == "local" {
					local = map[string]*commodorepb.ResolveStreamContextResponse{streamID: streamCtx}
				}
				if _, status := materializeManagedStreamWithLocal(context.Background(), logging.NewLogger(), "source", "node", row, local); status != materializeDenied {
					t.Fatalf("materialization bypassed placement: %v", status)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
