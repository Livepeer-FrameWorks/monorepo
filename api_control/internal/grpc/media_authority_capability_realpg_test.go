//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

func readyCellAttestation() *foghornpb.MediaCellPlacementCapability {
	return &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true, LiveReplicas: 2}
}

func TestMediaPlacementCellAttestation_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	now := time.Now().UTC()
	const legacyTenant, placedTenant = "41000000-0000-4000-8000-000000000001", "41000000-0000-4000-8000-000000000002"
	for _, tenant := range []struct {
		id     string
		schema int32
	}{{legacyTenant, 1}, {placedTenant, 2}} {
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: tenant.id, AuthorityVersion: 1, PayloadSchemaVersion: tenant.schema, Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"), IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: tenant.id, AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
		if err := q.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{AuthorityKind: "tenant", AuthorityID: tenant.id, CellID: "cell-a", AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	server := &CommodoreServer{db: db, logger: logrus.New()}
	inbox := func() []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT tenant_id::text FROM commodore.media_authority_refresh_obligations WHERE last_reason = $1 ORDER BY tenant_id", cellPlacementCapabilityRefreshReason)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var tenants []string
		for rows.Next() {
			var tenant string
			if err := rows.Scan(&tenant); err != nil {
				t.Fatal(err)
			}
			tenants = append(tenants, tenant)
		}
		return tenants
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", nil); err != nil {
		t.Fatal(err)
	}
	if rows := inbox(); len(rows) != 0 {
		t.Fatalf("absent attestation queued activation: %v", rows)
	}
	if ready, err := placementCellsReady(ctx, q, []string{"cell-a"}); err != nil || ready {
		t.Fatalf("absent attestation counted as ready: %t %v", ready, err)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if rows := inbox(); len(rows) != 1 || rows[0] != legacyTenant {
		t.Fatalf("ready attestation queued the wrong tenants: %v", rows)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_refresh_obligations SET status='parked', park_reason='test' WHERE tenant_id=$1", legacyTenant); err != nil {
		t.Fatal(err)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if rows := inbox(); len(rows) != 1 {
		t.Fatalf("repeated attestation duplicated activation work: %v", rows)
	}
	assertObligation := func(wantRevision int64, wantStatus string) {
		t.Helper()
		var revision int64
		var state string
		if err := db.QueryRowContext(ctx, "SELECT revision, status FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane='event'", legacyTenant).Scan(&revision, &state); err != nil {
			t.Fatal(err)
		}
		if revision != wantRevision || state != wantStatus {
			t.Fatalf("activation obligation revision=%d status=%s, want %d/%s", revision, state, wantRevision, wantStatus)
		}
	}
	assertObligation(1, "parked")
	nodeReady := readyCellAttestation()
	nodeReady.NodePlacementReady = true
	if err := server.recordCellPlacementCapability(ctx, "cell-a", nodeReady); err != nil {
		t.Fatal(err)
	}
	assertObligation(2, "pending")
	if err := server.recordCellPlacementCapability(ctx, "cell-a", nodeReady); err != nil {
		t.Fatal(err)
	}
	assertObligation(2, "pending")
	if ready, err := placementCellsReady(ctx, q, []string{"cell-a"}); err != nil || !ready {
		t.Fatalf("ready attestation not readable: %t %v", ready, err)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 1}); err != nil {
		t.Fatal(err)
	}
	if ready, err := placementCellsReady(ctx, q, []string{"cell-a"}); err != nil || ready {
		t.Fatalf("withdrawn attestation stayed ready: %t %v", ready, err)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", nodeReady); err != nil {
		t.Fatal(err)
	}
	assertObligation(3, "pending")
}

func TestMediaPlacementFirstIssuance_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	tenant, _ := commercialAuthorityFixture()
	tenant.MediaPlacement.Revision = 1
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerA := &clusterpb.TenantClusterPeer{ClusterId: "official", ClusterType: "edge", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, ControlCellId: "cell-a", RegionId: "us-east", MediaConsent: &pb.CapacityConsent{Revision: 7, AllowIngest: true, AllowServe: true, AllowExternalSource: true}}
	owner := &tenantPlacementOwnerFixture{entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{peerA}}}
	server := &CommodoreServer{db: db, logger: logrus.New(), authorityTenantSource: owner, authorityBillingSource: owner, mediaAuthorityKeyID: "first-issuance", mediaAuthorityPrivateKey: private}
	store := placementpolicy.NewStore(db)
	command := placementpolicy.ApplyInput{Scope: placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "tenant", ID: tenant.TenantId}, ActorID: "tenant-policy-test", IdempotencyKey: "first", ReviewDigest: strings.Repeat("a", 64), Policy: tenant.MediaPlacement}
	if _, err := store.Apply(ctx, command, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	q := commodoredb.New(db)
	current := func() (int64, *mediapb.TenantAuthority) {
		t.Helper()
		row, err := q.LockCurrentTenantMediaAuthority(ctx, tenant.TenantId)
		if err != nil {
			t.Fatal(err)
		}
		payload := &mediapb.TenantAuthority{}
		if err := proto.Unmarshal(row.Payload, payload); err != nil {
			t.Fatal(err)
		}
		var version int64
		if err := db.QueryRowContext(ctx, "SELECT authority_version FROM commodore.media_authority_current WHERE authority_kind='tenant' AND authority_id=$1", tenant.TenantId).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version, payload
	}
	// No history and no attestation: the first compile stays on the legacy schema.
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	if version, payload := current(); version != 1 || payload.SchemaVersion != 1 || payload.MediaPlacement != nil {
		t.Fatalf("unattested cell received placement authority: version=%d schema=%d", version, payload.SchemaVersion)
	}
	// Attestation by the only target cell unlocks first issuance from saved intent.
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	version, payload := current()
	if version != 2 || payload.SchemaVersion != 2 || payload.MediaPlacement.GetRevision() != 1 || len(payload.EffectiveClusterGrants) != 1 ||
		payload.EffectiveClusterGrants[0].RegionId != "us-east" || !proto.Equal(payload.EffectiveClusterGrants[0].MediaConsent, peerA.MediaConsent) {
		t.Fatalf("first issuance lost policy, region or consent: version=%d %v", version, payload)
	}
	var pendingActivation int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM commodore.media_authority_refresh_obligations WHERE last_reason = $1 AND tenant_id = $2", cellPlacementCapabilityRefreshReason, tenant.TenantId).Scan(&pendingActivation); err != nil || pendingActivation != 1 {
		t.Fatalf("attestation did not queue the legacy tenant exactly once: %d %v", pendingActivation, err)
	}
	// A later grant on an unattested cell is refused at publication, never rolled back.
	peerB := proto.CloneOf(peerA)
	peerB.ClusterId, peerB.ControlCellId = "partner", "cell-b"
	owner.entitlement.AllowedClusterIds = []string{"official", "partner"}
	owner.entitlement.EffectiveAccess = []*clusterpb.TenantClusterPeer{peerA, peerB}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err == nil || !strings.Contains(err.Error(), "placement enforcement capability") {
		t.Fatalf("unattested new target cell was published: %v", err)
	}
	if version, payload := current(); version != 2 || payload.SchemaVersion != 2 {
		t.Fatalf("refused publication changed the current authority: version=%d schema=%d", version, payload.SchemaVersion)
	}
	if err := server.recordCellPlacementCapability(ctx, "cell-b", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	if version, payload := current(); version != 3 || payload.SchemaVersion != 2 || len(payload.EffectiveClusterGrants) != 2 {
		t.Fatalf("attested new target cell was not published: version=%d schema=%d grants=%d", version, payload.SchemaVersion, len(payload.EffectiveClusterGrants))
	}
}
