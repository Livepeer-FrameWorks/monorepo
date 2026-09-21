package grpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	meteringpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/metering_contract"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCompileLiveStreamSecretPreservesAuthenticatedEmptyTargetSet(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("FROM commodore.push_targets").WithArgs("stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "target_uri"}))
	s := &CommodoreServer{
		db: db,
		mediaAuthorityRecipients: sharedauthority.SealRecipientSet{
			"cell-1": {},
		},
	}
	secret, err := s.compileLiveStreamSecret(context.Background(), "live:stream-1", "stream-1", "tenant-1", "push")
	if err != nil {
		t.Fatal(err)
	}
	if secret == nil || secret.GetAuthorityId() != "live:stream-1" || secret.GetTenantId() != "tenant-1" || len(secret.GetPushTargets()) != 0 {
		t.Fatalf("empty desired target set was not authenticated: %+v", secret)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type unusedMediaAuthorityTenantSource struct{}

func (unusedMediaAuthorityTenantSource) GetTenant(context.Context, string) (*quartermasterpb.GetTenantResponse, error) {
	panic("unused")
}
func (unusedMediaAuthorityTenantSource) GetTenantEntitlement(context.Context, string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	panic("unused")
}
func (unusedMediaAuthorityTenantSource) ListActiveTenants(context.Context) ([]string, error) {
	panic("unused")
}

type unusedMediaAuthorityBillingSource struct{}

func (unusedMediaAuthorityBillingSource) GetTenantBillingStatus(context.Context, string) (*purserpb.GetTenantBillingStatusResponse, error) {
	panic("unused")
}
func (unusedMediaAuthorityBillingSource) GetTenantAdmissionStatus(context.Context, string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	panic("unused")
}

func TestCurrentTenantServePolicyUsesCurrentSignedDecision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		authority *mediaauthoritypb.TenantAuthority
		wantIDs   []string
		wantAllow bool
	}{
		{
			name: "active exact grants",
			authority: &mediaauthoritypb.TenantAuthority{
				TenantId: "tenant-1", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision:   mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
				OfficialClusterId: "official-1", AllowPlatformSharedPlayback: false,
				EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{ClusterId: "private-1"}, {ClusterId: "official-1"}},
			},
			wantIDs: []string{"private-1", "official-1"},
		},
		{
			name: "suspended resolves explicit deny",
			authority: &mediaauthoritypb.TenantAuthority{
				TenantId: "tenant-1", Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
				BillingDecision:   mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED,
				OfficialClusterId: "official-1", AllowPlatformSharedPlayback: true,
				EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{ClusterId: "private-1"}},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			payload, err := proto.Marshal(tc.authority)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`SELECT versions\.payload, versions\.valid_until`).
				WithArgs("tenant", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(payload, time.Now().Add(time.Hour)))
			s := &CommodoreServer{
				db: db, authorityTenantSource: unusedMediaAuthorityTenantSource{}, authorityBillingSource: unusedMediaAuthorityBillingSource{},
				mediaAuthorityKeyID: "key-1", mediaAuthorityPrivateKey: make(ed25519.PrivateKey, ed25519.PrivateKeySize),
			}
			official, allow, peers, ok := s.currentTenantServePolicy(context.Background(), "tenant-1")
			if !ok {
				t.Fatal("current authority was not resolved")
			}
			ids := make([]string, 0, len(peers))
			for _, peer := range peers {
				ids = append(ids, peer.GetClusterId())
			}
			if !equalStrings(ids, tc.wantIDs) || allow != tc.wantAllow {
				t.Fatalf("policy = official %q allow %v peers %v", official, allow, ids)
			}
			if len(tc.wantIDs) == 0 && official != "" {
				t.Fatalf("denied authority retained official cluster %q", official)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMediaAuthorityRefreshRunsWhileDeliveryIsBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	deliveryStarted := make(chan struct{})
	refreshAdvanced := make(chan struct{})
	var refreshes atomic.Int32
	go func() {
		defer close(done)
		runMediaAuthorityWorkerGroup(ctx,
			func(context.Context) {
				if refreshes.Add(1) == 2 {
					close(refreshAdvanced)
				}
			},
			func(ctx context.Context) {
				select {
				case <-deliveryStarted:
				default:
					close(deliveryStarted)
				}
				<-ctx.Done()
			},
		)
	}()
	select {
	case <-deliveryStarted:
	case <-time.After(2 * mediaAuthorityWorkerInterval):
		t.Fatal("delivery worker did not start")
	}
	select {
	case <-refreshAdvanced:
	case <-time.After(3 * mediaAuthorityWorkerInterval):
		t.Fatal("blocked delivery stopped the independent refresh worker")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("authority workers did not stop after cancellation")
	}
}

func TestMediaAuthorityDeliveryDeadlineCoversWholeOperation(t *testing.T) {
	started := make(chan struct{})
	err := runMediaAuthorityDelivery(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("delivery error = %v, want context.DeadlineExceeded", err)
	}
	if mediaAuthorityDeliveryBatch > mediaAuthorityDeliveryWorkers {
		t.Fatalf("claimed batch %d can outlive one bounded worker wave of %d", mediaAuthorityDeliveryBatch, mediaAuthorityDeliveryWorkers)
	}
}

