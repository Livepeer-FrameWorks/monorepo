package grpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMediaAuthorityRefreshRunsWhileDeliveryIsBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	deliveryStarted := make(chan struct{})
	refreshAdvanced := make(chan struct{})
	var refreshes atomic.Int32
	go func() {
		defer close(done)
		runMediaAuthorityWorkerPair(ctx,
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
			ClusterId: "media-a", AccessActive: true, SubscriptionStatus: "active",
			AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_PLATFORM_TIER,
			AccessLevel:  "shared", ClusterClass: "platform_official", ControlCellId: "cell-a", EligibleServingCellIds: []string{"cell-c", "cell-b", "cell-a"},
			AccessExpiresAt: timestamppb.New(expiresAt),
		},
		{
			ClusterId: "media-official", AccessActive: true, SubscriptionStatus: "active",
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
	mock.ExpectQuery(`SELECT cell_id\s+FROM commodore\.media_authority_targets`).
		WithArgs("media_object", "live_stream:stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"cell_id"}))
	mock.ExpectQuery(`(?s)INSERT INTO commodore\.media_authority_counters.*RETURNING last_version`).
		WithArgs("media_object", "live_stream:stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"last_version"}).AddRow(int64(1)))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_versions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_current`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 0))
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
	mock.ExpectQuery(`SELECT cell_id\s+FROM commodore\.media_authority_targets`).
		WithArgs("tenant", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"cell_id"}).AddRow("cell-a"))
	mock.ExpectQuery(`(?s)INSERT INTO commodore\.media_authority_counters.*RETURNING last_version`).
		WithArgs("tenant", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"last_version"}).AddRow(int64(2)))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_versions`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_current`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_targets`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO commodore\.media_authority_deliveries`).WillReturnResult(sqlmock.NewResult(0, 1))
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
		ClusterId: "media-a", AccessActive: true, SubscriptionStatus: "active",
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
		ClusterId: "media-a", AccessActive: true, SubscriptionStatus: "active",
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
			ClusterId: id, ClusterClass: class, ControlCellId: "cell-" + id,
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
		ClusterClass: " TENANT_PRIVATE ",
	}) {
		t.Fatal("private-invite class should use the same normalization")
	}
}

