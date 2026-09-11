//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMediaPlacementTenantDeadline_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	const streamID = "31000000-0000-4000-8000-000000000021"
	tenant, _ := commercialAuthorityFixture()
	tenant.MediaPlacement = &pb.PolicySet{}
	if err := seedCommercialTenantVersion(ctx, db, tenant, 1); err != nil {
		t.Fatal(err)
	}
	q := commodoredb.New(db)
	if _, err := q.AllocateMediaAuthorityVersion(ctx, commodoredb.AllocateMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,'21000000-0000-4000-8000-000000000021','tenant-deadline-key','tenant-deadline-playback','tenant-deadline-internal','Deadline')", streamID, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	peer := &clusterpb.TenantClusterPeer{ClusterId: "official", ClusterClass: "platform_official", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER, ControlCellId: "cell-a", MediaConsent: &pb.CapacityConsent{AllowIngest: true, AllowServe: true}, AccessExpiresAt: timestamppb.New(time.Now().Add(30 * time.Second))}
	owner := &tenantPlacementOwnerFixture{entitlement: &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official"}, EffectiveAccess: []*clusterpb.TenantClusterPeer{peer}}}
	server := &CommodoreServer{db: db, logger: logging.NewLogger(), authorityTenantSource: owner, authorityBillingSource: owner, mediaAuthorityKeyID: "tenant-deadline", mediaAuthorityPrivateKey: private}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	if err := server.compileTenantAuthority(ctx, tenant.TenantId); err != nil {
		t.Fatal(err)
	}
	claim := func() ([]commodoredb.ClaimTenantMediaAuthorityDeadlineRefreshRow, error) {
		return q.ClaimTenantMediaAuthorityDeadlineRefresh(ctx, commodoredb.ClaimTenantMediaAuthorityDeadlineRefreshParams{BatchSize: 8, LeaseMs: tenantMediaAuthorityDeadlineLease.Milliseconds()})
	}
	if rows, err := claim(); err != nil || len(rows) != 0 {
		t.Fatalf("tenant renewal claimed before its deadline: %v %v", rows, err)
	}
	checkVersion := func(want int64) {
		t.Helper()
		got, err := q.GetScheduledMediaAuthorityVersion(ctx, commodoredb.GetScheduledMediaAuthorityVersionParams{TenantID: tenant.TenantId, AuthorityKind: "tenant", AuthorityID: tenant.TenantId})
		if err != nil || got != want {
			t.Fatalf("tenant version=%d want=%d err=%v", got, want, err)
		}
	}
	makeDue := func(version int64) {
		t.Helper()
		if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_refresh_inbox SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND source_service='commodore' AND source_event_id=$2", tenant.TenantId, mediaAuthorityDeadlineEvent("tenant", tenant.TenantId, version)); err != nil {
			t.Fatal(err)
		}
	}
	checkSchedule := func(version int64) {
		t.Helper()
		var issued, refresh, expiry, due time.Time
		if err := db.QueryRowContext(ctx, "SELECT issued_at,refresh_after,valid_until FROM commodore.media_authority_versions WHERE authority_kind='tenant' AND authority_id=$1 AND authority_version=$2", tenant.TenantId, version).Scan(&issued, &refresh, &expiry); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT next_attempt_at FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND source_service='commodore' AND source_event_id=$2", tenant.TenantId, mediaAuthorityDeadlineEvent("tenant", tenant.TenantId, version)).Scan(&due); err != nil {
			t.Fatal(err)
		}
		if !issued.Before(refresh) || !refresh.Before(expiry) || !due.Equal(refresh) || expiry.Sub(issued) > time.Minute || refresh.Sub(issued) > 30*time.Second {
			t.Fatal("short tenant grant did not schedule bounded renewal headroom")
		}
	}
	checkSchedule(2)
	makeDue(2)
	objectEvent := mediaAuthorityDeadlineEvent("media_object", sharedauthority.LiveStreamAuthorityID(streamID), 1)
	if _, err := q.ScheduleMediaAuthorityRefresh(ctx, commodoredb.ScheduleMediaAuthorityRefreshParams{TenantID: tenant.TenantId, SourceEventID: objectEvent, Reason: "media_object:live_stream:" + streamID + ":deadline_refresh", NextAttemptAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	ordinary, err := q.ClaimMediaAuthorityRefreshInbox(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxParams{BatchSize: 8, LeaseMs: 60000})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range ordinary {
		if strings.HasPrefix(row.SourceEventID, "authority-deadline:") {
			t.Fatal("ordinary worker stole a deadline job")
		}
	}
	objects, err := q.ClaimMediaAuthorityDeadlineRefresh(ctx, commodoredb.ClaimMediaAuthorityDeadlineRefreshParams{BatchSize: 8, LeaseMs: 60000})
	if err != nil || len(objects) != 1 || objects[0].SourceEventID != objectEvent {
		t.Fatalf("object worker crossed tenant claim boundary: %v %v", objects, err)
	}
	var wait sync.WaitGroup
	results := make(chan []commodoredb.ClaimTenantMediaAuthorityDeadlineRefreshRow, 2)
	failures := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); rows, err := claim(); results <- rows; failures <- err }()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var claimed []commodoredb.ClaimTenantMediaAuthorityDeadlineRefreshRow
	for rows := range results {
		claimed = append(claimed, rows...)
	}
	if len(claimed) != 1 {
		t.Fatalf("concurrent tenant claims=%d, want 1", len(claimed))
	}
	peer.AccessExpiresAt = timestamppb.New(time.Now().Add(60 * time.Second))
	server.processMediaAuthorityDeadlineRefreshRow(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(claimed[0]))
	checkVersion(3)
	checkSchedule(3)
	fanoutKey := "tenant-deadline-fanout:" + tenant.TenantId + ":3"
	rows, err := q.ClaimMediaAuthorityRefreshInbox(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxParams{BatchSize: 8, LeaseMs: 60000})
	if err != nil || len(rows) != 1 || rows[0].SourceEventID != fanoutKey || rows[0].Reason != "tenant_media_objects:deadline_refresh" {
		t.Fatalf("renewal lost its ordinary dependent obligation: %v %v", rows, err)
	}
	beforeFanout, err := q.LockMediaAuthorityCompile(ctx, "tenant:"+tenant.TenantId)
	if err != nil {
		t.Fatal(err)
	}
	server.processMediaAuthorityRefreshRow(ctx, rows[0])
	afterFanout, err := q.LockMediaAuthorityCompile(ctx, "tenant:"+tenant.TenantId)
	if err != nil || beforeFanout != afterFanout {
		t.Fatalf("dependent enumeration invalidated the tenant compilation generation: %v", err)
	}
	checkVersion(3)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND source_event_id LIKE 'tenant-fanout:%' AND reason=$2", tenant.TenantId, "media_object:live_stream:"+streamID+":tenant_authority_changed").Scan(&count); err != nil || count != 1 {
		t.Fatalf("dependent enumeration count=%d err=%v", count, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_refresh_inbox SET status='pending',lease_expires_at=NULL,next_attempt_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND source_service='commodore' AND source_event_id=$2", tenant.TenantId, mediaAuthorityDeadlineEvent("tenant", tenant.TenantId, 2)); err != nil {
		t.Fatal(err)
	}
	stale, err := claim()
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale crash-recovery claim: %v %v", stale, err)
	}
	server.processMediaAuthorityDeadlineRefreshRow(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(stale[0]))
	checkVersion(3)
	makeDue(3)
	claimed, err = claim()
	if err != nil || len(claimed) != 1 {
		t.Fatalf("next renewal claim: %v %v", claimed, err)
	}
	if _, err := db.ExecContext(ctx, "CREATE FUNCTION commodore.reject_tenant_fanout_test() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.source_event_id LIKE ''tenant-deadline-fanout:%'' THEN RAISE EXCEPTION ''injected fanout failure''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_tenant_fanout_test BEFORE INSERT ON commodore.media_authority_refresh_inbox FOR EACH ROW EXECUTE FUNCTION commodore.reject_tenant_fanout_test()"); err != nil {
		t.Fatal(err)
	}
	server.processMediaAuthorityDeadlineRefreshRow(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(claimed[0]))
	checkVersion(3)
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND source_event_id=$2", tenant.TenantId, mediaAuthorityDeadlineEvent("tenant", tenant.TenantId, 4)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed fanout left a future renewal: count=%d err=%v", count, err)
	}
	if _, err := db.ExecContext(ctx, "DROP TRIGGER reject_tenant_fanout_test ON commodore.media_authority_refresh_inbox; DROP FUNCTION commodore.reject_tenant_fanout_test()"); err != nil {
		t.Fatal(err)
	}
	makeDue(3)
	claimed, err = claim()
	if err != nil || len(claimed) != 1 {
		t.Fatalf("failed renewal was not retryable: %v %v", claimed, err)
	}
	owner.deleted = true
	server.processMediaAuthorityDeadlineRefreshRow(ctx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(claimed[0]))
	checkVersion(4)
	current, err := q.GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId})
	if err != nil {
		t.Fatal(err)
	}
	deleted := &mediapb.TenantAuthority{}
	if err := proto.Unmarshal(current.Payload, deleted); err != nil || deleted.Lifecycle != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		t.Fatalf("deadline renewal missed deletion: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND reason=$2 AND status<>'completed'", tenant.TenantId, tenantMediaAuthorityDeadlineReason).Scan(&count); err != nil || count != 0 {
		t.Fatalf("tombstone retained renewal chain: count=%d err=%v", count, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE tenant_id=$1 AND source_event_id=$2", tenant.TenantId, "tenant-deadline-fanout:"+tenant.TenantId+":4").Scan(&count); err != nil || count != 1 {
		t.Fatalf("tombstone lost dependent obligation: count=%d err=%v", count, err)
	}
}