func TestBuildTenantAuthorityTargetsControlAndEligibleCells(t *testing.T) {
	issuedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	expiresAt := issuedAt.Add(6 * time.Hour)
	tenant := &quartermasterpb.Tenant{
		Id: "tenant-1", IsActive: true,
		PrimaryClusterId: stringPointer("media-a"), OfficialClusterId: stringPointer("media-official"),
	}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
		{
			ClusterId: "media-a", ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			AccessLevel:  "shared", ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-c", "cell-b", "cell-a"},
			AccessExpiresAt: timestamppb.New(expiresAt),
		},
		{
			ClusterId: "media-official", ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			AccessLevel:  "shared", ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-a"},
			AccessExpiresAt: timestamppb.New(expiresAt),
		},
	}}
	billing := &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}
	admission := &purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1}

	payload, targets, validUntil, revisions, err := buildTenantAuthority(tenant, entitlement, billing, admission, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targets, []string{"cell-a", "cell-b", "cell-c"}; !equalStrings(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if !validUntil.Equal(expiresAt) {
		t.Fatalf("valid until = %s, want %s", validUntil, expiresAt)
	}
	if payload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW || !payload.GetAllowPlatformSharedPlayback() {
		t.Fatalf("unexpected positive authority: %+v", payload)
	}
	if payload.GetPreferredClusterId() != "media-a" || payload.GetOfficialClusterId() != "media-official" {
		t.Fatalf("cluster roles = preferred %q official %q", payload.GetPreferredClusterId(), payload.GetOfficialClusterId())
	}
	grant := payload.GetEffectiveClusterGrants()[0]
	if grant.GetControlCellId() != "cell-a" || !equalStrings(grant.GetEligibleServingCellIds(), []string{"cell-a", "cell-b", "cell-c"}) {
		t.Fatalf("signed grant cell scope = control %q eligible %v", grant.GetControlCellId(), grant.GetEligibleServingCellIds())
	}
	if len(revisions) != 2 || revisions[0].GetService() != "purser" || revisions[1].GetService() != "quartermaster" {
		t.Fatalf("source revisions are not stable: %+v", revisions)
	}
}

func TestBuildTenantAuthorityExcludesNonMediaOwnerClusters(t *testing.T) {
	issuedAt := time.Now().UTC()
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
		{
			ClusterId: "core-control", ClusterType: "central", ControlCellId: "core-control",
			AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
		},
		{
			ClusterId: "media-eu", ClusterType: "edge", ClusterClass: "platform_official", ControlCellId: "media-eu",
			AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		},
	}}
	payload, targets, _, _, err := buildTenantAuthority(
		&quartermasterpb.Tenant{Id: "tenant-1", IsActive: true}, entitlement,
		&purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1}, issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targets, []string{"media-eu"}; !equalStrings(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if len(payload.GetEffectiveClusterGrants()) != 1 || payload.GetEffectiveClusterGrants()[0].GetClusterId() != "media-eu" {
		t.Fatalf("media grants = %+v", payload.GetEffectiveClusterGrants())
	}
}

func TestBuildTenantAuthorityDropsOfficialClusterOutsideEffectiveGrants(t *testing.T) {
	issuedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	payload, _, _, _, err := buildTenantAuthority(
		&quartermasterpb.Tenant{Id: "tenant-1", IsActive: true, OfficialClusterId: stringPointer("inactive-official")},
		&quartermasterpb.GetTenantEntitlementResponse{},
		&purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1},
		issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GetOfficialClusterId() != "" {
		t.Fatalf("official cluster outside effective grants = %q, want empty", payload.GetOfficialClusterId())
	}
}

func TestBuildTenantAuthorityAllowsActiveTenantWithoutServingGrant(t *testing.T) {
	issuedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	payload, targets, validUntil, revisions, err := buildTenantAuthority(
		&quartermasterpb.Tenant{Id: "tenant-unassigned", IsActive: true},
		&quartermasterpb.GetTenantEntitlementResponse{},
		&purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1},
		issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
		t.Fatalf("billing decision = %s", payload.GetBillingDecision())
	}
	if len(payload.GetEffectiveClusterGrants()) != 0 || len(targets) != 0 {
		t.Fatalf("unassigned authority has grants=%v targets=%v", payload.GetEffectiveClusterGrants(), targets)
	}
	if !validUntil.Equal(issuedAt.Add(mediaAuthorityValidity)) || len(revisions) != 2 {
		t.Fatalf("invalid history authority: valid_until=%s revisions=%v", validUntil, revisions)
	}
}

