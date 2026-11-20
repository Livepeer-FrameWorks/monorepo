//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/dvrpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	authority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeer "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
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
	_, signingKey, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	tenant := &media.TenantAuthority{SchemaVersion: 1, TenantId: claimTenantID, Lifecycle: media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: media.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW, EffectiveClusterGrants: []*media.TenantClusterGrant{{ClusterId: "media-eu", ControlCellId: "cell-eu", ClusterType: "edge"}, {ClusterId: "media-us", ControlCellId: "cell-us", ClusterType: "edge"}}}
	object := &media.MediaObjectAuthority{SchemaVersion: 1, TenantId: claimTenantID, UserId: claimUserID, InternalName: "claim-internal", ObjectKind: media.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, Lifecycle: media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, Object: &media.MediaObjectAuthority_LiveStream{LiveStream: &media.LiveStreamAuthority{StreamId: claimStreamID, IngestMode: "push", PublisherConfigurationVersion: 1}}}
	tenant.BillingModel = media.TenantBillingModel_TENANT_BILLING_MODEL_POSTPAID
	for _, grant := range tenant.EffectiveClusterGrants {
		grant.AccessSource = clusterpeer.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER
		grant.SubscriptionStatus, grant.ClusterClass = "active", "platform_official"
	}
	object.PlaybackId = "claim-playback"
	object.PlaybackPolicy = &media.PlaybackPolicy{Kind: media.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}
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
			kindID := media.AuthorityKind_AUTHORITY_KIND_TENANT
			if kind == "media_object" {
				kindID = media.AuthorityKind_AUTHORITY_KIND_MEDIA_OBJECT
			}
			now := time.Now().UTC()
			envelope, envelopeErr := authority.NewEnvelope(kindID, id, uint64(version), now, now.Add(time.Hour), now.Add(2*time.Hour), "publisher-test", cell, payload,
				[]*media.AuthoritySourceRevision{{Service: "commodore", Revision: "publisher-lease-fixture"}})
			if envelopeErr != nil {
				t.Fatal(envelopeErr)
			}
			signed, signErr := authority.Sign(envelope, signingKey)
			if signErr != nil {
				t.Fatal(signErr)
			}
			wire, marshalErr := proto.Marshal(signed)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.media_authority_deliveries(authority_kind,authority_id,authority_version,cell_id,signed_envelope) VALUES($1,$2,$3,$4,$5)`, kind, id, version, cell, wire); err != nil {
				t.Fatal(err)
			}
		}
		return digest[:]
	}
	tenantDigest := publish("tenant", claimTenantID, 2, tenant)
	objectDigest := publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), 2, object)
	req := &pb.PublisherLeaseRequest{TenantId: claimTenantID, StreamId: claimStreamID, InternalName: "claim-internal", ClusterId: "media-eu", ControlCellId: "cell-eu", ExecutionToken: "first", NodeId: "node-eu", TenantAuthorityVersion: 2, ObjectAuthorityVersion: 2, TenantPayloadSha256: tenantDigest, ObjectPayloadSha256: objectDigest}
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
	leaseEpoch := func(response *pb.PublisherLeaseResponse) int64 {
		t.Helper()
		field := response.ProtoReflect().Descriptor().Fields().ByName("lease_epoch")
		if field == nil || response.ProtoReflect().Get(field).Int() <= 0 {
			t.Fatal("publisher lease grant has no globally ordered ownership epoch")
		}
		return response.ProtoReflect().Get(field).Int()
	}
	firstEpoch := leaseEpoch(acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED))
	if got := leaseEpoch(acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION)); got != firstEpoch {
		t.Fatalf("same execution changed ownership epoch: got=%d want=%d", got, firstEpoch)
	}
	lookup := proto.CloneOf(req)
	lookup.TenantAuthorityVersion, lookup.ObjectAuthorityVersion = 0, 0
	lookup.TenantPayloadSha256, lookup.ObjectPayloadSha256 = nil, nil
	read, err := server.GetPublisherLease(ctx, lookup)
	if err != nil || read.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION || leaseEpoch(read) != firstEpoch {
		t.Fatalf("read-only exact lease verification failed: response=%v err=%v", read, err)
	}
	for _, change := range []string{"node", "cell"} {
		wrong := proto.CloneOf(lookup)
		if change == "node" {
			wrong.NodeId = "another-node"
		} else {
			wrong.ControlCellId = "another-cell"
		}
		read, err = server.GetPublisherLease(ctx, wrong)
		if err != nil || read.GetOutcome() == pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION || read.GetLeaseEpoch() != 0 {
			t.Fatalf("wrong %s verified another execution: response=%v err=%v", change, read, err)
		}
	}
	t.Run("ownership epoch fences renewal and release", func(t *testing.T) {
		wrong := &pb.ActiveIngestStream{TenantId: claimTenantID, InternalName: "claim-internal", ClusterId: req.GetClusterId(),
			ClaimToken: req.GetExecutionToken(), NodeId: req.GetNodeId(), ControlCellId: req.GetControlCellId(), RequireExistingOwner: true,
			PublisherLeaseEpoch: firstEpoch + 1}
		for _, release := range []bool{false, true} {
			batch := &pb.SyncActiveIngestPlacementRequest{}
			if release {
				batch.Release = []*pb.ActiveIngestStream{wrong}
			} else {
				batch.Renew = []*pb.ActiveIngestStream{wrong}
			}
			result, err := server.SyncActiveIngestPlacement(ctx, batch)
			if err != nil {
				t.Fatal(err)
			}
			if release && (result.GetReleased() != 0 || len(result.GetReleaseRefused()) != 1) {
				t.Fatalf("wrong epoch released current execution: %v", result)
			}
			if !release && (result.GetRenewed() != 0 || len(result.GetRenewRefused()) != 1) {
				t.Fatalf("wrong epoch renewed current execution: %v", result)
			}
		}
		// A refused release must not poison the still-current execution's
		// abandonment marker or prevent its next valid renewal.
		wrong.PublisherLeaseEpoch = firstEpoch
		result, err := server.SyncActiveIngestPlacement(ctx, &pb.SyncActiveIngestPlacementRequest{Renew: []*pb.ActiveIngestStream{wrong}})
		if err != nil || result.GetRenewed() != 1 || len(result.GetRenewRefused()) != 0 {
			t.Fatalf("refused stale release poisoned current lease: response=%v err=%v", result, err)
		}
	})
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
	stale.TenantAuthorityVersion, stale.ObjectAuthorityVersion = 1, 1
	staleResponse := acquire(stale, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	pairField := staleResponse.ProtoReflect().Descriptor().Fields().ByName("signed_authorities")
	if pairField == nil || staleResponse.ProtoReflect().Get(pairField).List().Len() != 2 {
		t.Fatal("stale publisher received no current signed authority pair; its next attempt still depends on asynchronous delivery")
	}
	refreshed := proto.CloneOf(stale)
	for i := range 2 {
		var signed media.SignedAuthorityEnvelope
		if err := proto.Unmarshal(staleResponse.ProtoReflect().Get(pairField).List().Get(i).Bytes(), &signed); err != nil {
			t.Fatal(err)
		}
		if signed.GetEnvelope().GetAudienceCellId() != req.GetControlCellId() || signed.GetEnvelope().GetAuthorityVersion() != 2 || len(signed.GetSignature()) != ed25519.SignatureSize {
			t.Fatalf("stale response returned an unrelated or unsigned authority: kind=%s version=%d", signed.GetEnvelope().GetKind(), signed.GetEnvelope().GetAuthorityVersion())
		}
		if signed.GetEnvelope().GetKind() == media.AuthorityKind_AUTHORITY_KIND_TENANT {
			refreshed.TenantAuthorityVersion = int64(signed.GetEnvelope().GetAuthorityVersion())
			refreshed.TenantPayloadSha256 = signed.GetEnvelope().GetPayloadSha256()
		} else {
			refreshed.ObjectAuthorityVersion = int64(signed.GetEnvelope().GetAuthorityVersion())
			refreshed.ObjectPayloadSha256 = signed.GetEnvelope().GetPayloadSha256()
		}
	}
	if got := leaseEpoch(acquire(refreshed, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION)); got != firstEpoch {
		t.Fatalf("authority exchange changed ownership epoch: got=%d want=%d", got, firstEpoch)
	}
	forged := proto.CloneOf(req)
	forged.ObjectPayloadSha256 = make([]byte, 32)
	acquire(forged, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	wrongCell := proto.CloneOf(req)
	wrongCell.ControlCellId = "cell-unknown"
	wrongResponse := acquire(wrongCell, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	if wrongResponse.ProtoReflect().Get(pairField).List().Len() != 0 {
		t.Fatal("unknown cell received another cell's credential-bearing envelopes")
	}
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
	cluster, token := readClaim(t, conn)
	current := proto.CloneOf(req)
	if cluster == other.GetClusterId() {
		current = proto.CloneOf(other)
	}
	current.ExecutionToken = token
	if next := leaseEpoch(acquire(current, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION)); next <= firstEpoch {
		t.Fatalf("successor owner did not advance global epoch: first=%d successor=%d", firstEpoch, next)
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
		if err != nil || !response.GetValid() || response.GetPublisherLeaseEpoch() <= 0 {
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

	t.Run("cold claim rechecks configuration after discovery", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,
			active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL,is_recording_enabled=true,
			dvr_chapter_mode='fixed_interval',dvr_chapter_interval_seconds=86400
			WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		request := &pb.ValidateStreamKeyRequest{StreamKey: "sk-published-lease", ClaimToken: "cold-stale-chapters", NodeId: "node-eu", ControlCellId: "cell-eu"}
		configuration := &pb.ValidateStreamKeyResponse{TenantId: claimTenantID, StreamId: claimStreamID,
			IsRecordingEnabled: true, DvrChapterMode: "window_sized_chapters"}
		_, err := server.acquireCompatiblePublisherClaim(ctx, request, "media-eu", configuration)
		if !dvrpolicy.IsRecordingPolicyPending(err) {
			t.Fatalf("cold discovery admitted settings superseded before its lease: %v", err)
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("cold claim changed ownership before configuration was current: %s/%s", cluster, token)
		}
	})

	t.Run("lease waits for a concurrent chapter save", func(t *testing.T) {
		deadlineCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(deadlineCtx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,
			active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL,is_recording_enabled=true,
			dvr_chapter_mode='fixed_interval',dvr_chapter_interval_seconds=7200
			WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		published := proto.CloneOf(object)
		published.GetLiveStream().RecordingEnabled = true
		published.GetLiveStream().DvrChapterMode, published.GetLiveStream().DvrChapterIntervalSeconds = "fixed_interval", 7200
		attempt := proto.CloneOf(req)
		attempt.ExecutionToken, attempt.ObjectAuthorityVersion = "chapter-edit-serialization", 4
		attempt.ObjectPayloadSha256 = publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), 4, published)
		edit, err := conn.BeginTx(deadlineCtx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer edit.Rollback()
		if _, err := edit.ExecContext(deadlineCtx, `UPDATE commodore.streams SET dvr_chapter_interval_seconds=86400
			WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		type leaseResult struct {
			response *pb.PublisherLeaseResponse
			err      error
		}
		done := make(chan leaseResult, 1)
		go func() {
			response, err := server.AcquirePublisherLease(deadlineCtx, attempt)
			done <- leaseResult{response, err}
		}()
		// Observe the request's execution lock, not a guessed scheduler delay.
		// The owner edit still holds the stream row when the lease reaches it.
		for {
			probe, err := conn.BeginTx(deadlineCtx, nil)
			if err != nil {
				t.Fatal(err)
			}
			var acquired bool
			err = probe.QueryRowContext(deadlineCtx, `SELECT pg_try_advisory_xact_lock(hashtext($1)::bigint)`,
				publisherLeaseExecutionKey(attempt.GetControlCellId(), attempt.GetNodeId(), attempt.GetExecutionToken())).Scan(&acquired)
			rollbackErr := probe.Rollback()
			if err != nil || rollbackErr != nil {
				t.Fatalf("observe execution lock: %v %v", err, rollbackErr)
			}
			if !acquired {
				break
			}
			select {
			case result := <-done:
				t.Fatalf("lease completed before the concurrent settings save: %+v", result)
			case <-deadlineCtx.Done():
				t.Fatal(deadlineCtx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := edit.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			if result.response != nil || !dvrpolicy.IsRecordingPolicyPending(result.err) {
				t.Fatalf("lease ignored a save committed while it waited: %+v", result)
			}
		case <-deadlineCtx.Done():
			t.Fatal(deadlineCtx.Err())
		}
		if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
			t.Fatalf("concurrent save left a stale publisher lease: %s/%s", cluster, token)
		}
	})

	t.Run("saved chapter policy fences admission until publication", func(t *testing.T) {
		for i, tc := range []struct {
			name                             string
			publishedEnabled, savedEnabled   bool
			publishedMode, savedMode         string
			publishedInterval, savedInterval int32
		}{
			{"enable", false, true, "none", "fixed_interval", 0, 86400},
			{"disable", true, false, "fixed_interval", "fixed_interval", 86400, 86400},
			{"mode", true, true, "window_sized_chapters", "fixed_interval", 0, 86400},
			{"interval", true, true, "fixed_interval", "fixed_interval", 7200, 86400},
			{"explicit none", true, true, "window_sized_chapters", "none", 0, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams
					SET active_ingest_cluster_id=NULL, active_ingest_claim_id=NULL,
					    active_ingest_node_id=NULL, active_ingest_control_cell_id=NULL,
					    is_recording_enabled=$3, dvr_chapter_mode=NULLIF($4, 'none'),
					    dvr_chapter_interval_seconds=NULLIF($5, 0)
					WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID,
					tc.savedEnabled, tc.savedMode, tc.savedInterval); err != nil {
					t.Fatal(err)
				}
				published := proto.CloneOf(object)
				live := published.GetLiveStream()
				live.RecordingEnabled, live.DvrChapterMode, live.DvrChapterIntervalSeconds = tc.publishedEnabled, tc.publishedMode, tc.publishedInterval
				attempt := proto.CloneOf(req)
				if i%2 != 0 {
					attempt.ClusterId, attempt.ControlCellId, attempt.NodeId = "media-us", "cell-us", "node-us"
				}
				attempt.ExecutionToken = "chapter-policy-" + tc.name
				attempt.ObjectAuthorityVersion = int64(10 + i*2)
				attempt.ObjectPayloadSha256 = publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), attempt.ObjectAuthorityVersion, published)
				result, err := server.AcquirePublisherLease(ctx, attempt)
				if !dvrpolicy.IsRecordingPolicyPending(err) {
					t.Fatalf("saved settings were not fenced before recording: result=%v err=%v", result, err)
				}
				if cluster, token := readClaim(t, conn); cluster != "" || token != "" {
					t.Fatalf("pending recording acquired publisher lease: %s/%s", cluster, token)
				}
				live.RecordingEnabled, live.DvrChapterMode, live.DvrChapterIntervalSeconds = tc.savedEnabled, tc.savedMode, tc.savedInterval
				digest := publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), attempt.ObjectAuthorityVersion+1, published)
				// A lagging cell receives the newer signed pair in the existing
				// lease exchange; it must not need a separate configuration RPC.
				exchange := acquire(attempt, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
				if len(exchange.GetSignedAuthorities()) != 2 {
					t.Fatalf("new publication was not offered to the lagging cell: %v", exchange)
				}
				attempt.ObjectAuthorityVersion++
				attempt.ObjectPayloadSha256 = digest
				acquire(attempt, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED)
				// A setting edit after acceptance affects the next publisher,
				// not a retry of this execution whose start intent is pinned.
				if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET is_recording_enabled=NOT is_recording_enabled
					WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
					t.Fatal(err)
				}
				acquire(attempt, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION)
				newPublisher := proto.CloneOf(attempt)
				newPublisher.ExecutionToken += "-next"
				if result, err := server.AcquirePublisherLease(ctx, newPublisher); result != nil || !dvrpolicy.IsRecordingPolicyPending(err) {
					t.Fatalf("new execution bypassed pending settings: response=%v err=%v", result, err)
				}
			})
		}
	})

	t.Run("chapter-only changes do not block non-recording ingest", func(t *testing.T) {
		if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET active_ingest_cluster_id=NULL,active_ingest_claim_id=NULL,
			active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL,is_recording_enabled=false,
			dvr_chapter_mode='fixed_interval',dvr_chapter_interval_seconds=7200
			WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
			t.Fatal(err)
		}
		attempt := proto.CloneOf(req)
		attempt.ExecutionToken, attempt.ObjectAuthorityVersion = "non-recording-policy-edit", 50
		attempt.ObjectPayloadSha256 = publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), 50, object)
		acquire(attempt, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED)
	})

	if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET is_recording_enabled=false
		WHERE tenant_id=$1::uuid AND id=$2::uuid`, claimTenantID, claimStreamID); err != nil {
		t.Fatal(err)
	}
	req.ObjectAuthorityVersion = 100
	req.ObjectPayloadSha256 = publish("media_object", authority.LiveStreamAuthorityID(claimStreamID), req.ObjectAuthorityVersion, object)
	tenant.BillingDecision = media.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	tenant.EffectiveClusterGrants = nil
	req.TenantPayloadSha256 = publish("tenant", claimTenantID, 3, tenant)
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE)
	req.TenantAuthorityVersion = 3
	acquire(req, pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED)
}
