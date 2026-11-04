//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sync"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	authority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	media "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestPublisherLease_RealPG(t *testing.T) { testPublisherLease(t, startCommodoreRealPG(t)) }
func TestPublisherLease_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "publisher_lease", "commodore")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPublisherLease(t, conn)
}

func testPublisherLease(t *testing.T, conn *sql.DB) {
	t.Helper()
	ctx := serviceCtx()
	seedClaimStream(t, conn, "sk-published-lease")
	tenant := &media.TenantAuthority{SchemaVersion: 1, TenantId: claimTenantID, Lifecycle: media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: media.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, EffectiveClusterGrants: []*media.TenantClusterGrant{{ClusterId: "media-eu", ControlCellId: "cell-eu", ClusterType: "edge"}, {ClusterId: "media-us", ControlCellId: "cell-us", ClusterType: "edge"}}}
	object := &media.MediaObjectAuthority{SchemaVersion: 1, TenantId: claimTenantID, UserId: claimUserID, InternalName: "claim-internal", ObjectKind: media.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, Lifecycle: media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, Object: &media.MediaObjectAuthority_LiveStream{LiveStream: &media.LiveStreamAuthority{StreamId: claimStreamID, IngestMode: "push", PublisherConfigurationVersion: 1}}}
	publish := func(kind, id string, version int64, payload proto.Message) []byte {
		t.Helper()
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.media_authority_versions(authority_kind,authority_id,authority_version,payload_schema_version,payload,payload_sha256,content_digest,tombstone,source_revisions,issued_at,refresh_after,valid_until) VALUES($1,$2,$3,1,$4,$5,$5,false,'[]',NOW(),NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours')`, kind, id, version, encoded, digest[:]); err != nil {
			t.Fatal(err)
		}
		if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.media_authority_current(authority_kind,authority_id,authority_version) VALUES($1,$2,$3) ON CONFLICT(authority_kind,authority_id) DO UPDATE SET authority_version=EXCLUDED.authority_version`, kind, id, version); err != nil {
			t.Fatal(err)
		}
		for _, cell := range []string{"cell-eu", "cell-us"} {
			if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.media_authority_targets(authority_kind,authority_id,cell_id,highest_targeted_version) VALUES($1,$2,$3,$4) ON CONFLICT(authority_kind,authority_id,cell_id) DO UPDATE SET highest_targeted_version=EXCLUDED.highest_targeted_version`, kind, id, cell, version); err != nil {
				t.Fatal(err)
			}
		}
		return digest[:]
	}
	tenantDigest := publish("tenant", claimTenantID, 1, tenant)
	objectDigest := publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), 1, object)
	req := &pb.PublisherLeaseRequest{TenantId: claimTenantID, StreamId: claimStreamID, InternalName: "claim-internal", ClusterId: "media-eu", ControlCellId: "cell-eu", ExecutionToken: "first", NodeId: "node-eu", TenantAuthorityVersion: 1, ObjectAuthorityVersion: 1, TenantPayloadSha256: tenantDigest, ObjectPayloadSha256: objectDigest}
	server := &CommodoreServer{db: conn, logger: logging.NewLogger()}
	if _, err := server.AcquirePublisherLease(context.Background(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("lease allowed non-service caller: %v", err)
	}
	acquire := func(request *pb.PublisherLeaseRequest, want pb.PublisherLeaseOutcome) *pb.PublisherLeaseResponse {
		t.Helper()
		got, err := server.AcquirePublisherLease(ctx, request)
		if err != nil || got.GetOutcome() != want {
			t.Fatalf("lease outcome=%v want=%v error=%v", got, want, err)
		}
		return got
	}
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED)
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION)
	other := proto.CloneOf(req)
	other.ExecutionToken = "second"
	if got := acquire(other, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER); got.GetHeldExecutionToken() != "first" {
		t.Fatal("same-cell stale-owner recovery lost its exact owner token")
	}
	other.ClusterId, other.ControlCellId, other.NodeId = "media-us", "cell-us", "node-us"
	if got := acquire(other, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER); got.GetHeldExecutionToken() != "" {
		t.Fatal("other cell learned publisher owner token")
	}
	stale := proto.CloneOf(req)
	stale.TenantAuthorityVersion = 2
	acquire(stale, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	forged := proto.CloneOf(req)
	forged.ObjectPayloadSha256 = make([]byte, 32)
	acquire(forged, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	wrongCell := proto.CloneOf(req)
	wrongCell.ControlCellId = "cell-unknown"
	acquire(wrongCell, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	if cluster, token := readClaim(t, conn); cluster != "media-eu" || token != "first" {
		t.Fatalf("failed request stole lease: %s/%s", cluster, token)
	}
	// Make the claim absent, then drive simultaneous cross-cell ownership writes.
	if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_cluster_updated_at=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	outcomes := make(chan pb.PublisherLeaseOutcome, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, request := range []*pb.PublisherLeaseRequest{req, other} {
		wg.Go(func() {
			<-start
			got, err := server.AcquirePublisherLease(ctx, request)
			outcomes <- got.GetOutcome()
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(outcomes)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	won, lost := 0, 0
	for outcome := range outcomes {
		if outcome == pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED {
			won++
		}
		if outcome == pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER {
			lost++
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("cross-cell duplicate ownership: won=%d lost=%d", won, lost)
	}
	t.Run("released owner cannot be restored by an in-flight renewal", func(t *testing.T) {
		cluster, token := readClaim(t, conn)
		stream := &pb.ActiveIngestStream{TenantId: claimTenantID, InternalName: "claim-internal", ClusterId: cluster, ClaimToken: token, RequireExistingOwner: true}
		if _, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Release: []*pb.ActiveIngestStream{stream}}); err != nil {
			t.Fatal(err)
		}
		renewed, err := server.RenewPublisherLeases(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{stream}})
		if err != nil || len(renewed.GetRenewRefused()) != 1 {
			t.Fatalf("released execution reacquired its lease through renewal: %+v %v", renewed, err)
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("late renewal restored ghost owner: %s/%s", cluster, token)
		}
	})

	t.Run("abandon before acquire fences a delayed request without a cell session", func(t *testing.T) {
		late := proto.CloneOf(req)
		late.ExecutionToken = "no-cell-session"
		if _, err := server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: late.GetControlCellId(), NodeId: late.GetNodeId(), ExecutionToken: late.GetExecutionToken()}); err != nil {
			t.Fatal(err)
		}
		acquire(late, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED)
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("abandoned execution created a ghost lease: %s/%s", cluster, token)
		}
	})

	t.Run("abandon releases only its exact execution and prevents late renew", func(t *testing.T) {
		owner := proto.CloneOf(req)
		owner.ExecutionToken = "lost-acquire-reply"
		got, err := server.AcquirePublisherLease(ctx, owner)
		if err != nil || got.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED {
			t.Fatalf("acquire: %v %v", got, err)
		}
		marker := &pb.AbandonPublisherLeaseRequest{ControlCellId: owner.GetControlCellId(), NodeId: owner.GetNodeId(), ExecutionToken: owner.GetExecutionToken()}
		if _, err := server.AbandonPublisherLease(context.Background(), marker); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unauthenticated abandonment: %v", err)
		}
		wrong := proto.CloneOf(marker)
		wrong.NodeId = "other-node"
		if _, err := server.AbandonPublisherLease(ctx, wrong); err != nil {
			t.Fatal(err)
		}
		if _, token := readClaim(t, conn); token != owner.GetExecutionToken() {
			t.Fatal("wrong node released another execution")
		}
		for range 2 {
			if _, err := server.AbandonPublisherLease(ctx, marker); err != nil {
				t.Fatal(err)
			}
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("abandon left ghost owner %s/%s", cluster, token)
		}
		// The dedicated renewal API must enforce strict ownership even if the
		// caller omitted the legacy compatibility flag.
		renew, err := server.RenewPublisherLeases(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{{TenantId: owner.GetTenantId(), InternalName: owner.GetInternalName(), ClusterId: owner.GetClusterId(), ClaimToken: owner.GetExecutionToken()}}})
		if err != nil || len(renew.GetRenewRefused()) != 1 {
			t.Fatalf("abandoned owner renewed: %v %v", renew, err)
		}
		got, err = server.AcquirePublisherLease(ctx, owner)
		if err != nil || got.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED {
			t.Fatalf("abandoned execution reacquired: %v %v", got, err)
		}
		successor := proto.CloneOf(owner)
		successor.ExecutionToken = "successor"
		got, err = server.AcquirePublisherLease(ctx, successor)
		if err != nil || got.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED {
			t.Fatalf("successor: %v %v", got, err)
		}
		if _, err := server.AbandonPublisherLease(ctx, marker); err != nil {
			t.Fatal(err)
		}
		if _, token := readClaim(t, conn); token != successor.GetExecutionToken() {
			t.Fatal("old abandonment released successor")
		}
		marker.ExecutionToken = successor.GetExecutionToken()
		if _, err := server.AbandonPublisherLease(ctx, marker); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cold validating claim is execution fenced before cell preparation", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		cold := &pb.ValidateStreamKeyRequest{StreamKey: "sk-published-lease", ClusterId: "media-eu", ClaimToken: "cold-no-session", NodeId: "node-eu", ControlCellId: "cell-eu"}
		response, err := claimServer(conn).ValidateStreamKey(ctx, cold)
		if err != nil || !response.GetValid() {
			t.Fatalf("cold acquire: %v %v", response, err)
		}
		if _, err = server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: cold.GetControlCellId(), NodeId: cold.GetNodeId(), ExecutionToken: cold.GetClaimToken()}); err != nil {
			t.Fatal(err)
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("cold claim survived definitive abandonment without a session: %s/%s", cluster, token)
		}
		response, err = claimServer(conn).ValidateStreamKey(ctx, cold)
		if err != nil || response.GetValid() {
			t.Fatalf("late cold claim crossed abandonment: %v %v", response, err)
		}
	})
	t.Run("outage renewal cannot restore a typed acquire with a lost acknowledgement", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		owner := proto.CloneOf(req)
		owner.ExecutionToken = "lost-ack-outage"
		if acquired, err := server.AcquirePublisherLease(ctx, owner); err != nil || acquired.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED {
			t.Fatalf("acquire: %v %v", acquired, err)
		}
		if _, err := server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: owner.GetControlCellId(), NodeId: owner.GetNodeId(), ExecutionToken: owner.GetExecutionToken()}); err != nil {
			t.Fatal(err)
		}
		entry := &pb.ActiveIngestStream{TenantId: owner.GetTenantId(), InternalName: owner.GetInternalName(), ClusterId: owner.GetClusterId(), ClaimToken: owner.GetExecutionToken(), NodeId: owner.GetNodeId(), ControlCellId: owner.GetControlCellId()}
		response, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}})
		if err != nil || len(response.GetRenewRefused()) != 1 {
			t.Fatalf("late outage renewal restored abandoned typed execution: %v %v", response, err)
		}
	})

	t.Run("outage renewal adopts an absent lease with an abandonment fence", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		entry := &pb.ActiveIngestStream{TenantId: claimTenantID, InternalName: "claim-internal", ClusterId: "media-eu", ClaimToken: "outage-no-lease", NodeId: "node-eu", ControlCellId: "cell-eu"}
		response, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}})
		if err != nil || response.GetRenewed() != 1 || !response.GetExecutionFencesApplied() {
			t.Fatalf("outage owner did not establish fenced placement: %v %v", response, err)
		}
		if _, err = server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: entry.GetControlCellId(), NodeId: entry.GetNodeId(), ExecutionToken: entry.GetClaimToken()}); err != nil {
			t.Fatal(err)
		}
		response, err = server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}})
		if err != nil || len(response.GetRenewRefused()) != 1 {
			t.Fatalf("abandoned outage owner reacquired placement: %v %v", response, err)
		}
	})
	t.Run("abandon and outage adoption serialize in either order", func(t *testing.T) {
		entry := &pb.ActiveIngestStream{TenantId: claimTenantID, InternalName: "claim-internal", ClusterId: "media-eu", ClaimToken: "outage-adoption-race", NodeId: "node-eu", ControlCellId: "cell-eu"}
		start := make(chan struct{})
		failures := make(chan error, 2)
		var concurrent sync.WaitGroup
		concurrent.Go(func() {
			<-start
			_, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}})
			failures <- err
		})
		concurrent.Go(func() {
			<-start
			_, err := server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: entry.GetControlCellId(), NodeId: entry.GetNodeId(), ExecutionToken: entry.GetClaimToken()})
			failures <- err
		})
		close(start)
		concurrent.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("concurrent outage adoption crossed abandonment: %s/%s", cluster, token)
		}
	})

	t.Run("terminal outage release fences a renewal queued before normal close", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		entry := &pb.ActiveIngestStream{TenantId: claimTenantID, InternalName: "claim-internal", ClusterId: "media-eu", ClaimToken: "outage-normal-close", NodeId: "node-eu", ControlCellId: "cell-eu"}
		if response, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}}); err != nil || response.GetRenewed() != 1 {
			t.Fatalf("outage adopt: %v %v", response, err)
		}
		if _, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Release: []*pb.ActiveIngestStream{entry}}); err != nil {
			t.Fatal(err)
		}
		response, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{entry}})
		if err != nil || len(response.GetRenewRefused()) != 1 {
			t.Fatalf("queued outage renewal restored normally closed execution: %v %v", response, err)
		}
	})

	t.Run("cold execution token cannot bypass abandonment by whitespace", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		if _, err := server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: "cell-eu", NodeId: "node-eu", ExecutionToken: "canonical-cold-token"}); err != nil {
			t.Fatal(err)
		}
		response, err := claimServer(conn).ValidateStreamKey(ctx, &pb.ValidateStreamKeyRequest{StreamKey: "sk-published-lease", ClusterId: "media-eu", ClaimToken: " canonical-cold-token ", NodeId: "node-eu", ControlCellId: "cell-eu"})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("noncanonical cold token crossed execution fence: response=%v error=%v", response, err)
		}
	})

	t.Run("fenced release refusal commits the requested execution terminal marker", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		owner := proto.CloneOf(req)
		owner.ExecutionToken = "refused-execution-owner"
		if response, err := server.AcquirePublisherLease(ctx, owner); err != nil || response.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED {
			t.Fatalf("owner acquire: %v %v", response, err)
		}
		ended := &pb.ActiveIngestStream{TenantId: owner.GetTenantId(), InternalName: owner.GetInternalName(), ClusterId: owner.GetClusterId(), ClaimToken: owner.GetExecutionToken(), NodeId: "different-ended-node", ControlCellId: owner.GetControlCellId()}
		response, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Release: []*pb.ActiveIngestStream{ended}})
		if err != nil || !response.GetExecutionFencesApplied() || len(response.GetReleaseRefused()) != 1 {
			t.Fatalf("fenced refusal not acknowledged: %v %v", response, err)
		}
		var terminal bool
		if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.publisher_lease_abandonments WHERE control_cell_id=$1 AND node_id=$2 AND execution_token=$3)`, ended.GetControlCellId(), ended.GetNodeId(), ended.GetClaimToken()).Scan(&terminal); err != nil || !terminal {
			t.Fatalf("refused execution terminal marker missing: %v %v", terminal, err)
		}
		if _, token := readClaim(t, conn); token != owner.GetExecutionToken() {
			t.Fatal("refusal cleared a different node's owner")
		}
		if _, err = server.AbandonPublisherLease(ctx, &pb.AbandonPublisherLeaseRequest{ControlCellId: owner.GetControlCellId(), NodeId: owner.GetNodeId(), ExecutionToken: owner.GetExecutionToken()}); err != nil {
			t.Fatal(err)
		}
	})

	tenant.BillingDecision = media.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	req.TenantPayloadSha256 = publish("tenant", claimTenantID, 2, tenant)
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	req.TenantAuthorityVersion = 2
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED)
}
