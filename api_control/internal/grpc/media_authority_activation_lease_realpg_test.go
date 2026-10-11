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
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

// A replica that claimed a legacy tenant's activation and then stopped (a
// restart mid-compile, or a claim that committed after its caller gave up)
// leaves a live lease behind. The attestation that completes the tenant's cells
// folds into that leased row; the tenant's placement authority must still be
// published within seconds of that attestation, not when a compile-length lease
// runs out.
func TestReportCapability_ActivationNotHeldByAbandonedClaim_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tenant, _ := commercialAuthorityFixture()
	tenant.MediaPlacement.Revision = 1
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peerA := &clusterpb.TenantClusterPeer{ClusterId: "official", ClusterType: "edge", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, ControlCellId: "cell-a", RegionId: "us-east", MediaConsent: &pb.CapacityConsent{Revision: 7, AllowIngest: true, AllowServe: true, AllowExternalSource: true}}
	peerB := proto.CloneOf(peerA)
	peerB.ClusterId, peerB.ControlCellId = "partner", "cell-b"
	owner := &tenantPlacementOwnerFixture{entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official", "partner"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{peerA, peerB}}}
	server := &CommodoreServer{db: db, logger: logrus.New(), authorityTenantSource: owner, authorityBillingSource: owner, mediaAuthorityKeyID: "activation-lease", mediaAuthorityPrivateKey: private}
	command := placementpolicy.ApplyInput{Scope: placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "tenant", ID: tenant.TenantId}, ActorID: "tenant-policy-test", IdempotencyKey: "activation-lease", ReviewDigest: strings.Repeat("a", 64), Policy: tenant.MediaPlacement}
	if _, err := placementpolicy.NewStore(db).Apply(ctx, command, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	q := commodoredb.New(db)
	currentSchema := func() int32 {
		t.Helper()
		var schema int32
		err := db.QueryRowContext(ctx, `SELECT version.payload_schema_version FROM commodore.media_authority_current AS current
			JOIN commodore.media_authority_versions AS version USING (authority_kind, authority_id, authority_version)
			WHERE current.authority_kind = 'tenant' AND current.authority_id = $1`, tenant.TenantId).Scan(&schema)
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}

	// One cell attested: the tenant is published on the legacy schema.
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	if schema := currentSchema(); schema != 1 {
		t.Fatalf("tenant with an unattested cell published schema %d, want 1", schema)
	}

	// The replica that claims the activation stops without settling it.
	if err := q.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(tenant.TenantId), tenant.TenantId, "test", "commodore", "abandoned:"+tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	abandoned, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{
		LeaseMs: mediaAuthorityObligationLease.Milliseconds(), Lane: commodoredb.MediaAuthorityLaneEvent, BatchSize: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimedTenant := false
	for _, row := range abandoned {
		claimedTenant = claimedTenant || row.TargetKey == commodoredb.TenantMediaAuthorityTarget(tenant.TenantId).Key
	}
	if !claimedTenant {
		t.Fatalf("abandoned claim did not take the tenant's activation: %+v", abandoned)
	}

	// The last cell attests; its activation folds into the abandoned row.
	if err := server.recordCellPlacementCapability(ctx, "cell-b", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	attested := time.Now()
	const within = 15 * time.Second
	for currentSchema() < 2 {
		if waited := time.Since(attested); waited > within {
			t.Fatalf("placement authority not published %s after the last attestation (schema %d)", waited.Round(time.Second), currentSchema())
		}
		server.processMediaAuthorityEventObligations(ctx)
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("placement authority published %s after the last attestation", time.Since(attested).Round(100*time.Millisecond))
}

// A compile in flight renews its lease, so no other worker claims its target
// while it runs however long it takes; once it stops renewing, the target is
// claimable again within one lease.
func TestObligationLeaseRenewal_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	const tenantID = "43000000-0000-4000-8000-000000000001"
	if err := q.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID, "test", "commodore", "renewal:"+tenantID); err != nil {
		t.Fatal(err)
	}
	const lease = 1500 * time.Millisecond
	claim := func() []commodoredb.ClaimMediaAuthorityObligationsRow {
		t.Helper()
		rows, err := q.ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{LeaseMs: lease.Milliseconds(), Lane: commodoredb.MediaAuthorityLaneEvent, BatchSize: 8})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := claim()
	if len(rows) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(rows))
	}
	server := &CommodoreServer{db: db, logger: logrus.New()}
	stop := server.renewMediaAuthorityObligationLease(ctx, rows[0], lease)
	// A fold while the compile runs keeps the lease, and renewal keeps it alive.
	if err := q.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID, "test", "commodore", "renewal-fold:"+tenantID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * lease)
	if stolen := claim(); len(stolen) != 0 {
		stop()
		t.Fatalf("a renewed lease was claimed by another worker: %+v", stolen)
	}
	stop()
	time.Sleep(lease + 500*time.Millisecond)
	if reclaimed := claim(); len(reclaimed) != 1 {
		t.Fatalf("target not claimable one lease after renewal stopped: %d rows", len(reclaimed))
	}
}
