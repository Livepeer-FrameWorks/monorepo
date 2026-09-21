package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestCustomDomainTransitionDoesNotRemovePreviousDomainDuringEpochWindow(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster",
		}).AddRow("new.example.com", false, false, true, time.Unix(0, 0).UTC(), true))
	mock.ExpectRollback()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.enqueueCustomDomainTransition(context.Background(), tx, "tenant-1", "old.example.com", "new.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("epoch transition emitted a destructive outbox action: %v", err)
	}
}

func TestTenantAliasBackstopReconcilesClusterAuthorityAsASet(t *testing.T) {
	actions := tenantAliasBackstopActions(tenantAliasDesired{
		tenantID: "tenant-1", subdomain: "acme", want: true, clusterIDs: []string{"cluster-1", "cluster-2"},
	}, &dnspb.GetTenantAliasStatusResponse{
		Found: true, Subdomain: "acme", AuthorizedClusterIds: []string{"cluster-2", "cluster-removed"},
	})
	got := map[string]string{}
	for _, action := range actions {
		got[action.action] = action.clusterID
	}
	if got["ensure_cluster"] != "cluster-1" || got["remove_cluster"] != "cluster-removed" || len(actions) != 2 {
		t.Fatalf("cluster repair actions = %#v", actions)
	}
}

