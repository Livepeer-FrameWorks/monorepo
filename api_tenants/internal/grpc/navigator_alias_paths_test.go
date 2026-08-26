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
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "new", "supporter", true, true))
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
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
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
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
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
		mock.ExpectQuery(`SELECT EXISTS`).
			WithArgs("tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
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

func TestSubscribeToClusterRejectsDirectWriterWithoutSideEffects(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	_, subscribeErr := server.SubscribeToCluster(ctx, &quartermasterpb.SubscribeToClusterRequest{ClusterId: "core-1"})
	if status.Code(subscribeErr) != codes.FailedPrecondition {
		t.Fatalf("SubscribeToCluster error = %v, want FailedPrecondition", subscribeErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestUnsubscribeFromClusterEnqueuesRemoveClusterThenTeardown(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")

	mock.ExpectBegin()
	// remove_cluster first (lower seq), then deactivate, then full teardown.
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "", "core-1", "cluster_unsubscribed", "remove_cluster").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rc-1"))
	mock.ExpectExec(`UPDATE quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE quartermaster\.tenants AS tenant`).
		WithArgs("tenant-1", "core-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "", "", "", "remove").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("rm-1"))
	mock.ExpectCommit()

	if _, err := server.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{ClusterId: "core-1"}); err != nil {
		t.Fatalf("UnsubscribeFromCluster: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
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
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).
			AddRow("Acme", "acme", "supporter", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectQuery(`SELECT jsonb_build_object`).
		WithArgs("tenant-1", "core-1").
		WillReturnRows(sqlmock.NewRows([]string{"state"}).AddRow(`{"access_level":"read","access_source":"operator_override","expires_at":null}`))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(eventClusterAccessGranted, "tenant-1", "operator-1", "cluster_access", "tenant-1:core-1", sqlmock.AnyArg()).
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
	t.Setenv("CLUSTER_ACCESS_MATERIALIZATION_SECRET", secret)
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
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
	t.Setenv("CLUSTER_ACCESS_MATERIALIZATION_SECRET", secret)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	req := materializationRequest(t, secret, "tenant-1", "byo-1", clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, "purser:tenant_private", time.Now().UTC().Truncate(time.Second))

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM quartermaster\.tenants`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COALESCE\(owner_tenant_id::text`).WithArgs("byo-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "cluster_class", "is_platform_official", "is_active"}).AddRow("tenant-1", "tenant_private", false, true))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quartermaster\.tenant_cluster_access`).WithArgs("tenant-1", "byo-1", "owner", "active").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT t\.name, t\.subdomain, t\.deployment_tier, t\.is_active.*FOR UPDATE`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"name", "subdomain", "deployment_tier", "is_active", "has_cluster"}).AddRow("Acme", "acme", "supporter", true, true))
	mock.ExpectQuery(`INSERT INTO quartermaster\.navigator_tenant_alias_outbox`).
		WithArgs("tenant-1", "acme", "", "", "ensure").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ensure-1"))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(eventClusterAccessMaterialized, "tenant-1", "", "cluster_access", "tenant-1:byo-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("audit-1"))
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
	t.Setenv("CLUSTER_ACCESS_MATERIALIZATION_SECRET", secret)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	const source = clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION
	authorizedAt := time.Now().UTC().Truncate(time.Second)
	proof, err := auth.MintClusterAccessMaterializationProof(secret, "tenant-1", "market-1", int32(source), "purser:marketplace-approval", "pending_approval", authorizedAt)
	if err != nil {
		t.Fatalf("mint materialization proof: %v", err)
	}
	req := &quartermasterpb.MaterializeClusterAccessRequest{
		TenantId: "tenant-1", ClusterId: "market-1", AccessSource: source,
		AuthorizationReference: "purser:marketplace-approval", SubscriptionStatus: "pending_approval",
		AuthorizedAt: timestamppb.New(authorizedAt), AuthorizationProof: proof,
	}

	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM quartermaster\.tenants`).WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT COALESCE\(owner_tenant_id::text`).WithArgs("market-1").
		WillReturnRows(sqlmock.NewRows([]string{"owner_tenant_id", "cluster_class", "is_platform_official", "is_active"}).AddRow("provider-1", "third_party_marketplace", false, true))
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO quartermaster\.tenant_cluster_access`).
		WithArgs("tenant-1", "market-1", "marketplace_subscription", "pending_approval").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`INSERT INTO quartermaster\.service_event_outbox`).
		WithArgs(eventClusterSubscriptionRequested, "tenant-1", "", "cluster_subscription", "tenant-1:market-1", sqlmock.AnyArg()).
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