func TestBuildTenantAuthorityDerivesPreferredFromFilteredGrants(t *testing.T) {
	issuedAt := time.Now().UTC()
	peer := func(id, class, role string) *clusterpeerpb.TenantClusterPeer {
		return &clusterpeerpb.TenantClusterPeer{
			ClusterId: id, ClusterClass: class, Role: role, ControlCellId: "cell-" + id,
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
			ClusterId: "primary", ClusterClass: "platform_official", ControlCellId: "cell-primary",
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
			ClusterId: id, ClusterClass: class, ControlCellId: "cell-" + id,
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

func TestRequestMediaAuthorityRefreshRequiresServiceAndIsIdempotent(t *testing.T) {
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
	mock.ExpectExec("INSERT INTO commodore.media_authority_refresh_inbox").WithArgs("purser", "event-1", "tenant-1", "billing_gate_changed").WillReturnResult(sqlmock.NewResult(0, 1))
	resp, err := s.RequestMediaAuthorityRefresh(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetAccepted() {
		t.Fatal("first source event was not accepted")
	}
	mock.ExpectExec("INSERT INTO commodore.media_authority_refresh_inbox").WithArgs("purser", "event-1", "tenant-1", "billing_gate_changed").WillReturnResult(sqlmock.NewResult(0, 0))
	resp, err = s.RequestMediaAuthorityRefresh(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetAccepted() {
		t.Fatal("duplicate source event reported newly accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestMediaAuthorityReplayRequiresServiceAndRequeuesCurrentSet(t *testing.T) {
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
	mock.ExpectQuery("WITH requeued AS").WithArgs("cell-a").WillReturnRows(sqlmock.NewRows([]string{"requeued_count"}).AddRow(7))
	resp, err := s.RequestMediaAuthorityReplay(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetRequeuedCount() != 7 {
		t.Fatalf("requeued count = %d, want 7", resp.GetRequeuedCount())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantAuthorityRefreshFansOutObjectsBeforeAcknowledgement(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	row := commodoredb.ClaimMediaAuthorityRefreshInboxRow{
		SourceService: "purser", SourceEventID: "billing-1", TenantID: "10000000-0000-0000-0000-000000000001",
	}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id::text AS stream_id, tenant_id::text AS tenant_id").
		WithArgs(row.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{"stream_id", "tenant_id"}).AddRow("20000000-0000-0000-0000-000000000001", row.TenantID))
	mock.ExpectExec("INSERT INTO commodore.media_authority_refresh_inbox").
		WithArgs("commodore", sqlmock.AnyArg(), row.TenantID, "media_object:live_stream:20000000-0000-0000-0000-000000000001:tenant_authority_changed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT id::text AS authority_id, tenant_id::text AS tenant_id").
		WithArgs(row.TenantID).
		WillReturnRows(sqlmock.NewRows([]string{"authority_id", "tenant_id", "artifact_kind"}).AddRow("30000000-0000-0000-0000-000000000001", row.TenantID, "dvr"))
	mock.ExpectExec("INSERT INTO commodore.media_authority_refresh_inbox").
		WithArgs("commodore", sqlmock.AnyArg(), row.TenantID, "media_object:dvr:30000000-0000-0000-0000-000000000001:tenant_authority_changed").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE commodore.media_authority_refresh_inbox").
		WithArgs("purser", "billing-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.completeTenantAuthorityRefresh(context.Background(), row); err != nil {
		t.Fatalf("completeTenantAuthorityRefresh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTenantAuthorityRefreshSkipsObjectFanoutForMeteringOnlyChange(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	row := commodoredb.ClaimMediaAuthorityRefreshInboxRow{
		SourceService: "purser", SourceEventID: "usage-1", TenantID: "10000000-0000-0000-0000-000000000001",
		Reason: "allowance_usage_changed",
	}
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE commodore.media_authority_refresh_inbox").
		WithArgs("purser", "usage-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := s.completeTenantAuthorityRefresh(context.Background(), row); err != nil {
		t.Fatalf("completeTenantAuthorityRefresh: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTenantRefreshRequiresObjectFanout(t *testing.T) {
	for _, test := range []struct {
		source, reason string
		want           bool
	}{
		{"purser", "allowance_usage_changed", false},
		{"purser", "prepaid_admission_gate_changed", false},
		{"purser", "billing_tier_authority_changed", true},
		{"quartermaster", "cluster_access_changed", true},
		{"commodore", "tenant_media_objects:signing_key_changed", true},
	} {
		if got := tenantRefreshRequiresObjectFanout(test.source, test.reason); got != test.want {
			t.Errorf("tenantRefreshRequiresObjectFanout(%q, %q) = %v, want %v", test.source, test.reason, got, test.want)
		}
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

func TestMediaAuthorityCompileScopeSeparatesTenantObjects(t *testing.T) {
	tenantID := "10000000-0000-0000-0000-000000000001"
	first := mediaAuthorityCompileScope(tenantID, "dvr", "30000000-0000-0000-0000-000000000001", true)
	second := mediaAuthorityCompileScope(tenantID, "chapter", "30000000-0000-0000-0000-000000000002", true)
	if first == second || first != "media_object:artifact:30000000-0000-0000-0000-000000000001" ||
		second != "media_object:artifact:30000000-0000-0000-0000-000000000002" {
		t.Fatalf("object compile scopes collapsed: first=%q second=%q", first, second)
	}
	if got := mediaAuthorityCompileScope(tenantID, "", "", false); got != "tenant:"+tenantID {
		t.Fatalf("tenant compile scope = %q", got)
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
	if err := lockMediaAuthorityCompileFence(ctx, commodoredb.New(db), scopeKey); err == nil {
		t.Fatal("superseded compile generation was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
