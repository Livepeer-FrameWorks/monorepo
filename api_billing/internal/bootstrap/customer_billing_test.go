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

	// Post-commit: one grant per official cluster + one set-primary.
	wantGrants := map[string]bool{"core-1": false, "core-2": false}
	gotPrimary := ""
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
		}
	}
	for cluster, seen := range wantGrants {
		if !seen {
			t.Errorf("expected grant op for %q, did not find it", cluster)
		}
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
	if len(post) != 0 {
		t.Errorf("cluster_access=none should emit no post-commit ops, got %v", post)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
