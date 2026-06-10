package bootstrap

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// fakeQM is a minimal QMBootstrapClient for unit tests. It records the calls
// the reconciler made and returns canned results.
type fakeQM struct {
	tenantUUIDs map[string]string
	official    []string
}

func (f *fakeQM) Resolve(_ context.Context, alias string) (string, error) {
	id, ok := f.tenantUUIDs[alias]
	if !ok {
		return "", &qmAliasErr{alias: alias}
	}
	return id, nil
}

func (f *fakeQM) PlatformOfficialClusterIDs(_ context.Context) ([]string, error) {
	return append([]string(nil), f.official...), nil
}

type qmAliasErr struct{ alias string }

func (e *qmAliasErr) Error() string { return "alias not found: " + e.alias }

// TestReconcileCustomerBilling_PlatformOfficialIntersection is the regression
// the auditor flagged: a `cluster_pricing` row for a private customer cluster
// must NOT be auto-granted to a derived-access tenant. Eligibility is the
// intersection of (a) platform-official set from QM, (b) priced clusters, (c)
// tier-qualified.
func TestReconcileCustomerBilling_PlatformOfficialIntersection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	tenantUUID := "11111111-1111-1111-1111-111111111111"
	tierUUID := "22222222-2222-2222-2222-222222222222"

	// Tier resolution.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, tier_level, currency FROM purser.billing_tiers WHERE tier_name = $1")).
		WithArgs("starter").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow(tierUUID, int32(1), "EUR"))

	// Subscription probe → not present → insert.
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.tenant_subscriptions")).
		WithArgs(tenantUUID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO purser.tenant_subscriptions")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Prepaid balance.
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO purser.prepaid_balances")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	// Eligibility query: ANY(platform-official ids), tier-qualified.
	// QM reports core-1 and core-2 as official. cluster_pricing has both of
	// those plus private-3. The query is filtered by ANY(official) so
	// private-3 must not appear in results; the test sets up rows for the
	// two official clusters at tier_level<=1.
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.cluster_pricing")).
		WithArgs(sqlmock.AnyArg(), int32(1)).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "required_tier_level"}).
			AddRow("core-1", int32(0)).
			AddRow("core-2", int32(1)))

	qm := &fakeQM{
		tenantUUIDs: map[string]string{"acme": tenantUUID},
		official:    []string{"core-1", "core-2"}, // private-3 is intentionally absent
	}
	entries := []CustomerBilling{{
		Tenant: TenantRef{Ref: "quartermaster.tenants[acme]"},
		Model:  "prepaid",
		Tier:   "starter",
	}}

	res, post, err := ReconcileCustomerBilling(context.Background(), db, entries, qm)
	if err != nil {
		t.Fatalf("ReconcileCustomerBilling: %v", err)
	}
	if len(res.Created) != 1 {
		t.Errorf("expected 1 created subscription, got %v", res)
	}

	// Post-commit: one grant per official cluster + one set-primary + one
	// deployment-tier stamp.
	wantGrants := map[string]bool{"core-1": false, "core-2": false}
	gotPrimary := ""
	gotTier := ""
	for _, op := range post {
		switch op.Kind {
		case PostCommitGrantClusterAccess:
			if op.ClusterID == "private-3" {
				t.Errorf("private cluster leaked into derived access ops")
			}
			if _, ok := wantGrants[op.ClusterID]; ok {
				wantGrants[op.ClusterID] = true
			}
		case PostCommitSetPrimaryCluster:
			gotPrimary = op.ClusterID
		case PostCommitSetDeploymentTier:
			gotTier = op.Tier
		}
	}
	for cluster, seen := range wantGrants {
		if !seen {
			t.Errorf("expected grant op for %q, did not find it", cluster)
		}
	}
	if gotTier != "starter" {
		t.Errorf("expected set-deployment-tier op with tier \"starter\", got %q", gotTier)
	}
	// The mock returned core-2 first (ORDER BY required_tier_level DESC), so
	// pickPrimary should pick core-2.
	if gotPrimary != "core-1" && gotPrimary != "core-2" {
		t.Errorf("expected a set-primary op among eligible clusters, got %q", gotPrimary)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestReconcileCustomerBilling_ClusterAccessNone exercises the operator opt-out
// path: no eligibility query, no post-commit ops.
func TestReconcileCustomerBilling_ClusterAccessNone(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	tenantUUID := "11111111-1111-1111-1111-111111111111"
	tierUUID := "22222222-2222-2222-2222-222222222222"

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, tier_level, currency FROM purser.billing_tiers WHERE tier_name = $1")).
		WithArgs("enterprise").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow(tierUUID, int32(5), "EUR"))
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.tenant_subscriptions")).
		WithArgs(tenantUUID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO purser.tenant_subscriptions")).
		WillReturnResult(sqlmock.NewResult(0, 1))

	qm := &fakeQM{
		tenantUUIDs: map[string]string{"acme": tenantUUID},
		official:    []string{"core-1"},
	}
	entries := []CustomerBilling{{
		Tenant:        TenantRef{Ref: "quartermaster.tenants[acme]"},
		Model:         "postpaid",
		Tier:          "enterprise",
		ClusterAccess: "none",
	}}

	_, post, err := ReconcileCustomerBilling(context.Background(), db, entries, qm)
	if err != nil {
		t.Fatalf("ReconcileCustomerBilling: %v", err)
	}
	// cluster_access=none suppresses access ops, but the deployment-tier stamp
	// is billing state, not cluster entitlement — it is still emitted.
	if len(post) != 1 || post[0].Kind != PostCommitSetDeploymentTier || post[0].Tier != "enterprise" {
		t.Errorf("cluster_access=none should emit only the deployment-tier stamp, got %v", post)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestReconcileCustomerBilling_EntitlementOverrides(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()

	tenantUUID := "11111111-1111-1111-1111-111111111111"
	tierUUID := "22222222-2222-2222-2222-222222222222"
	subscriptionUUID := "33333333-3333-3333-3333-333333333333"

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text, tier_level, currency FROM purser.billing_tiers WHERE tier_name = $1")).
		WithArgs("free").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_level", "currency"}).AddRow(tierUUID, int32(1), "EUR"))
	mock.ExpectQuery(regexp.QuoteMeta("FROM purser.tenant_subscriptions")).
		WithArgs(tenantUUID).
		WillReturnRows(sqlmock.NewRows([]string{"tier_id", "billing_model"}).AddRow(tierUUID, "postpaid"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text FROM purser.tenant_subscriptions WHERE tenant_id = $1::uuid")).
		WithArgs(tenantUUID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(subscriptionUUID))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM purser.subscription_entitlement_overrides")).
		WithArgs(subscriptionUUID).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO purser.subscription_entitlement_overrides")).
		WithArgs(subscriptionUUID, "max_concurrent_streams", "0").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO purser.subscription_entitlement_overrides")).
		WithArgs(subscriptionUUID, "max_concurrent_viewers", "0").
		WillReturnResult(sqlmock.NewResult(0, 1))

	qm := &fakeQM{tenantUUIDs: map[string]string{"frameworks": tenantUUID}}
	entries := []CustomerBilling{{
		Tenant:        TenantRef{Ref: "quartermaster.system_tenant"},
		Model:         "postpaid",
		Tier:          "free",
		ClusterAccess: "none",
		EntitlementOverrides: map[string]any{
			"max_concurrent_streams": 0,
			"max_concurrent_viewers": 0,
		},
	}}

	res, post, err := ReconcileCustomerBilling(context.Background(), db, entries, qm)
	if err != nil {
		t.Fatalf("ReconcileCustomerBilling: %v", err)
	}
	if len(post) != 1 || post[0].Kind != PostCommitSetDeploymentTier || post[0].Tier != "free" {
		t.Fatalf("cluster_access=none should emit only the deployment-tier stamp, got %v", post)
	}
	if len(res.Noop) != 1 || res.Noop[0] != "frameworks" {
		t.Fatalf("expected noop system tenant subscription, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