func TestPersistMediaObjectAuthorityAllowsHistoryWithoutServingTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	payload := &mediaauthoritypb.MediaObjectAuthority{
		SchemaVersion: 1, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		TenantId: "tenant-unassigned", InternalName: "stream-internal", PlaybackId: "playback-id",
		Lifecycle:      mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{
			StreamId: "stream-1", IngestMode: "push",
		}},
	}
	mock.ExpectBegin()
	// A live object's change goes to the cells that may still hold a valid copy,
	// not to every cell that ever held it.
	mock.ExpectExec("RetireMediaAuthorityTargets").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("LockMediaAuthorityTargetHorizons").WillReturnRows(sqlmock.NewRows([]string{"cell_id", "correction_until"}))
	mock.ExpectQuery("ListMediaAuthorityHoldingCells").
		WithArgs("media_object", "live_stream:stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"cell_id"}))
	expectNoCurrentMediaAuthorityPublication(mock, "media_object", "live_stream:stream-1")
	mock.ExpectQuery(`(?s)INSERT INTO commodore\.media_authority_counters.*RETURNING last_version`).
		WithArgs("media_object", "live_stream:stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"last_version"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_versions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_current`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 0))
	expectMediaAuthorityRenewalScheduled(mock, "object_deadline", "media_object:live_stream:stream-1", 1)
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, mediaAuthorityKeyID: "signer-1", mediaAuthorityPrivateKey: privateKey}
	err = server.persistMediaObjectAuthority(
		context.Background(), "live_stream:stream-1", payload, nil,
		[]*mediaauthoritypb.AuthoritySourceRevision{{Service: "commodore", Revision: "stream-revision"}},
		issuedAt, issuedAt.Add(mediaAuthorityValidity),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// expectTenantPublicationFollowUps scripts what every tenant publication does
// after its deliveries are queued: objects waiting for this tenant's authority
// are re-armed, and the cells' features decide whether objects are still capped
// by the tenant's validity.
func expectTenantPublicationFollowUps(mock sqlmock.Sqlmock, tenantID string, cells ...string) {
	mock.ExpectExec("RearmMediaAuthorityObligationsAwaitingTenant").WithArgs(tenantID).WillReturnResult(sqlmock.NewResult(0, 0))
	if len(cells) == 0 {
		return
	}
	features := sqlmock.NewRows([]string{"cell_id", "long_validity_ready", "use_reports_ready"})
	for _, cell := range cells {
		features.AddRow(cell, true, true)
	}
	mock.ExpectQuery("ListMediaCellAuthorityFeatures").WillReturnRows(features)
}

// expectNoCurrentMediaAuthorityPublication scripts the publication decision for
// an authority that has never been published, which always publishes.
func expectNoCurrentMediaAuthorityPublication(mock sqlmock.Sqlmock, authorityKind, authorityID string) {
	mock.ExpectQuery(`(?s)SELECT versions\.authority_version, versions\.content_digest`).
		WithArgs(authorityKind, authorityID).
		WillReturnRows(sqlmock.NewRows([]string{"authority_version", "content_digest", "dependents_digest", "issued_at", "refresh_after", "valid_until"}))
}

// expectMediaAuthorityRenewalScheduled asserts that publication schedules the
// renewal of exactly the version it published, in the lane for its kind.
func expectMediaAuthorityRenewalScheduled(mock sqlmock.Sqlmock, lane, targetKey string, version int64) {
	mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
		WithArgs(lane, targetKey, sqlmock.AnyArg(), sqlmock.AnyArg(), "renewal", "commodore", "renewal:"+targetKey, sqlmock.AnyArg(), version).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestTenantWithoutServingRecipientsCompilesObjectsAsInactive(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
	}
	if tenantCanServeMediaObjects(tenant, nil) {
		t.Fatal("cell-less tenant was considered able to serve media objects")
	}
	tenant.EffectiveClusterGrants = []*mediaauthoritypb.TenantClusterGrant{{ControlCellId: "cell-a"}}
	if !tenantCanServeMediaObjects(tenant, activeTenantAuthorityCells(tenant)) {
		t.Fatal("active tenant with a serving recipient was rejected")
	}
	tenant.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
	if tenantCanServeMediaObjects(tenant, activeTenantAuthorityCells(tenant)) {
		t.Fatal("suspended tenant was considered able to serve media objects")
	}
}

func TestTenantAuthorityNotFoundRequiresExplicitQuartermasterResponse(t *testing.T) {
	if !tenantAuthorityNotFound(&quartermasterpb.GetTenantResponse{Error: "Tenant not found"}) {
		t.Fatal("explicit Quartermaster not-found response was not recognized")
	}
	for _, resp := range []*quartermasterpb.GetTenantResponse{
		nil,
		{},
		{Error: "database unavailable"},
		{Error: "Tenant not found", Tenant: &quartermasterpb.Tenant{Id: "tenant-1"}},
	} {
		if tenantAuthorityNotFound(resp) {
			t.Fatalf("ambiguous response was treated as an authoritative deletion: %+v", resp)
		}
	}
}

func TestDeletedTenantAuthorityCarriesNoPositiveAuthority(t *testing.T) {
	payload := deletedTenantAuthorityPayload("tenant-1")
	if payload.GetTenantId() != "tenant-1" || payload.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE ||
		payload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_INACTIVE || payload.GetDecisionReason() != "tenant_deleted" {
		t.Fatalf("unexpected tenant tombstone: %+v", payload)
	}
	if payload.GetAllowPlatformSharedPlayback() || len(payload.GetEffectiveClusterGrants()) != 0 || payload.GetResourceLimits() != nil ||
		len(payload.GetAllowances()) != 0 || payload.GetDvrPolicy() != nil || payload.GetTierLevel() != 0 {
		t.Fatalf("tenant tombstone retained positive authority: %+v", payload)
	}
}

func TestCompileDeletedTenantAuthorityDeliversTombstoneToPriorCells(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tenantID := "tenant-1"
	previous, err := proto.Marshal(&mediaauthoritypb.TenantAuthority{SchemaVersion: 1, TenantId: tenantID})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`(?s)SELECT versions\.payload, versions\.valid_until.*media_authority_current`).
		WithArgs("tenant", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(previous, time.Now().UTC().Add(time.Hour)))
	mock.ExpectBegin()
	mock.ExpectExec("RetireMediaAuthorityTargets").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("LockMediaAuthorityTargetHorizons").WillReturnRows(sqlmock.NewRows([]string{"cell_id", "correction_until"}))
	mock.ExpectQuery(`SELECT cell_id\s+FROM commodore\.media_authority_targets`).
		WithArgs("tenant", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("cell-a"))
	expectNoCurrentMediaAuthorityPublication(mock, "tenant", tenantID)
	mock.ExpectQuery(`(?s)INSERT INTO commodore\.media_authority_counters.*RETURNING last_version`).
		WithArgs("tenant", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"last_version"}).AddRow(int64(2)))
	mock.ExpectQuery("LockCurrentTenantMediaAuthority").WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(previous, time.Now().Add(time.Hour)))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_versions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_current`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_targets`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 1))
	// A tombstone is terminal: it schedules no renewal, but its objects are
	// refreshed so they stop deriving from the deleted tenant.
	expectTenantPublicationFollowUps(mock, tenantID, "cell-a")
	mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
		WithArgs("event", "tenant_media_objects:"+tenantID, "tenant_media_objects", tenantID,
			"tenant_media_objects:tenant_authority_changed", "commodore", "tenant-version:2", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, mediaAuthorityKeyID: "signer-1", mediaAuthorityPrivateKey: privateKey}
	if err := server.compileDeletedTenantAuthority(context.Background(), tenantID); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActiveTenantAuthorityCellsExcludeHistoricalRecipients(t *testing.T) {
	tenant := &mediaauthoritypb.TenantAuthority{
		Lifecycle:       mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
			{ControlCellId: "cell-current", EligibleServingCellIds: []string{"cell-current", "cell-serve"}},
		},
	}
	if got, want := activeTenantAuthorityCells(tenant), []string{"cell-current", "cell-serve"}; !equalStrings(got, want) {
		t.Fatalf("active cells = %v, want %v", got, want)
	}
	// cell-revoked may still be a delivery target for a signed denial, but it is
	// deliberately absent from the payload-derived secret recipient set.
	tenant.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
	if got := activeTenantAuthorityCells(tenant); len(got) != 0 {
		t.Fatalf("inactive authority retained secret recipients: %v", got)
	}
}

