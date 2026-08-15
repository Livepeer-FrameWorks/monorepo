//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
)

// Claim ownership is enforced by SQL, so it is proven against a real Postgres on
// the real commodore.sql baseline. A mock can be told to return zero rows; only
// the database can show that the statement WOULD.
const (
	claimTenantID = "11111111-1111-1111-1111-111111111111"
	claimUserID   = "22222222-2222-2222-2222-222222222222"
	claimStreamID = "33333333-3333-3333-3333-333333333333"
)

func seedClaimStream(t *testing.T, conn *sql.DB, streamKey string) {
	t.Helper()
	ctx := context.Background()
	// Tenants live in Quartermaster's schema; commodore.users carries the
	// tenant id it was provisioned under, and ValidateStreamKey joins it for the
	// is_active gate.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, password_hash, is_active)
		VALUES ($1::uuid, $2::uuid, 'claim@example.com', 'x', TRUE)
		ON CONFLICT (id) DO NOTHING
	`, claimUserID, claimTenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, internal_name, stream_key, playback_id, ingest_mode, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'claim-internal', $4, 'pb_claim', 'push', 'claim stream')
		ON CONFLICT (id) DO NOTHING
	`, claimStreamID, claimTenantID, claimUserID, streamKey); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
}

func claimServer(conn *sql.DB) *CommodoreServer {
	peers := []*clusterpeerpb.TenantClusterPeer{{ClusterId: "media-eu", HealthStatus: "healthy"}}
	return &CommodoreServer{
		db:     conn,
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{claimTenantID: {
			clusterID:           "media-eu",
			admissionPeers:      peers,
			clusterPeers:        peers,
			resolvedAt:          time.Now(),
			admissionResolvedAt: time.Now(),
		}},
		routeCacheTTL: 5 * time.Minute,
	}
}

func readClaim(t *testing.T, conn *sql.DB) (cluster, token string) {
	t.Helper()
	var c, tok sql.NullString
	if err := conn.QueryRow(`
		SELECT active_ingest_cluster_id, active_ingest_claim_id
		FROM commodore.streams WHERE id = $1::uuid
	`, claimStreamID).Scan(&c, &tok); err != nil {
		t.Fatalf("read claim: %v", err)
	}
	return c.String, tok.String
}

// Same cluster is not the same owner. A second publisher connection reaching the
// cluster that already holds a live claim must not take it over: if it did, it
// would be told the claim is its own, and its rejection by a later admission
// gate would hand back the placement of the publisher that is actually
// streaming.
func TestValidateStreamKey_SameClusterCannotStealLiveClaim_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-steal")
	server := claimServer(conn)
	ctx := context.Background()

	first, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-steal", ClusterId: "media-eu", ClaimToken: "connection-A",
	})
	if err != nil {
		t.Fatalf("first validate: %v", err)
	}
	if !first.GetValid() || !first.GetClaimAcquired() {
		t.Fatalf("publisher A did not reserve the claim: valid=%v acquired=%v", first.GetValid(), first.GetClaimAcquired())
	}

	second, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-steal", ClusterId: "media-eu", ClaimToken: "connection-B",
	})
	if err != nil {
		t.Fatalf("second validate: %v", err)
	}
	if second.GetClaimAcquired() {
		t.Fatal("publisher B was told it holds a claim owned by A")
	}
	// Not merely "no claim": B must be REFUSED. Asserting only claim_acquired
	// would pass against the older behaviour, which returned valid=true and let
	// B publish alongside A.
	if second.GetValid() {
		t.Fatal("publisher B was admitted while A holds the claim")
	}
	if second.GetRejectionReason() != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST {
		t.Fatalf("rejection reason = %v, want DUPLICATE_INGEST", second.GetRejectionReason())
	}

	cluster, token := readClaim(t, conn)
	if cluster != "media-eu" || token != "connection-A" {
		t.Fatalf("claim was stolen: cluster=%q owner=%q, want media-eu/connection-A", cluster, token)
	}
}