func TestTenantCustomDomainBackstopAction(t *testing.T) {
	cases := []struct {
		name    string
		desired tenantCustomDomainDesired
		current *dnspb.GetCustomDomainStatusResponse
		want    string
	}{
		{name: "missing ensure", desired: tenantCustomDomainDesired{want: true}, current: &dnspb.GetCustomDomainStatusResponse{}, want: "ensure"},
		{name: "teardown interrupted by entitlement", desired: tenantCustomDomainDesired{want: true}, current: &dnspb.GetCustomDomainStatusResponse{Found: true, Status: "tearing_down"}, want: "ensure"},
		{name: "undesired active domain", desired: tenantCustomDomainDesired{want: false}, current: &dnspb.GetCustomDomainStatusResponse{Found: true, Status: "cert_issued"}, want: "remove"},
		{name: "already removed", desired: tenantCustomDomainDesired{want: false}, current: &dnspb.GetCustomDomainStatusResponse{}, want: ""},
		{name: "already converged", desired: tenantCustomDomainDesired{want: true}, current: &dnspb.GetCustomDomainStatusResponse{Found: true, Status: "cert_issued"}, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenantCustomDomainBackstopAction(tc.desired, tc.current); got != tc.want {
				t.Fatalf("action = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTenantCustomDomainBackstopUndesiredLookupEnumeratesAllRows(t *testing.T) {
	if got := tenantCustomDomainLookupDomain(tenantCustomDomainDesired{domain: "configured.example", want: false}); got != "" {
		t.Fatalf("undesired lookup domain = %q, want tenant-wide deterministic enumeration", got)
	}
	if got := tenantCustomDomainLookupDomain(tenantCustomDomainDesired{domain: "configured.example", want: true}); got != "configured.example" {
		t.Fatalf("desired lookup domain = %q", got)
	}
}

func TestTenantCustomDomainBackstopUsesAppliedDomainAfterDesiredNameCleared(t *testing.T) {
	action, domain := tenantCustomDomainBackstopTarget(
		tenantCustomDomainDesired{tenantID: "tenant-1", want: false},
		&dnspb.GetCustomDomainStatusResponse{Found: true, Domain: "stale.example.com", Status: "cert_issued"},
	)
	if action != "remove" || domain != "stale.example.com" {
		t.Fatalf("target = (%q,%q), want remove stale.example.com", action, domain)
	}
}

// Rename must retire the OLD label before ensuring the NEW one: retire is
// enqueued first so it gets the lower BIGSERIAL seq and the worker dispatches
// it ahead of the ensure. sqlmock enforces ordered expectations, so this test
// fails if the order flips.
func TestEnqueueTenantAliasForSubdomainChangeRetiresBeforeEnsure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "old", "", "", "retire").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("retire-1"))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("Acme", "new", true, true, observedBillingEntitlementsAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "new", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasForSubdomainChange(ctx, tx, "tenant-1", "old", "new"); enqErr != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainChange: %v", enqErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("ordering/expectations: %v", mErr)
	}
}

func TestEnqueueTenantAliasForSubdomainChangeClearRemoves(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	// Clearing the subdomain → a single full teardown, no retire/ensure.
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "old", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasForSubdomainChange(ctx, tx, "tenant-1", "old", ""); enqErr != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainChange: %v", enqErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestEnqueueTenantAliasForSubdomainUpdateRemovesWhenIneligible(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(true, false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "old", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if enqErr := server.enqueueTenantAliasForSubdomainUpdate(ctx, tx, "tenant-1", "old", "new"); enqErr != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainUpdate: %v", enqErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestEnqueueTenantAliasForSubdomainUpdateDoesNothingWhileEntitlementsUnobserved(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(false, false))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := server.enqueueTenantAliasForSubdomainUpdate(ctx, tx, "tenant-1", "old", "new"); err != nil {
		t.Fatalf("enqueueTenantAliasForSubdomainUpdate: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unobserved entitlement emitted an outbox write: %v", err)
	}
}

func TestEnqueueTenantAliasForTierChangeDowngrade(t *testing.T) {
	t.Run("removes when no paid access remains", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer func() { _ = db.Close() }()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		ctx := context.Background()

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(true, false))
		mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
			WithArgs("tenant-1", "", "", "", "remove").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if enqErr := server.enqueueTenantAliasForTierChange(ctx, tx, "tenant-1", true); enqErr != nil {
			t.Fatalf("enqueueTenantAliasForTierChange: %v", enqErr)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	t.Run("keeps alias when paid access remains", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer func() { _ = db.Close() }()
		server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
		ctx := context.Background()

		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(true, true))
		// No teardown enqueued.

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if enqErr := server.enqueueTenantAliasForTierChange(ctx, tx, "tenant-1", true); enqErr != nil {
			t.Fatalf("enqueueTenantAliasForTierChange: %v", enqErr)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}

func TestEnqueueTenantAliasDesiredStateRemovesAfterClusterLoss(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active,[\s\S]*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("Acme", "acme", true, true, observedBillingEntitlementsAt, false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("remove-1"))

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := server.enqueueTenantAliasDesiredStateTx(ctx, tx, "tenant-1"); err != nil {
		t.Fatalf("enqueueTenantAliasDesiredStateTx: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEpochBillingStateCannotEnqueueDNSRemoval(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.Background()
	epoch := time.Unix(0, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active,[\s\S]*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("Acme", "acme", false, true, epoch, false))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.enqueueTenantAliasDesiredStateTx(ctx, tx, "tenant-1"); err != nil {
		t.Fatalf("alias desired state: %v", err)
	}

	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("stream.acme.example", false, false, true, epoch, false))
	if err := server.enqueueCustomDomainDesiredStateTx(ctx, tx, "tenant-1"); err != nil {
		t.Fatalf("custom-domain desired state: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("epoch state emitted an unexpected outbox write: %v", err)
	}
}

func TestSubscribeToClusterRejectsDirectWriterWithoutSideEffects(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	_, subscribeErr := server.SubscribeToCluster(ctx, &quartermasterpb.SubscribeToClusterRequest{ClusterId: "core-1"})
	if status.Code(subscribeErr) != codes.FailedPrecondition {
		t.Fatalf("SubscribeToCluster error = %v, want FailedPrecondition", subscribeErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestDeactivateClusterAccessDoesNotRemoveDNSMembershipBeforeBillingObservation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).WithArgs("tenant-1", "core-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.tenants AS tenant`).WithArgs("tenant-1", "core-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(false, false))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("legacy.example", true, true, true, time.Unix(0, 0).UTC(), false))
	mock.ExpectCommit()

	if _, err := server.DeactivateClusterAccess(serviceCtx(), &quartermasterpb.DeactivateClusterAccessRequest{TenantId: "tenant-1", ClusterId: "core-1"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("epoch deactivation emitted an unexpected DNS removal: %v", err)
	}
}

func TestRevokeMaterializedClusterAccessDoesNotRemoveDNSMembershipBeforeBillingObservation(t *testing.T) {
	const secret = "materialization-test-secret"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetClusterAccessMaterializationSecret(secret)
	authorizedAt := time.Now().UTC().Truncate(time.Second)
	proof, err := auth.MintClusterAccessRevocationProof(
		secret, "tenant-1", "core-1",
		int32(clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION),
		"stripe:sub-1", authorizedAt,
	)
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1", "marketplace_subscription").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.tenants AS tenant`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(false, false))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("legacy.example", true, true, true, time.Unix(0, 0).UTC(), false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventClusterAccessRevoked, "tenant-1", "tenant", "", "cluster_access", "tenant-1:core-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("audit-1"))
	mock.ExpectCommit()

	_, err = server.RevokeMaterializedClusterAccess(serviceCtx(), &quartermasterpb.RevokeMaterializedClusterAccessRequest{
		TenantId: "tenant-1", ClusterId: "core-1",
		AccessSource:           clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
		AuthorizationReference: "stripe:sub-1",
		AuthorizedAt:           timestamppb.New(authorizedAt),
		AuthorizationProof:     proof,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("epoch revocation emitted an unexpected DNS removal: %v", err)
	}
}

// unsubscribeTenant is a tenant UUID: unsubscribing records a tenant-scoped
// domain event, whose envelope requires one.
const unsubscribeTenant = "33333333-3333-4333-8333-333333333333"

func TestUnsubscribeFromClusterEnqueuesRemoveClusterThenTeardown(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetEventTokenHasher(testEventTokenHasher(t))
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, unsubscribeTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).
		WithArgs(unsubscribeTenant, "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.tenants AS tenant`).
		WithArgs(unsubscribeTenant, "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).
		WithArgs(unsubscribeTenant).
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(true, false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs(unsubscribeTenant, "", "core-1", "cluster_unsubscribed", "remove_cluster").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rc-1"))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs(unsubscribeTenant, "", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rm-1"))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).
		WithArgs(unsubscribeTenant).
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow(nil, false, false, true, observedBillingEntitlementsAt, false))
	expectServiceEventOutbox(mock, eventTenantClusterUnassigned, unsubscribeTenant)
	mock.ExpectCommit()

	if _, err := server.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatalf("UnsubscribeFromCluster: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestUnsubscribeFromClusterDoesNotRemoveDNSMembershipBeforeBillingObservation(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetEventTokenHasher(testEventTokenHasher(t))
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, unsubscribeTenant)
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).WithArgs(unsubscribeTenant, "core-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.tenants AS tenant`).WithArgs(unsubscribeTenant, "core-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.billing_entitlements_observed_at`).WithArgs(unsubscribeTenant).
		WillReturnRows(sqlmock.NewRows([]string{"entitlements_observed", "has_paid_cluster_access"}).AddRow(false, false))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).WithArgs(unsubscribeTenant).
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("legacy.example", true, true, true, time.Unix(0, 0).UTC(), false))
	expectServiceEventOutbox(mock, eventTenantClusterUnassigned, unsubscribeTenant)
	mock.ExpectCommit()

	if _, err := server.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("epoch unsubscribe emitted an unexpected DNS removal: %v", err)
	}
}

func TestUnsubscribeFromClusterDoesNotEnqueueWhenAccessWasNotActive(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	if _, err := server.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatalf("UnsubscribeFromCluster: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unchanged unsubscribe emitted cleanup: %v", err)
	}
}

func TestGrantClusterAccessEnqueuesEnsure(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM quartermaster\.tenants`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT is_platform_official, is_active`).
		WithArgs("core-1").
		WillReturnRows(sqlmock.NewRows([]string{"is_platform_official", "is_active"}).AddRow(false, true))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WithArgs("tenant-1", "core-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`INSERT INTO quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1", "read", "{}", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow("Acme", "acme", true, true, observedBillingEntitlementsAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "", "core-1", "cluster_access_active", "ensure_cluster").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cluster-ensure-1"))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).
			AddRow(nil, false, false, true, observedBillingEntitlementsAt, true))
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WithArgs("tenant-1", "core-1").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(`{"access_level":"read","access_source":"operator_override","expires_at":null}`))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventClusterAccessGranted, "tenant-1", "tenant", "operator-1", "cluster_access", "tenant-1:core-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("audit-1"))
	mock.ExpectCommit()

	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyPlatformOperator, true)
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "operator-1")
	if _, err := server.GrantClusterAccess(ctx, &quartermasterpb.GrantClusterAccessRequest{
		TenantId: "tenant-1", ClusterId: "core-1",
	}); err != nil {
		t.Fatalf("GrantClusterAccess: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestGrantClusterAccessRejectsNonOperatorBeforeDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	for _, ctx := range []context.Context{
		context.Background(),
		context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"),
		context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyRole, "provider"),
	} {
		_, grantErr := server.GrantClusterAccess(ctx, &quartermasterpb.GrantClusterAccessRequest{TenantId: "tenant-1", ClusterId: "cluster-1"})
		if status.Code(grantErr) != codes.PermissionDenied {
			t.Fatalf("GrantClusterAccess error = %v, want PermissionDenied", grantErr)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("denied grant touched database: %v", err)
	}
}