func TestBuildTenantAuthorityDeniedCarriesNoPositiveGrants(t *testing.T) {
	issuedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	tenant := &quartermasterpb.Tenant{Id: "tenant-1", IsActive: true}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{{
		ClusterId: "media-a", ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
		AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER,
		ClusterClass: "tenant_private", ControlCellId: "cell-a",
	}}}
	billing := &purserpb.GetTenantBillingStatusResponse{BillingModel: "prepaid", IsBalanceNegative: true}

	payload, targets, _, _, err := buildTenantAuthority(tenant, entitlement, billing, &purserpb.GetTenantAdmissionStatusResponse{TierLevel: 4}, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED {
		t.Fatalf("decision = %s", payload.GetBillingDecision())
	}
	if payload.GetAllowPlatformSharedPlayback() || len(payload.GetEffectiveClusterGrants()) != 0 || len(targets) != 0 {
		t.Fatalf("denied authority carried positive grants: payload=%+v targets=%v", payload, targets)
	}
}

func TestBuildTenantAuthorityRejectsUnscopedGrant(t *testing.T) {
	issuedAt := time.Now().UTC()
	tenant := &quartermasterpb.Tenant{Id: "tenant-1", IsActive: true}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{{
		ClusterId: "media-a", ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active",
		AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, ClusterClass: "tenant_private",
	}}}
	billing := &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"}
	if _, _, _, _, err := buildTenantAuthority(tenant, entitlement, billing, &purserpb.GetTenantAdmissionStatusResponse{TierLevel: 4}, issuedAt); err == nil {
		t.Fatal("expected missing control-cell scope to fail")
	}
}

func TestBuildTenantAuthorityFiltersClusterClassesByPurserTier(t *testing.T) {
	issuedAt := time.Now().UTC()
	tenant := &quartermasterpb.Tenant{Id: "tenant-1", IsActive: true, OfficialClusterId: stringPointer("official")}
	peer := func(id, class string) *clusterpeerpb.TenantClusterPeer {
		return &clusterpeerpb.TenantClusterPeer{
			ClusterId: id, ClusterType: "edge", ClusterClass: class, ControlCellId: "cell-" + id,
			AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		}
	}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
		peer("official", "platform_official"), peer("market", "third_party_marketplace"), peer("private", "tenant_private"),
	}}
	payload, targets, _, _, err := buildTenantAuthority(
		tenant, entitlement, &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 2}, issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targets, []string{"cell-market", "cell-official"}; !equalStrings(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	if len(payload.GetEffectiveClusterGrants()) != 2 || payload.GetEffectiveClusterGrants()[1].GetClusterId() != "official" {
		t.Fatalf("filtered grants = %+v", payload.GetEffectiveClusterGrants())
	}
}

func TestMediaAuthorityClusterClassAllowedNormalizesCase(t *testing.T) {
	for _, class := range []string{"platform_official", " PLATFORM_OFFICIAL ", "Platform_Official"} {
		if !mediaAuthorityClusterClassAllowed(1, class) {
			t.Fatalf("official class %q should be allowed after normalization", class)
		}
	}
	if !mediaAuthorityClusterClassAllowed(2, "THIRD_PARTY_MARKETPLACE") {
		t.Fatal("marketplace class should be case-insensitive")
	}
	if !mediaAuthorityPeerAllowed(1, &clusterpeerpb.TenantClusterPeer{
		AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE,
		ClusterType:  " EDGE ", ClusterClass: " TENANT_PRIVATE ",
	}) {
		t.Fatal("private-invite class should use the same normalization")
	}
}