// The owner may refresh its own claim — that is an ordinary PUSH_REWRITE re-fire
// from a live connection — but a refresh is NOT a reservation, so it must not be
// reported as one. Reporting it would let a rejected retry release the placement
// backing the live session.
func TestValidateStreamKey_OwnerRefreshIsNotAReservation_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-refresh")
	server := claimServer(conn)
	ctx := context.Background()

	if _, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-refresh", ClusterId: "media-eu", ClaimToken: "connection-A",
	}); err != nil {
		t.Fatalf("first validate: %v", err)
	}

	again, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-refresh", ClusterId: "media-eu", ClaimToken: "connection-A",
	})
	if err != nil {
		t.Fatalf("refresh validate: %v", err)
	}
	if !again.GetValid() {
		t.Fatal("the claim owner was refused its own re-fire")
	}
	if again.GetClaimAcquired() {
		t.Fatal("a refresh by the existing owner was reported as a new reservation")
	}

	if _, token := readClaim(t, conn); token != "connection-A" {
		t.Fatalf("owner changed on refresh: %q", token)
	}
}

// Once the claim lapses, the next publisher takes it: the fence is ownership of
// a LIVE claim, not a permanent pin.
func TestValidateStreamKey_LapsedClaimIsReservable_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-lapsed")
	server := claimServer(conn)
	ctx := context.Background()

	if _, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-lapsed", ClusterId: "media-eu", ClaimToken: "connection-A",
	}); err != nil {
		t.Fatalf("first validate: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE commodore.streams
		SET active_ingest_cluster_updated_at = NOW() - INTERVAL '10 minutes'
		WHERE id = $1::uuid
	`, claimStreamID); err != nil {
		t.Fatalf("age the claim: %v", err)
	}

	second, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-lapsed", ClusterId: "media-eu", ClaimToken: "connection-B",
	})
	if err != nil {
		t.Fatalf("second validate: %v", err)
	}
	if !second.GetClaimAcquired() {
		t.Fatal("a lapsed claim was not reservable by the next publisher")
	}
	if _, token := readClaim(t, conn); token != "connection-B" {
		t.Fatalf("owner = %q, want connection-B", token)
	}
}

// Release is owner-fenced in SQL: a connection that does not own the claim
// clears nothing, however right its cluster is.
func TestSyncActiveIngestPlacement_ReleaseRequiresOwnership_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-release")
	server := claimServer(conn)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	if _, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-release", ClusterId: "media-eu", ClaimToken: "connection-A",
	}); err != nil {
		t.Fatalf("validate: %v", err)
	}

	notOwner := &commodorepb.ActiveIngestStream{
		TenantId: claimTenantID, InternalName: "claim-internal", ClaimToken: "connection-B",
	}
	resp, err := server.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Release:   []*commodorepb.ActiveIngestStream{notOwner},
	})
	if err != nil {
		t.Fatalf("release by non-owner: %v", err)
	}
	if resp.GetReleased() != 0 {
		t.Fatalf("released = %d; a non-owner cleared the claim", resp.GetReleased())
	}
	if cluster, _ := readClaim(t, conn); cluster != "media-eu" {
		t.Fatalf("claim cleared by a non-owner: cluster=%q", cluster)
	}

	owner := &commodorepb.ActiveIngestStream{
		TenantId: claimTenantID, InternalName: "claim-internal", ClaimToken: "connection-A",
	}
	ownerResp, err := server.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Release:   []*commodorepb.ActiveIngestStream{owner},
	})
	if err != nil {
		t.Fatalf("release by owner: %v", err)
	}
	if ownerResp.GetReleased() != 1 {
		t.Fatalf("released = %d; the owner could not give its own claim back", ownerResp.GetReleased())
	}
	if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
		t.Fatalf("claim not cleared: cluster=%q owner=%q", cluster, token)
	}
}

// Renewal is owner-fenced too. "Same cluster" is not proof of owning what is in
// it: a renewal naming another connection's token must not refresh the claim,
// and must not replace its owner — doing so would hand the renewer licence to
// release a publisher it never admitted.
func TestSyncActiveIngestPlacement_RenewalRequiresOwnership_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-renew")
	server := claimServer(conn)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	if _, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-renew", ClusterId: "media-eu", ClaimToken: "connection-A",
	}); err != nil {
		t.Fatalf("validate: %v", err)
	}

	intruder := &commodorepb.ActiveIngestStream{
		TenantId: claimTenantID, InternalName: "claim-internal", ClaimToken: "connection-B",
	}
	resp, err := server.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     []*commodorepb.ActiveIngestStream{intruder},
	})
	if err != nil {
		t.Fatalf("renew by non-owner: %v", err)
	}
	if resp.GetRenewed() != 0 {
		t.Fatalf("renewed = %d; a non-owner refreshed a live claim", resp.GetRenewed())
	}
	if _, token := readClaim(t, conn); token != "connection-A" {
		t.Fatalf("claim owner replaced by a renewal: %q", token)
	}

	owner := &commodorepb.ActiveIngestStream{
		TenantId: claimTenantID, InternalName: "claim-internal", ClaimToken: "connection-A",
	}
	ownerResp, err := server.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     []*commodorepb.ActiveIngestStream{owner},
	})
	if err != nil {
		t.Fatalf("renew by owner: %v", err)
	}
	if ownerResp.GetRenewed() != 1 {
		t.Fatalf("renewed = %d; the owner could not refresh its own claim", ownerResp.GetRenewed())
	}
}

// Renewal may still ESTABLISH placement where none is held — that is what
// recovers a publisher admitted from the client's validation cache during a
// Commodore outage, which took no claim.
func TestSyncActiveIngestPlacement_RenewalEstablishesUnheldClaim_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-establish")
	server := claimServer(conn)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	stream := &commodorepb.ActiveIngestStream{
		TenantId: claimTenantID, InternalName: "claim-internal", ClaimToken: "connection-A",
	}
	resp, err := server.SyncActiveIngestPlacement(ctx, &commodorepb.SyncActiveIngestPlacementRequest{
		ClusterId: "media-eu",
		Renew:     []*commodorepb.ActiveIngestStream{stream},
	})
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if resp.GetRenewed() != 1 {
		t.Fatalf("renewed = %d; an unheld claim was not established", resp.GetRenewed())
	}
	if cluster, token := readClaim(t, conn); cluster != "media-eu" || token != "connection-A" {
		t.Fatalf("claim = %q/%q, want media-eu/connection-A", cluster, token)
	}
}

// A managed retract arriving late must not clear a push publisher's claim on the
// same cluster — the writers share the ownership invariant.
func TestClearStreamActiveCluster_CannotClearPushClaim_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	seedClaimStream(t, conn, "sk-managed")
	server := claimServer(conn)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")

	if _, err := server.ValidateStreamKey(ctx, &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "sk-managed", ClusterId: "media-eu", ClaimToken: "connection-A",
	}); err != nil {
		t.Fatalf("validate: %v", err)
	}

	resp, err := server.ClearStreamActiveCluster(ctx, &commodorepb.ClearStreamActiveClusterRequest{
		StreamId:          claimStreamID,
		ExpectedClusterId: "media-eu",
		TenantId:          claimTenantID,
	})
	if err != nil {
		t.Fatalf("managed clear: %v", err)
	}
	if resp.GetCleared() {
		t.Fatal("a managed retract cleared a push publisher's claim")
	}
	if cluster, token := readClaim(t, conn); cluster != "media-eu" || token != "connection-A" {
		t.Fatalf("push claim disturbed: cluster=%q owner=%q", cluster, token)
	}
}