func materializationRequest(t *testing.T, secret, tenantID, clusterID string, source clusterpeerpb.TenantClusterAccessSource, reference string, authorizedAt time.Time) *quartermasterpb.MaterializeClusterAccessRequest {
	t.Helper()
	proof, err := auth.MintClusterAccessMaterializationProof(secret, tenantID, clusterID, int32(source), reference, "active", authorizedAt)
	if err != nil {
		t.Fatalf("mint materialization proof: %v", err)
	}
	return &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: tenantID, ClusterId: clusterID, AccessSource: source,
		AuthorizationReference: reference, AuthorizedAt: timestamppb.New(authorizedAt), AuthorizationProof: proof,
	}
}

func TestMaterializeClusterAccessRejectsInvalidAuthorityBeforeDatabase(t *testing.T) {
	const secret = "materialization-test-secret"
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetClusterAccessMaterializationSecret(secret)
	now := time.Now().UTC().Truncate(time.Second)
	valid := materializationRequest(t, secret, "tenant-1", "market-1", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION, "stripe:sub-1", now)

	tests := []struct {
		name string
		ctx  context.Context
		req  *quartermasterpb.MaterializeClusterAccessRequest
	}{
		{name: "missing service auth", ctx: context.Background(), req: valid},
		{name: "missing proof", ctx: serviceCtx(), req: &quartermasterpb.MaterializeClusterAccessRequest{TenantId: "tenant-1", ClusterId: "market-1", AccessSource: valid.GetAccessSource(), AuthorizationReference: "stripe:sub-1"}},
		{name: "tampered tenant", ctx: serviceCtx(), req: func() *quartermasterpb.MaterializeClusterAccessRequest {
			clone := proto.Clone(valid).(*quartermasterpb.MaterializeClusterAccessRequest)
			clone.TenantId = "tenant-2"
			return clone
		}()},
		{name: "tampered status", ctx: serviceCtx(), req: func() *quartermasterpb.MaterializeClusterAccessRequest {
			clone := proto.Clone(valid).(*quartermasterpb.MaterializeClusterAccessRequest)
			clone.SubscriptionStatus = "pending_approval"
			return clone
		}()},
		{name: "expired proof", ctx: serviceCtx(), req: materializationRequest(t, secret, "tenant-1", "market-1", valid.GetAccessSource(), "stripe:sub-1", now.Add(-auth.ClusterAccessProofMaxAge-time.Second))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, gotErr := server.MaterializeClusterAccess(test.ctx, test.req)
			if status.Code(gotErr) != codes.PermissionDenied {
				t.Fatalf("error = %v, want PermissionDenied", gotErr)
			}
		})
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("rejected materialization touched database: %v", err)
	}
}