func TestBuildTenantAuthorityDerivesPreferredFromFilteredGrants(t *testing.T) {
	issuedAt := time.Now().UTC()
	peer := func(id, class, role string) *clusterpeerpb.TenantClusterPeer {
		return &clusterpeerpb.TenantClusterPeer{
			ClusterId: id, ClusterType: "edge", ClusterClass: class, Role: role, ControlCellId: "cell-" + id,
			AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		}
	}
	tenant := &quartermasterpb.Tenant{
		Id: "tenant-1", IsActive: true,
		PrimaryClusterId: stringPointer("private-primary"), OfficialClusterId: stringPointer("official"),
	}
	payload, _, _, _, err := buildTenantAuthority(
		tenant,
		&quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
			peer("official", "platform_official", "preferred"),
			peer("private-primary", "tenant_private", "subscribed"),
		}},
		&purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1},
		issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := payload.GetPreferredClusterId(); got != "official" {
		t.Fatalf("preferred cluster = %q, want filtered routing fallback official", got)
	}
	if got := payload.GetOfficialClusterId(); got != "official" {
		t.Fatalf("official cluster = %q, want explicit official identity", got)
	}
}

func TestBuildTenantAuthorityDoesNotInventOfficialCluster(t *testing.T) {
	issuedAt := time.Now().UTC()
	payload, _, _, _, err := buildTenantAuthority(
		&quartermasterpb.Tenant{Id: "tenant-1", IsActive: true, PrimaryClusterId: stringPointer("primary")},
		&quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{{
			ClusterId: "primary", ClusterType: "edge", ClusterClass: "platform_official", ControlCellId: "cell-primary",
			AccessActive: true, SubscriptionStatus: "active", Role: "preferred",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
		}}},
		&purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 1},
		issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GetPreferredClusterId() != "primary" || payload.GetOfficialClusterId() != "" {
		t.Fatalf("cluster roles = preferred %q official %q, want primary/empty", payload.GetPreferredClusterId(), payload.GetOfficialClusterId())
	}
}

