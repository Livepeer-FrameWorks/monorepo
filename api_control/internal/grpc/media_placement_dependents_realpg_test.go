//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"frameworks/api_control/internal/database/commodoredb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

const activationStream = "8f5d2c11-0000-4000-8000-00000000ab02"

// seedPlacementDependent publishes one live-stream authority version of the
// activation tenant, compiled against parent placement revision parent and
// still being delivered to every cell, together with the renewal row that is
// the tenant's index into its objects.
func seedPlacementDependent(t *testing.T, db *sql.DB, version int64, parent uint64, cells []string) {
	t.Helper()
	ctx := context.Background()
	authorityID := sharedauthority.LiveStreamAuthorityID(activationStream)
	payload, err := proto.Marshal(&mediapb.MediaObjectAuthority{
		SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: activationTenant, InternalName: "dependent",
		Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		MediaPlacement: &placementpb.PolicySet{Revision: 1}, PlacementTenantRevision: parent,
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: activationStream, IngestMode: "push"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_versions
		  (authority_kind, authority_id, authority_version, payload_schema_version, payload,
		   payload_sha256, source_revisions, issued_at, refresh_after, valid_until)
		SELECT 'media_object', $1, $2, $3, $4::bytea, sha256($4::bytea), '[]'::jsonb, NOW(), NOW() + INTERVAL '1 hour', NOW() + INTERVAL '2 hours'`,
		authorityID, version, sharedauthority.PlacementSchemaVersion, payload); err != nil {
		t.Fatalf("seed object version: %v", err)
	}
	if _, err = db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_current (authority_kind, authority_id, authority_version)
		VALUES ('media_object', $1, $2)
		ON CONFLICT (authority_kind, authority_id) DO UPDATE SET authority_version = EXCLUDED.authority_version, updated_at = NOW()`,
		authorityID, version); err != nil {
		t.Fatalf("seed current object: %v", err)
	}
	for _, cell := range cells {
		if _, err = db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_deliveries
			  (authority_kind, authority_id, authority_version, cell_id, signed_envelope, status)
			VALUES ('media_object', $1, $2, $3, '\x00'::bytea, 'delivering')`, authorityID, version, cell); err != nil {
			t.Fatalf("seed object delivery for %s: %v", cell, err)
		}
	}
	if _, err = db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_refresh_obligations
		  (target_key, lane, tenant_id, target_kind, status, last_reason, last_source_service, last_source_event_id)
		VALUES ('media_object:' || $1, 'object_deadline', $2::uuid, 'live_stream', 'pending', 'renewal', 'commodore', 'seed')
		ON CONFLICT (target_key, lane) DO NOTHING`, authorityID, activationTenant); err != nil {
		t.Fatalf("seed object renewal: %v", err)
	}
}

// A tenant placement change is in force on a cell only once the cell holds the
// tenant's objects recompiled against it: the cell evaluates each stream
// against the tenant revision the stream's own authority names. Every cell
// acknowledging the tenant authority is therefore not enough for "effective".
// The object acknowledgement that completes the set activates the change.
func TestPlacementActivationWaitsForDependentObjects_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	cells := []string{"cell-a", "cell-b"}
	seedPlacementActivationFixture(t, db, cells)
	// The stream as cells held it before the change, already acknowledged, and
	// its recompile against the new revision, still on its way.
	seedPlacementDependent(t, db, 1, activationRevision-1, cells)
	if _, err := db.Exec(`
		UPDATE commodore.media_authority_deliveries SET status='acknowledged', acknowledged_at=NOW()
		WHERE authority_kind='media_object'`); err != nil {
		t.Fatal(err)
	}
	srv := &CommodoreServer{db: db}
	ctx := context.Background()

	for _, cell := range cells {
		_, finish := ackInTx(t, srv, db, cell)
		if err := finish(); err != nil {
			t.Fatalf("tenant acknowledgement from %s: %v", cell, err)
		}
	}
	if status, active := placementChangeStatus(t, db); status != "pending" || active != 0 {
		t.Fatalf("every cell holds the tenant authority but the stream still names revision %d: rollout %q active %d, want pending and inactive",
			activationRevision-1, status, active)
	}

	seedPlacementDependent(t, db, 2, activationRevision, cells)
	authorityID := sharedauthority.LiveStreamAuthorityID(activationStream)
	acknowledge := func(cell string) {
		t.Helper()
		if err := srv.acknowledgeMediaAuthorityDelivery(ctx, commodoredb.ClaimMediaAuthorityDeliveriesRow{
			AuthorityKind: "media_object", AuthorityID: authorityID, AuthorityVersion: 2, CellID: cell,
		}); err != nil {
			t.Fatalf("object acknowledgement from %s: %v", cell, err)
		}
	}
	acknowledge("cell-a")
	if status, active := placementChangeStatus(t, db); status != "pending" || active != 0 {
		t.Fatalf("the recompiled stream authority is acknowledged by cell-a only: rollout %q active %d, want pending and inactive", status, active)
	}
	var reason string
	if err := db.QueryRow(`SELECT rollout_reason FROM commodore.media_placement_changes WHERE tenant_id=$1::uuid AND revision=$2`,
		activationTenant, activationRevision).Scan(&reason); err != nil || reason != placementRolloutReasonDependents {
		t.Fatalf("pending rollout reason %q (%v), want %q", reason, err, placementRolloutReasonDependents)
	}

	acknowledge("cell-b")
	if status, active := placementChangeStatus(t, db); status != "effective" || active != activationRevision {
		t.Fatalf("the last object acknowledgement did not activate the tenant change: rollout %q active %d", status, active)
	}
}