func TestMaterializeClusterAccessOwnerGrantIsAuditedAtomically(t *testing.T) {
	const secret = "materialization-test-secret"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetClusterAccessMaterializationSecret(secret)
	const tenantID = "44444444-4444-4444-8444-444444444444"
	req := materializationRequest(t, secret, tenantID, "byo-1", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, "purser:tenant_private", time.Now().UTC().Truncate(time.Second))
	req.Actor = &commonpb.RequestActor{AuthType: "api_token", UserId: "user-7", TokenHash: 4242}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM quartermaster\.tenants`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COALESCE\(owner_tenant_id::text`).WithArgs("byo-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "cluster_class", "is_platform_official", "is_active"}).AddRow(tenantID, "tenant_private", false, true))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\('tenant_cluster_access:'`).
		WithArgs(tenantID, "byo-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM quartermaster\.tenant_cluster_access\s+WHERE tenant_id = \$1::uuid\s+AND cluster_id = \$2::text\s+FOR UPDATE`).
		WithArgs(tenantID, "byo-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO quartermaster\.tenant_cluster_access`).WithArgs(tenantID, "byo-1", "owner", "active").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("access-1"))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.custom_subdomain_enabled, t\.is_active.*FOR UPDATE`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "custom_subdomain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).AddRow("Acme", "acme", true, true, observedBillingEntitlementsAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs(tenantID, "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs(tenantID, "", "byo-1", "cluster_access_active", "ensure_cluster").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("cluster-ensure-1"))
	mock.ExpectQuery(`SELECT t\.custom_domain, t\.custom_subdomain_enabled, t\.custom_domain_enabled, t\.is_active`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"custom_domain", "custom_subdomain_enabled", "custom_domain_enabled", "is_active", "billing_entitlements_observed_at", "has_cluster"}).AddRow(nil, false, false, true, observedBillingEntitlementsAt, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventClusterAccessMaterialized, tenantID, "tenant", "", "cluster_access", tenantID+":byo-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("audit-1"))
	// The owner grant made access active: tenant.cluster_assigned, attributed
	// to the principal Purser named, commits with it.
	mock.ExpectQuery(`FROM quartermaster\.tenant_cluster_access\s+WHERE tenant_id = \$1::uuid\s+AND cluster_id = \$2::text\s+FOR UPDATE`).
		WithArgs(tenantID, "byo-1").WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO quartermaster\.domain_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "tenant.cluster_assigned", "quartermaster", "tenants", tenantID,
			sqlmock.AnyArg(), "tenant", tenantID, "api_token", "user-7", "4242", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventTenantClusterAssigned, tenantID, "tenant", "user-7", "cluster", "byo-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("assigned-1"))
	mock.ExpectCommit()

	if _, err := server.MaterializeClusterAccess(serviceCtx(), req); err != nil {
		t.Fatalf("MaterializeClusterAccess: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMaterializeClusterAccessMarketplaceRequestRemainsPending(t *testing.T) {
	const secret = "materialization-test-secret"
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetClusterAccessMaterializationSecret(secret)
	server.SetEventTokenHasher(testEventTokenHasher(t))
	const (
		source   = clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION
		tenantID = "11111111-1111-4111-8111-111111111111"
		accessID = "22222222-2222-4222-8222-222222222222"
	)
	authorizedAt := time.Now().UTC().Truncate(time.Second)
	proof, err := auth.MintClusterAccessMaterializationProof(secret, tenantID, "market-1", int32(source), "purser:marketplace-approval", "pending_approval", authorizedAt)
	if err != nil {
		t.Fatalf("mint materialization proof: %v", err)
	}
	req := &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: tenantID, ClusterId: "market-1", AccessSource: source,
		AuthorizationReference: "purser:marketplace-approval", SubscriptionStatus: "pending_approval",
		AuthorizedAt: timestamppb.New(authorizedAt), AuthorizationProof: proof,
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM quartermaster\.tenants`).WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COALESCE\(owner_tenant_id::text`).WithArgs("market-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "cluster_class", "is_platform_official", "is_active"}).AddRow("provider-1", "third_party_marketplace", false, true))
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtextextended\('tenant_cluster_access:'`).
		WithArgs(tenantID, "market-1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM quartermaster\.tenant_cluster_access\s+WHERE tenant_id = \$1::uuid\s+AND cluster_id = \$2::text\s+FOR UPDATE`).
		WithArgs(tenantID, "market-1").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO quartermaster\.tenant_cluster_access`).
		WithArgs(tenantID, "market-1", "marketplace_subscription", "pending_approval").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accessID))
	// The domain event is keyed by the access row, the subscription later
	// approvals and rejections name.
	mock.ExpectExec(`INSERT INTO quartermaster\.domain_event_outbox`).
		WithArgs(sqlmock.AnyArg(), "cluster.subscription_requested", "quartermaster", "cluster_subscriptions", accessID,
			sqlmock.AnyArg(), "tenant", tenantID, "service", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(sqlmock.AnyArg(), eventClusterSubscriptionRequested, tenantID, "tenant", "", "cluster_subscription", tenantID+":market-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("audit-1"))
	mock.ExpectCommit()

	if _, err := server.MaterializeClusterAccess(serviceCtx(), req); err != nil {
		t.Fatalf("MaterializeClusterAccess: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// recordAliasOutboxFailure increments the stored counter instead of writing the
// carried claim value back, so retries and alert thresholds advance.
func TestRecordAliasOutboxFailureIncrementsAttempts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET attempts = attempts \+ 1,.*next_retry_at = NOW\(\) \+ \$3::interval`).
		WithArgs("outbox-1", "boom", "16000 milliseconds").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := server.recordAliasOutboxFailure(context.Background(), "outbox-1", 3, errors.New("boom"), 16*time.Second); err != nil {
		t.Fatalf("recordAliasOutboxFailure: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}