func TestBuildTenantAuthorityUsesGrantProvenanceBeforeTier(t *testing.T) {
	issuedAt := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	tenant := &quartermasterpb.Tenant{Id: "tenant-1", IsActive: true, OfficialClusterId: stringPointer("official")}
	peer := func(id, class string, source clusterpeerpb.TenantClusterAccessSource) *clusterpeerpb.TenantClusterPeer {
		return &clusterpeerpb.TenantClusterPeer{
			ClusterId: id, ClusterType: "edge", ClusterClass: class, ControlCellId: "cell-" + id,
			AccessActive: true, SubscriptionStatus: "active", AccessSource: source,
		}
	}
	entitlement := &quartermasterpb.GetTenantEntitlementResponse{EffectiveAccess: []*clusterpeerpb.TenantClusterPeer{
		peer("official", "platform_official", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER),
		peer("owned-private", "tenant_private", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER),
		peer("invited-private", "tenant_private", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE),
		peer("forged-marketplace-invite", "third_party_marketplace", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PRIVATE_INVITE),
		peer("tier-private", "tenant_private", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER),
	}}
	payload, targets, _, _, err := buildTenantAuthority(
		tenant, entitlement, &purserpb.GetTenantBillingStatusResponse{BillingModel: "postpaid"},
		&purserpb.GetTenantAdmissionStatusResponse{TierLevel: 3}, issuedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := targets, []string{"cell-invited-private", "cell-official", "cell-owned-private"}; !equalStrings(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	got := make([]string, 0, len(payload.GetEffectiveClusterGrants()))
	for _, grant := range payload.GetEffectiveClusterGrants() {
		got = append(got, grant.GetClusterId())
	}
	want := []string{"invited-private", "official", "owned-private"}
	if !equalStrings(got, want) {
		t.Fatalf("grants = %v, want %v", got, want)
	}
}

func TestRequestMediaAuthorityRefreshRequiresServiceAndFoldsIntoTheTargetObligation(t *testing.T) {
	db, mock, dbErr := sqlmock.New()
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	defer db.Close()
	s := &CommodoreServer{db: db}
	req := &commodorepb.RequestMediaAuthorityRefreshRequest{SourceService: "purser", SourceEventId: "event-1", TenantId: "tenant-1", Reason: "billing_gate_changed"}

	if _, err := s.RequestMediaAuthorityRefresh(context.Background(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthenticated code = %s, want PermissionDenied", status.Code(err))
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	// A redelivered owner event is the same obligation, so it is accepted again
	// rather than reported as a duplicate the owner would have to interpret.
	for range 2 {
		mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
			WithArgs("event", "tenant:tenant-1", "tenant", "tenant-1", "billing_gate_changed", "purser", "event-1", sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		resp, err := s.RequestMediaAuthorityRefresh(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if !resp.GetAccepted() {
			t.Fatal("durably recorded source event was not accepted")
		}
	}

	// A stream's process configuration is read from the tenant's tier, which the
	// tenant authority does not carry. A tier change therefore refreshes the
	// tenant's objects directly; a metering change must not.
	for _, tier := range []string{"subscription_authority_changed", "billing_tier_authority_changed"} {
		mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
			WithArgs("event", "tenant:tenant-1", "tenant", "tenant-1", tier, "purser", "event-2", sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
			WithArgs("event", "tenant_media_objects:tenant-1", "tenant_media_objects", "tenant-1", "tenant_media_objects:"+tier, "purser", "event-2", sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		if _, err := s.RequestMediaAuthorityRefresh(ctx, &commodorepb.RequestMediaAuthorityRefreshRequest{SourceService: "purser", SourceEventId: "event-2", TenantId: "tenant-1", Reason: tier}); err != nil {
			t.Fatal(err)
		}
	}
	mock.ExpectExec("SELECT commodore.enqueue_media_authority_obligation").
		WithArgs("event", "tenant:tenant-1", "tenant", "tenant-1", "allowance_usage_changed", "purser", "event-3", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if _, err := s.RequestMediaAuthorityRefresh(ctx, &commodorepb.RequestMediaAuthorityRefreshRequest{SourceService: "purser", SourceEventId: "event-3", TenantId: "tenant-1", Reason: "allowance_usage_changed"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAuthorityTargetForReasonSharesOneTargetPerAuthority(t *testing.T) {
	const tenantID = "10000000-0000-0000-0000-000000000001"
	for _, tc := range []struct {
		reason string
		want   commodoredb.MediaAuthorityTarget
	}{
		{"billing_gate_changed", commodoredb.MediaAuthorityTarget{Key: "tenant:" + tenantID, Kind: "tenant"}},
		{"tenant_media_objects:signing_key_changed", commodoredb.MediaAuthorityTarget{Key: "tenant_media_objects:" + tenantID, Kind: "tenant_media_objects"}},
		{"media_object:live_stream:stream-1:stream_changed", commodoredb.MediaAuthorityTarget{Key: "media_object:live_stream:stream-1", Kind: "live_stream"}},
		// A chapter and the VOD row backing it are one authority: both reasons
		// must land on the same obligation.
		{"media_object:vod:asset-1:artifact_changed", commodoredb.MediaAuthorityTarget{Key: "media_object:artifact:asset-1", Kind: "artifact"}},
		{"media_object:chapter:asset-1:tenant_authority_changed", commodoredb.MediaAuthorityTarget{Key: "media_object:artifact:asset-1", Kind: "artifact"}},
	} {
		got := commodoredb.MediaAuthorityTargetForReason(tenantID, tc.reason)
		if got != tc.want {
			t.Fatalf("target for %q = %+v, want %+v", tc.reason, got, tc.want)
		}
		if tc.want.Kind == "live_stream" && got.ObjectID() != "stream-1" {
			t.Fatalf("live stream object id = %q", got.ObjectID())
		}
		if tc.want.Kind == "artifact" && got.ObjectID() != "asset-1" {
			t.Fatalf("artifact object id = %q", got.ObjectID())
		}
	}
}

func TestRequestMediaAuthorityReplayRequiresServiceAndValidClock(t *testing.T) {
	db, mock, dbErr := sqlmock.New()
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	defer db.Close()
	s := &CommodoreServer{db: db}
	req := &commodorepb.RequestMediaAuthorityReplayRequest{ControlCellId: " cell-a "}

	if _, err := s.RequestMediaAuthorityReplay(context.Background(), req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthenticated code = %s, want PermissionDenied", status.Code(err))
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	if _, err := s.RequestMediaAuthorityReplay(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing timestamp: %v", err)
	}
	for _, skew := range []time.Duration{-time.Hour, time.Hour} {
		req.AsOf = timestamppb.New(time.Now().Add(skew))
		if _, err := s.RequestMediaAuthorityReplay(ctx, req); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("skew %s: %v", skew, err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The fanout is one statement, so it cannot be half applied and needs no
// transaction. Which objects it selects and which lane they land in is SQL, and
// is asserted against a real database in TestMediaAuthorityFollowsUse_RealPG.
func TestTenantMediaObjectFanoutIsOneStatement(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	const tenantID = "10000000-0000-0000-0000-000000000001"
	mock.ExpectQuery("EnqueueTenantMediaObjectRefreshes").
		WithArgs("tenant_media_objects:fanout", "tenant-fanout:"+tenantID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"enqueued"}).AddRow(int64(2)))
	if err := s.enqueueTenantMediaObjectRefreshes(context.Background(), tenantID); err != nil {
		t.Fatalf("enqueueTenantMediaObjectRefreshes: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Media objects are re-signed only when a tenant field they derive from moves.
// Metering moves allowances, limits, and decision text on every usage report;
// if those reached the dependents digest, metering would become
// O(objects x cells) signing work.
func TestTenantDependentsDigestIgnoresMeteringOnlyFields(t *testing.T) {
	base := &mediaauthoritypb.TenantAuthority{
		SchemaVersion: sharedauthority.SchemaVersion, TenantId: "10000000-0000-0000-0000-000000000001",
		Lifecycle:         mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		BillingDecision:   mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
		OfficialClusterId: "media-eu-1", PreferredClusterId: "media-eu-1",
		EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{{ClusterId: "media-eu-1", ControlCellId: "media-eu-1"}},
	}
	digest := func(tenant *mediaauthoritypb.TenantAuthority) string {
		sum, err := sharedauthority.TenantDependentsDigest(tenant)
		if err != nil {
			t.Fatal(err)
		}
		return string(sum)
	}
	want := digest(base)

	metered := proto.CloneOf(base)
	metered.DecisionReason = "allowance nearly exhausted"
	metered.Allowances = []*meteringpb.MeterAllowance{{}}
	if digest(metered) != want {
		t.Fatal("a metering-only tenant change would fan out to every media object")
	}
	for name, mutate := range map[string]func(*mediaauthoritypb.TenantAuthority){
		"billing decision": func(t *mediaauthoritypb.TenantAuthority) {
			t.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED
		},
		"lifecycle": func(t *mediaauthoritypb.TenantAuthority) {
			t.Lifecycle = mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
		},
		"preferred cluster": func(t *mediaauthoritypb.TenantAuthority) { t.PreferredClusterId = "media-us-1" },
		"grants":            func(t *mediaauthoritypb.TenantAuthority) { t.EffectiveClusterGrants[0].ControlCellId = "media-us-1" },
	} {
		changed := proto.CloneOf(base)
		mutate(changed)
		if digest(changed) == want {
			t.Fatalf("%s change did not move the dependents digest, so objects would keep stale authority", name)
		}
	}
}

// Sealing draws fresh key material per compile. Two compiles of identical state
// must still digest equal, or every live stream would publish on every compile;
// a changed secret or recipient must digest differently, or a rotated secret
// would never reach the cell.
func TestMediaObjectContentDigestIsStableAcrossResealsAndMovesWithTheSecret(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const authorityID = "live_stream:20000000-0000-0000-0000-000000000001"
	payload := func(box string) *mediaauthoritypb.MediaObjectAuthority {
		return &mediaauthoritypb.MediaObjectAuthority{
			SchemaVersion: sharedauthority.SchemaVersion, TenantId: "tenant-1", InternalName: "stream",
			Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{
				StreamId:          "20000000-0000-0000-0000-000000000001",
				SealedCellSecrets: []*mediaauthoritypb.SealedCellSecret{{AudienceCellId: "cell-a", Ciphertext: []byte(box)}},
			}},
		}
	}
	digest := func(box string, plaintext string, recipients ...string) []byte {
		commitment, commitErr := sharedauthority.SecretCommitment(privateKey, authorityID, []byte(plaintext), recipients)
		if commitErr != nil {
			t.Fatal(commitErr)
		}
		sum, ok, digestErr := sharedauthority.MediaObjectContentDigest(payload(box), "signer-1", [][]byte{commitment})
		if digestErr != nil || !ok {
			t.Fatalf("digest ok=%v err=%v", ok, digestErr)
		}
		return sum
	}
	first := digest("sealed-with-nonce-1", "rtmp://target/key", "cell-a\x00key-a")
	if resealed := digest("sealed-with-nonce-2", "rtmp://target/key", "cell-a\x00key-a"); !bytes.Equal(first, resealed) {
		t.Fatal("resealing identical secret state changed the content digest")
	}
	if rotated := digest("sealed-with-nonce-1", "rtmp://target/rotated", "cell-a\x00key-a"); bytes.Equal(first, rotated) {
		t.Fatal("a changed secret kept the same content digest")
	}
	if recipients := digest("sealed-with-nonce-1", "rtmp://target/key", "cell-a\x00key-b"); bytes.Equal(first, recipients) {
		t.Fatal("a changed recipient key kept the same content digest")
	}
	if _, ok, _ := sharedauthority.MediaObjectContentDigest(payload("sealed"), "signer-1", nil); ok {
		t.Fatal("sealed content without a commitment must have no stable digest")
	}
}

func TestMediaAuthorityTenantCompileUsesFenceWithoutPinnedConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &CommodoreServer{db: db}
	tenantID := "10000000-0000-0000-0000-000000000001"
	scopeKey := "tenant:" + tenantID
	mock.ExpectQuery("INSERT INTO commodore.media_authority_compile_fences").
		WithArgs(scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(int64(7)))
	called := false
	err = s.withMediaAuthorityCompileFence(context.Background(), scopeKey, func(ctx context.Context) error {
		called = true
		fence, ok := ctx.Value(mediaAuthorityCompileFenceContextKey{}).(mediaAuthorityCompileFence)
		if !ok || fence.scopeKey != scopeKey || fence.generation != 7 {
			t.Fatalf("compile fence = %+v, %v", fence, ok)
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("withMediaAuthorityCompileFence = called:%v err:%v", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An obligation's target key is also its compile-fence scope, so it must equal
// the scope the publishing transaction locks ("media_object:" + authority ID).
func TestMediaAuthorityTargetKeyEqualsThePublishFenceScope(t *testing.T) {
	stream := commodoredb.LiveStreamMediaAuthorityTarget("20000000-0000-0000-0000-000000000001")
	if want := "media_object:" + sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001"); stream.Key != want {
		t.Fatalf("live-stream target key = %q, want %q", stream.Key, want)
	}
	artifact := commodoredb.ArtifactMediaAuthorityTarget("30000000-0000-0000-0000-000000000001")
	if want := "media_object:" + sharedauthority.ArtifactAuthorityID("30000000-0000-0000-0000-000000000001"); artifact.Key != want {
		t.Fatalf("artifact target key = %q, want %q", artifact.Key, want)
	}
	if got := mediaObjectAuthorityTarget(sharedauthority.LiveStreamAuthorityID("20000000-0000-0000-0000-000000000001")); got != stream {
		t.Fatalf("authority ID mapped to %+v, want %+v", got, stream)
	}
	if got := mediaObjectAuthorityTarget(sharedauthority.ArtifactAuthorityID("30000000-0000-0000-0000-000000000001")); got != artifact {
		t.Fatalf("authority ID mapped to %+v, want %+v", got, artifact)
	}
}

func TestAuthorityBackoffUsesBoundedDeterministicJitter(t *testing.T) {
	first := authorityBackoff(3, "media_object", "artifact:one")
	if again := authorityBackoff(3, "media_object", "artifact:one"); again != first {
		t.Fatalf("same retry identity produced unstable backoff: %v then %v", first, again)
	}
	if first < 4*time.Second || first >= 6*time.Second {
		t.Fatalf("attempt 3 backoff %v is outside [4s, 6s)", first)
	}
	if other := authorityBackoff(3, "media_object", "artifact:two"); other == first {
		t.Fatalf("distinct retry identities synchronized at %v", first)
	}
}

func TestMediaAuthorityTenantCompileRejectsSupersededGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tenantID := "10000000-0000-0000-0000-000000000001"
	scopeKey := "tenant:" + tenantID
	ctx := context.WithValue(context.Background(), mediaAuthorityCompileFenceContextKey{}, mediaAuthorityCompileFence{
		scopeKey: scopeKey, generation: 7,
	})
	mock.ExpectQuery("SELECT generation").WithArgs(scopeKey).
		WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(int64(8)))
	fenceErr := lockMediaAuthorityCompileFence(ctx, commodoredb.New(db), scopeKey)
	if fenceErr == nil {
		t.Fatal("superseded compile generation was accepted")
	}
	// Losing the fence is not a failure: the newer compile covers this one, so
	// the obligation completes instead of burning an attempt and backing off.
	if class, _ := classifyAuthorityCompileError(fmt.Errorf("persist: %w", fenceErr)); class != authorityCompileSuperseded {
		t.Fatalf("fence loss classified as %v, want superseded", class)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAuthorityCompileErrorClassification(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		class authorityCompileClass
		code  string
	}{
		"rpc failure retries":         {errors.New("load tenant billing authority: unavailable"), authorityCompileTransient, "transient"},
		"missing seal recipient":      {fmt.Errorf("compile: %w", parkAuthorityCompile("seal_recipient_missing", errors.New("no recipient"))), authorityCompilePark, "seal_recipient_missing"},
		"malformed envelope":          {fmt.Errorf("publish: %w", sharedauthority.ErrMalformed), authorityCompilePark, "malformed_authority"},
		"object before tenant":        {fmt.Errorf("load: %w", errTenantAuthorityMissing), authorityCompileAwaitTenant, "awaiting_tenant_authority"},
		"superseded by newer compile": {fmt.Errorf("lock: %w", errMediaAuthorityCompileSuperseded), authorityCompileSuperseded, "superseded"},
	} {
		if class, code := classifyAuthorityCompileError(tc.err); class != tc.class || code != tc.code {
			t.Errorf("%s: classified (%v, %q), want (%v, %q)", name, class, code, tc.class, tc.code)
		}
	}
}

func TestBuildArtifactAuthorityPayloadPreservesKindParentAndOrigin(t *testing.T) {
	policy := &mediaauthoritypb.PlaybackPolicy{Kind: mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC}
	tenant := &mediaauthoritypb.TenantAuthority{OfficialClusterId: "media-official"}
	for input, want := range map[string]mediaauthoritypb.ArtifactKind{
		"clip":    mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CLIP,
		"dvr":     mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_DVR,
		"vod":     mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_VOD,
		"chapter": mediaauthoritypb.ArtifactKind_ARTIFACT_KIND_CHAPTER,
	} {
		t.Run(input, func(t *testing.T) {
			payload, err := buildArtifactAuthorityPayload(commodoredb.GetArtifactMediaAuthoritySourceRow{
				AuthorityID: "artifact-1", ArtifactKind: input, ArtifactHash: "hash-1", TenantID: "tenant-1",
				UserID: "user-1", StreamID: "parent-stream-1", ParentStreamInternalName: "parent-routing-name",
				InternalName: "artifact-name", PlaybackID: "playback-1",
			}, tenant, policy, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE)
			if err != nil {
				t.Fatal(err)
			}
			artifact := payload.GetArtifact()
			if artifact.GetArtifactKind() != want || artifact.GetParentStreamId() != "parent-stream-1" ||
				artifact.GetParentStreamInternalName() != "parent-routing-name" || payload.GetOriginClusterId() != "media-official" {
				t.Fatalf("artifact payload = %+v", payload)
			}
		})
	}

	if _, err := buildArtifactAuthorityPayload(commodoredb.GetArtifactMediaAuthoritySourceRow{ArtifactKind: "unknown"}, tenant, policy, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE); err == nil {
		t.Fatal("unknown artifact kind was accepted")
	}
}

func TestCompileProtectedChapterPolicyNeverBecomesPublic(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &CommodoreServer{db: db}
	mock.ExpectQuery("SELECT kid, algorithm, public_key_pem").
		WithArgs("10000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"kid", "algorithm", "public_key_pem"}).
			AddRow("kid-1", "ES256", "public-key"))

	policy, err := s.compilePlaybackPolicy(context.Background(), "10000000-0000-0000-0000-000000000001", true,
		`{"type":"jwt","jwt":{"allowed_kids":["kid-1"]}}`)
	if err != nil {
		t.Fatalf("compilePlaybackPolicy: %v", err)
	}
	if policy.GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT {
		t.Fatalf("protected chapter compiled as %v", policy.GetKind())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string { return &value }

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
