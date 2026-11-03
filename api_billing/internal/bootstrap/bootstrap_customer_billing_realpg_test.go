//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	pursergrpc "frameworks/api_billing/internal/grpc"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestBootstrapCustomerBillingRepository_RealPG(t *testing.T) { //nolint:funlen // One transaction proves the complete customer bootstrap boundary.
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	tierID := uuid.NewString()
	tenantID := uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
			id, tier_name, display_name, tier_level, currency
		) VALUES ($1, 'bootstrap-contract', 'Bootstrap contract', 2, 'EUR')
	`, tierID); writeErr != nil {
		t.Fatal(writeErr)
	}
	for _, row := range []struct {
		id       string
		required int
	}{
		{id: "official-a", required: 1},
		// A priced, official control cluster ranks above official-a but
		// Quartermaster refuses it as a primary.
		{id: "official-control", required: 2},
		{id: "private-b", required: 1},
		{id: "official-too-high", required: 3},
	} {
		if _, writeErr := db.ExecContext(ctx, `
			INSERT INTO purser.cluster_pricing (
				id, cluster_id, pricing_model, required_tier_level, allow_free_tier
			) VALUES ($1, $2, 'tier_inherit', $3, FALSE)
		`, uuid.NewString(), row.id, row.required); writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	qm := &fakeQM{
		tenantUUIDs:     map[string]string{"acme": tenantID},
		official:        []string{"official-a", "official-too-high"},
		officialControl: []string{"official-control"},
	}
	entry := CustomerBilling{
		Tenant: TenantRef{Ref: "quartermaster.tenants[acme]"},
		Model:  "prepaid", Tier: "bootstrap-contract",
		EntitlementOverrides: map[string]any{
			"max_concurrent_streams":   3,
			"recording_retention_days": 30,
		},
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, post, err := ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if len(result.Created) != 1 {
		t.Fatalf("first result = %+v", result)
	}
	assertBootstrapPostCommitOps(t, post)
	assertBootstrapCreationPeriods(t, db, tenantID)

	var subscriptionID, storedTierID, model string
	if readErr := db.QueryRowContext(ctx, `
		SELECT id::text, tier_id::text, billing_model
		FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&subscriptionID, &storedTierID, &model); readErr != nil {
		t.Fatal(readErr)
	}
	if storedTierID != tierID || model != "prepaid" {
		t.Fatalf("subscription tier/model = %s/%s", storedTierID, model)
	}
	var overrideCount int
	if readErr := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.subscription_entitlement_overrides
		WHERE subscription_id = $1
	`, subscriptionID).Scan(&overrideCount); readErr != nil {
		t.Fatal(readErr)
	}
	if overrideCount != 2 {
		t.Fatalf("override count = %d", overrideCount)
	}
	if _, writeErr := db.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = 700
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID); writeErr != nil {
		t.Fatal(writeErr)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, post, err = ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm, nil)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if commitErr2 := tx.Commit(); commitErr2 != nil {
		t.Fatal(commitErr2)
	}
	if len(result.Noop) != 1 {
		t.Fatalf("second result = %+v", result)
	}
	assertBootstrapPostCommitOps(t, post)
	var balance int64
	var balanceRows int
	if readErr := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(balance_cents)
		FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balanceRows, &balance); readErr != nil {
		t.Fatal(readErr)
	}
	if balanceRows != 1 || balance != 700 {
		t.Fatalf("idempotent balance rows/value = %d/%d", balanceRows, balance)
	}
}

func assertBootstrapPostCommitOps(t *testing.T, ops []PostCommitOp) {
	t.Helper()
	var grant, primary, tier bool
	for _, op := range ops {
		switch op.Kind {
		case PostCommitGrantClusterAccess:
			grant = grant || op.ClusterID == "official-a"
			if op.ClusterID == "private-b" || op.ClusterID == "official-too-high" {
				t.Fatalf("ineligible cluster granted: %+v", op)
			}
		case PostCommitSetPrimaryCluster:
			if op.ClusterID != "official-a" {
				t.Fatalf("primary = %+v, want the edge official-a", op)
			}
			primary = true
		case PostCommitSetDeploymentTier:
			tier = op.Tier == "bootstrap-contract"
		}
	}
	if !grant || !primary || !tier {
		t.Fatalf("post-commit operations missing grant/primary/tier: %+v", ops)
	}
}

func TestBootstrapDeclaredGrantIsIdempotent_RealPG(t *testing.T) {
	var db *sql.DB
	if strings.HasSuffix(t.Name(), "_RealYugabyte") {
		var ok bool
		db, ok = dockerpg.OpenSharedYugabyteBaseline(t, "bootstrap_operator_grants", "purser")
		if !ok {
			t.Skip("requires the shared Yugabyte fixture")
		}
	} else {
		db = startBootstrapPricingRealPG(t)
	}
	ctx := context.Background()
	assertBootstrapPreservesUncollectedUpgrade(t, db)
	testBootstrapPreservesCLIGrant(t, db)
	tenantID, tierID := uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'grant-contract','Grant contract',4,100,'EUR',false)`, tierID); writeErr != nil {
		t.Fatal(writeErr)
	}
	zero := "0"
	entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[trial]"}, Model: "postpaid", Tier: "grant-contract", ClusterAccess: "none", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Reason: "operator test account"}}
	qm := &fakeQM{tenantUUIDs: map[string]string{"trial": tenantID}}
	unprepared, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ReconcileCustomerBilling(ctx, unprepared, []CustomerBilling{entry}, qm, nil)
	if rollbackErr := unprepared.Rollback(); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if err == nil {
		t.Fatal("fresh grant write accepted without preparation")
	}
	for attempt := range 2 {
		prepared, err := PrepareCustomerGrants(ctx, db, []CustomerBilling{entry}, qm, logging.NewLogger(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, _, err := ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm, prepared)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if attempt == 1 && len(result.Noop) != 1 {
			t.Fatalf("reapply=%+v, want noop", result)
		}
	}
	entry.OperatorGrant.Until = "2020-01-01"
	if changed, actionErr := customerGrantNeedsWrite(ctx, db, tenantID, entry); actionErr != nil || changed {
		t.Fatalf("expired declaration must be inert, changed=%v err=%v", changed, actionErr)
	}
	var history, events int
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenantID).Scan(&history); readErr != nil {
		t.Fatal(readErr)
	}
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.domain_event_outbox WHERE tenant_id=$1 AND event_type='billing.subscription_updated'`, tenantID).Scan(&events); readErr != nil {
		t.Fatal(readErr)
	}
	if history != 0 || events != 1 {
		t.Fatalf("idempotent grant emitted history=%d events=%d, want 0/1", history, events)
	}
	cancelled := uuid.NewString()
	if _, seedErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model) VALUES($1,$2,'cancelled','postpaid')`, cancelled, tierID); seedErr != nil {
		t.Fatal(seedErr)
	}
	if _, seedErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,waive_usage,reason) SELECT id,true,'cancelled declaration' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, cancelled); seedErr != nil {
		t.Fatal(seedErr)
	}
	liveEntry := entry
	liveEntry.OperatorGrant = nil
	cancelledEntry := entry
	cancelledEntry.OperatorGrant = nil
	cancelledEntry.Tenant.Ref = "quartermaster.tenants[cancelled]"
	entries := []CustomerBilling{cancelledEntry, liveEntry}
	qm.tenantUUIDs["cancelled"] = cancelled
	prepared, prepareErr := PrepareCustomerGrants(ctx, db, entries, qm, logging.NewLogger(), nil, nil)
	if prepareErr != nil || prepared["cancelled"].deferred == nil {
		t.Fatalf("cancelled declaration aborted preparation: %v,%+v", prepareErr, prepared)
	}
	tx, beginErr := db.BeginTx(ctx, nil)
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	result, _, reconcileErr := ReconcileCustomerBilling(ctx, tx, entries, qm, prepared)
	if reconcileErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			t.Fatal(rollbackErr)
		}
		t.Fatal(reconcileErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if len(result.Deferred) != 1 || len(result.Noop) != 1 {
		t.Fatalf("cancelled declaration blocked healthy tenant: %+v", result)
	}
}

func TestBootstrapDeclaredGrantIsIdempotent_RealYugabyte(t *testing.T) {
	TestBootstrapDeclaredGrantIsIdempotent_RealPG(t)
}

func TestBootstrapGrantWithoutAssignedTierPreservesHistory_RealPG(t *testing.T) {
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	tenantID, oldTier, newTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'legacy-grant','Legacy',4,100,'EUR',false),($2,'declared-grant','Declared',4,200,'EUR',false)`, []any{oldTier, newTier}},
		{`INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'active','postpaid',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days','EUR')`, []any{tenantID, oldTier}},
		{`INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,granted_at) SELECT id,0,true,'provider','operator test',NOW()-INTERVAL '1 day' FROM purser.tenant_subscriptions WHERE tenant_id=$1`, []any{tenantID}},
	} {
		if _, writeErr := db.ExecContext(ctx, seed.sql, seed.args...); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	zero := "0"
	entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[trial]"}, Model: "postpaid", Tier: "declared-grant", ClusterAccess: "none", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Reason: "operator test"}}
	qm := &fakeQM{tenantUUIDs: map[string]string{"trial": tenantID}}
	prepared, err := PrepareCustomerGrants(ctx, db, []CustomerBilling{entry}, qm, logging.NewLogger(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm, prepared); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var tier string
	if err = db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, tenantID).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != oldTier {
		t.Fatalf("grant without assigned_tier_id history tier=%s, want %s", tier, oldTier)
	}
}

func TestBootstrapPendingExpiryIsPerTenantAndLiveDeclarationReplacesFallback_RealPG(t *testing.T) {
	testAlreadySweptBootstrapModelMismatch(t)
	testExpiredBootstrapModelSwitchesDeferred(t)
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	active, fallback, retired := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_active,is_default_prepaid) VALUES($1,'trial','Trial',4,250,'EUR',false,true,false),($2,'payg','PAYG',0,0,'EUR',false,true,true),($3,'retired','Retired',0,0,'EUR',false,false,false)`, active, fallback, retired); writeErr != nil {
		t.Fatal(writeErr)
	}
	ids := map[string]string{"expired": uuid.NewString(), "stuck": uuid.NewString(), "live": uuid.NewString(), "raced": uuid.NewString(), "phasechanged": uuid.NewString()}
	zero := "0"
	var entries []CustomerBilling
	for _, alias := range []string{"expired", "stuck", "live", "raced", "phasechanged"} {
		id := ids[alias]
		fb := fallback
		if alias == "stuck" {
			fb = retired
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'active','postpaid',NOW()-INTERVAL '3 days',NOW()+INTERVAL '27 days','EUR')`, id, active); writeErr != nil {
			t.Fatal(writeErr)
		}
		until := time.Now().Add(-time.Hour)
		if alias == "live" || alias == "raced" || alias == "phasechanged" {
			until = time.Now().Add(time.Hour)
		}
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,granted_at,expires_at,assigned_tier_id,fallback_tier_id) SELECT id,0,true,'provider','CLI trial',NOW()-INTERVAL '3 days',$2,tier_id,$3 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, id, until, fb); writeErr != nil {
			t.Fatal(writeErr)
		}
		base := &zero
		if alias == "phasechanged" {
			nonzero := "18"
			base = &nonzero
		}
		entries = append(entries, CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[" + alias + "]"}, Model: "postpaid", Tier: "trial", ClusterAccess: "none", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: base, WaiveUsage: true, Reason: "declared arrangement"}})
	}
	entries = append(entries, CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[missing]"}, Model: "postpaid", Tier: "trial", ClusterAccess: "none", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Reason: "unresolved"}})
	qm := &fakeQM{tenantUUIDs: ids}
	preview, err := PrepareCustomerGrants(ctx, db, entries, qm, logging.NewLogger(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if preview["expired"] == nil || preview["expired"].deferred == nil || preview["stuck"] == nil || preview["stuck"].deferred == nil {
		t.Fatal("dry-run did not report pending expiries")
	}
	var history int
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.subscription_operator_grant_history WHERE tenant_id=$1`, ids["expired"]).Scan(&history); readErr != nil {
		t.Fatal(readErr)
	}
	if history != 0 {
		t.Fatal("dry-run ended an expiry")
	}
	expiry := pursergrpc.NewBootstrapGrantExpiry(db, logging.NewLogger(), nil, nil, nil)
	prepared, err := PrepareCustomerGrants(ctx, db, entries, qm, logging.NewLogger(), nil, expiry)
	if err != nil {
		t.Fatal(err)
	}
	if prepared["stuck"] == nil || prepared["stuck"].deferred == nil {
		t.Fatal("stuck expiry was not reported per tenant")
	}
	// A concurrent CLI grant change after preparation defers just this tenant.
	if _, writeErr := db.ExecContext(ctx, `UPDATE purser.subscription_operator_grants SET reason='CLI after preparation',granted_at=granted_at+INTERVAL '1 microsecond' WHERE subscription_id=(SELECT id FROM purser.tenant_subscriptions WHERE tenant_id=$1)`, ids["raced"]); writeErr != nil {
		t.Fatal(writeErr)
	}
	if prepared["phasechanged"].close == nil {
		t.Fatal("closing-document regression was not prepared")
	}
	if _, writeErr := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_period_start=billing_period_start+INTERVAL '1 second' WHERE tenant_id=$1`, ids["phasechanged"]); writeErr != nil {
		t.Fatal(writeErr)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	result, ops, err := ReconcileCustomerBilling(ctx, tx, entries, qm, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deferred) != 5 {
		t.Fatalf("deferred report=%v, want expired model switch, stuck, raced, phasechanged and missing", result.Deferred)
	}
	invalidations := 0
	for _, op := range ops {
		if op.Kind == PostCommitInvalidateTenantCache {
			invalidations++
		}
	}
	if invalidations != 1 {
		t.Fatalf("bootstrap grant cache invalidations=%d, want 1", invalidations)
	}
	if commitErr3 := tx.Commit(); commitErr3 != nil {
		t.Fatal(commitErr3)
	}
	var rolledBackDocuments int
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1`, ids["phasechanged"]).Scan(&rolledBackDocuments); readErr != nil {
		t.Fatal(readErr)
	}
	if rolledBackDocuments != 0 || prepared["phasechanged"].applied {
		t.Fatal("changed closing document leaked effects from its savepoint")
	}
	var restoredTier, restoredModel string
	if readErr := db.QueryRowContext(ctx, `SELECT tier_id::text,billing_model FROM purser.tenant_subscriptions WHERE tenant_id=$1`, ids["expired"]).Scan(&restoredTier, &restoredModel); readErr != nil {
		t.Fatal(readErr)
	}
	if restoredTier != fallback || restoredModel != "prepaid" {
		t.Fatalf("deferred model switch changed expiry fallback tier/model=%s/%s", restoredTier, restoredModel)
	}
	var invoices, statements int
	var total string
	if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE document_kind='invoice'),COUNT(*) FILTER (WHERE document_kind='prepaid_statement'),COALESCE(SUM(amount),0)::text FROM purser.billing_invoices WHERE tenant_id=$1`, ids["expired"]).Scan(&invoices, &statements, &total); readErr != nil {
		t.Fatal(readErr)
	}
	if invoices != 1 || statements != 0 || total != "0.00" {
		t.Fatalf("expiry round trip invoices/statements/amount=%d/%d/%s", invoices, statements, total)
	}
	for _, alias := range []string{"live"} {
		grant, grantErr := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, ids[alias])
		if grantErr != nil {
			t.Fatal(grantErr)
		}
		if grant.Reason != "declared arrangement" || grant.FallbackTierID != "" || !grant.Active {
			t.Fatalf("%s replacement=%+v", alias, grant)
		}
	}
	if _, getSubscriptionOperatorGrantErr := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, ids["expired"]); !errors.Is(getSubscriptionOperatorGrantErr, sql.ErrNoRows) {
		t.Fatalf("deferred model restore recreated grant: %v", getSubscriptionOperatorGrantErr)
	}
	raced, err := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, ids["raced"])
	if err != nil || raced.Reason != "CLI after preparation" {
		t.Fatalf("raced tenant declaration overwrote concurrent grant: %v err=%v", raced, err)
	}
	grant, err := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, ids["stuck"])
	if err != nil {
		t.Fatal(err)
	}
	if grant.Reason != "CLI trial" || grant.FallbackTierID != retired {
		t.Fatalf("stuck arrangement modified: %+v", grant)
	}
}

// Both billing-model directions must use the operator transition before a
// declaration can replace the grant that expired into its fallback.
func testExpiredBootstrapModelSwitchesDeferred(t *testing.T) {
	for _, fromModel := range []string{"postpaid", "prepaid"} {
		t.Run("expired restore to "+fromModel, func(t *testing.T) {
			db := startBootstrapPricingRealPG(t)
			ctx := context.Background()
			tenant, tier, fallback := uuid.NewString(), uuid.NewString(), uuid.NewString()
			level, fallbackLevel := 4, 0
			fallbackModel := "prepaid"
			if fromModel == "prepaid" {
				level, fallbackLevel = 0, 4
				fallbackModel = "postpaid"
			}
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled,is_default_prepaid) VALUES($1,'declared','Declared',$3,100,'EUR',false,$3=0),($2,'fallback','Fallback',$4,50,'EUR',false,$4=0)`, tier, fallback, level, fallbackLevel); writeErr != nil {
				t.Fatal(writeErr)
			}
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,billing_period_start,billing_period_end,presentment_currency) VALUES($1,$2,'suspended',$3,NOW()-INTERVAL '3 days',NOW()+INTERVAL '27 days','EUR')`, tenant, tier, fromModel); writeErr != nil {
				t.Fatal(writeErr)
			}
			if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,collection,reason,granted_at,expires_at,assigned_tier_id,fallback_tier_id) SELECT id,0,true,'provider','expired',billing_period_start,NOW()-INTERVAL '1 hour',tier_id,$2 FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant, fallback); writeErr != nil {
				t.Fatal(writeErr)
			}
			zero := "0"
			entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[tenant]"}, Tier: "declared", Model: fromModel, ClusterAccess: "none", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Reason: "renewal"}}
			prepared, err := PrepareCustomerGrants(ctx, db, []CustomerBilling{entry}, &fakeQM{tenantUUIDs: map[string]string{"tenant": tenant}}, logging.NewLogger(), nil, pursergrpc.NewBootstrapGrantExpiry(db, logging.NewLogger(), nil, nil, nil))
			if err != nil {
				t.Fatal(err)
			}
			if prepared["tenant"] == nil || prepared["tenant"].deferred == nil || !strings.Contains(prepared["tenant"].deferred.Error(), "set-tier") {
				t.Fatalf("model restoration not deferred with recovery: %+v", prepared["tenant"])
			}
			// A later apply sees no grant: the sweep has already restored the fallback.
			prepared, err = PrepareCustomerGrants(ctx, db, []CustomerBilling{entry}, &fakeQM{tenantUUIDs: map[string]string{"tenant": tenant}}, logging.NewLogger(), nil, nil)
			if err != nil || prepared["tenant"] == nil || prepared["tenant"].deferred == nil {
				t.Fatalf("already-swept model mismatch was not deferred: prepared=%+v err=%v", prepared, err)
			}
			entry.OperatorGrant = nil
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, _, err := ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, &fakeQM{tenantUUIDs: map[string]string{"tenant": tenant}}, nil)
			if err != nil || len(result.Deferred) != 1 {
				_ = tx.Rollback()
				t.Fatalf("model mismatch aborted reconciliation: %+v err=%v", result, err)
			}
			if commitErr4 := tx.Commit(); commitErr4 != nil {
				t.Fatal(commitErr4)
			}
			var model, statusValue string
			if readErr := db.QueryRowContext(ctx, `SELECT billing_model,status FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&model, &statusValue); readErr != nil {
				t.Fatal(readErr)
			}
			if model != fallbackModel {
				t.Fatalf("deferred restore changed model to %s, want %s", model, fallbackModel)
			}
			if fromModel == "postpaid" && statusValue != "suspended" {
				t.Fatalf("deferred restore changed suspended fallback: %s", statusValue)
			}
			var docs int
			if readErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM purser.billing_invoices WHERE tenant_id=$1`, tenant).Scan(&docs); readErr != nil {
				t.Fatal(readErr)
			}
			if docs != 1 {
				t.Fatalf("deferred restore closed an extra billing phase: documents=%d", docs)
			}
		})
	}
}

// testAlreadySweptBootstrapModelMismatch starts after the expiry worker removed
// the grant, independently of preparation's own expiry path.
func testAlreadySweptBootstrapModelMismatch(t *testing.T) {
	t.Helper()
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	declared, fallback, swept, healthy := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'declared','Declared',4,100,'EUR',false),($2,'fallback','Fallback',0,50,'EUR',false)`, declared, fallback); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,billing_model,status) VALUES($1,$2,'prepaid','suspended'),($3,$4,'postpaid','active')`, swept, fallback, healthy, declared); writeErr != nil {
		t.Fatal(writeErr)
	}
	entries := []CustomerBilling{
		{Tenant: TenantRef{Ref: "quartermaster.tenants[swept]"}, Tier: "declared", Model: "postpaid", ClusterAccess: "none"},
		{Tenant: TenantRef{Ref: "quartermaster.tenants[healthy]"}, Tier: "declared", Model: "postpaid", ClusterAccess: "none"},
	}
	qm := &fakeQM{tenantUUIDs: map[string]string{"swept": swept, "healthy": healthy}}
	prepared, err := PrepareCustomerGrants(ctx, db, entries, qm, logging.NewLogger(), nil, nil)
	if err != nil || prepared["swept"] == nil || prepared["swept"].deferred == nil {
		t.Fatalf("already-swept model mismatch was not deferred: prepared=%+v err=%v", prepared, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, _, err := ReconcileCustomerBilling(ctx, tx, entries, qm, prepared)
	if err != nil || len(result.Deferred) != 1 || len(result.Noop) != 1 || result.Noop[0] != "healthy" {
		t.Fatalf("already-swept model mismatch blocked healthy tenant: result=%+v err=%v", result, err)
	}
	if commitErr5 := tx.Commit(); commitErr5 != nil {
		t.Fatal(commitErr5)
	}
}

func testBootstrapPreservesCLIGrant(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, expiredDeclaration := range []bool{false, true} {
		tenant, grantedTier, declaredTier := uuid.NewString(), uuid.NewString(), uuid.NewString()
		if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,metering_enabled) VALUES($1::text::uuid,$1::text,'Granted',4,100,false),($2::text::uuid,$2::text,'Declared',1,40,false)`, grantedTier, declaredTier); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model) VALUES($1,$2,'active','postpaid')`, tenant, grantedTier); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.subscription_operator_grants(subscription_id,base_price,waive_usage,reason,assigned_tier_id) SELECT id,0,true,'CLI trial',tier_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant); err != nil {
			t.Fatal(err)
		}
		entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[protected]"}, Model: "postpaid", Tier: declaredTier, ClusterAccess: "none"}
		if expiredDeclaration {
			entry.OperatorGrant = &billing.OperatorGrantSpec{WaiveUsage: true, Reason: "old declaration", Until: "2020-01-01"}
		}
		qm := &fakeQM{tenantUUIDs: map[string]string{"protected": tenant}}
		prepared, err := PrepareCustomerGrants(t.Context(), db, []CustomerBilling{entry}, qm, logging.NewLogger(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		_, ops, err := ReconcileCustomerBilling(t.Context(), tx, []CustomerBilling{entry}, qm, prepared)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var got string
		if err = db.QueryRowContext(t.Context(), `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != grantedTier {
			t.Fatalf("bootstrap overwrote CLI granted tier: %s", got)
		}
		for _, op := range ops {
			if op.Kind == PostCommitSetDeploymentTier && op.Tier != grantedTier {
				t.Fatalf("bootstrap propagated declared tier under CLI grant: %+v", op)
			}
		}
	}
}

func assertBootstrapPreservesUncollectedUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tenant, current, target, upgrade := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,currency)
 VALUES($1::uuid,$1::text,'Current',2,'EUR'),($2::uuid,$2::text,'Target',3,'EUR')`, current, target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions(tenant_id,tier_id,status,billing_model,pending_reason) VALUES($1,$2,'active','postpaid',NULL)`, tenant, current)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO purser.subscription_tier_upgrades(id,tenant_id,old_tier_id,tier_id,actor_id,old_period_start,old_period_end,requested_at,provider,collection_provider,customer_id,provider_subscription_id,stripe_price_id,old_base_cents,base_cents,state)
 VALUES($1,$2,$3,$3,'operator',NOW()-INTERVAL '1 day',NOW()+INTERVAL '29 days',NOW(),'invoice','','','','',0,0,'pricing_applied')`, upgrade, tenant, current)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := ReconcileCustomerBilling(ctx, db, []CustomerBilling{{Tenant: TenantRef{Ref: "quartermaster.tenants[upgrade]"}, Model: "postpaid", Tier: target, ClusterAccess: "none"}}, &fakeQM{tenantUUIDs: map[string]string{"upgrade": tenant}}, nil)
	if err != nil || len(result.Deferred) != 1 {
		t.Fatalf("bootstrap ignored uncollected upgrade: result=%+v err=%v", result, err)
	}
	var actual string
	if err = db.QueryRowContext(ctx, `SELECT tier_id::text FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant).Scan(&actual); err != nil || actual != current {
		t.Fatalf("bootstrap overwrote purchased tier: %s %v", actual, err)
	}
}

func assertBootstrapCreationPeriods(t *testing.T, db *sql.DB, tenantID string) {
	t.Helper()
	var created time.Time
	var start, end, next sql.NullTime
	if err := db.QueryRow(`SELECT created_at,billing_period_start,billing_period_end,next_billing_date FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenantID).Scan(&created, &start, &end, &next); err != nil {
		t.Fatal(err)
	}
	if !start.Valid || !end.Valid || !next.Valid {
		t.Fatalf("bootstrap subscription has NULL billing coordinates: %v %v %v", start, end, next)
	}
	if !start.Time.Equal(created) || !end.Time.Equal(billing.NextMonthlyAnniversary(created)) || !next.Time.Equal(end.Time) {
		t.Fatalf("bootstrap billed before creation or lost anniversary: created=%s start=%s end=%s next=%s", created, start.Time, end.Time, next.Time)
	}
}

// A fresh derived tenant with a declared grant, as the stack's stack-qa
// fixture declares it: the primary is the edge, never the priced official
// control cluster, and the cache invalidation runs after the primary and
// deployment tier exist, since Commodore resolves its fanout from them.
func TestBootstrapFreshDerivedGrantSetsAnEdgePrimaryBeforeInvalidation_RealPG(t *testing.T) {
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers(id,tier_name,display_name,tier_level,base_price,currency,metering_enabled) VALUES($1,'fresh-grant','Fresh grant',1,100,'EUR',false)`, uuid.NewString()); writeErr != nil {
		t.Fatal(writeErr)
	}
	for _, clusterID := range []string{"central-primary", "media-b", "media-a"} {
		if _, writeErr := db.ExecContext(ctx, `INSERT INTO purser.cluster_pricing(id,cluster_id,pricing_model,required_tier_level,allow_free_tier) VALUES($1,$2,'tier_inherit',0,TRUE)`, uuid.NewString(), clusterID); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	zero := "0"
	entry := CustomerBilling{Tenant: TenantRef{Ref: "quartermaster.tenants[stack-qa]"}, Model: "postpaid", Tier: "fresh-grant", ClusterAccess: "derived", OperatorGrant: &billing.OperatorGrantSpec{BaseFee: &zero, WaiveUsage: true, Reason: "Stack QA account"}}
	qm := &fakeQM{tenantUUIDs: map[string]string{"stack-qa": tenantID}, official: []string{"media-a", "media-b"}, officialControl: []string{"central-primary"}}
	prepared, err := PrepareCustomerGrants(ctx, db, []CustomerBilling{entry}, qm, logging.NewLogger(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, ops, err := ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm, prepared)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		t.Fatal(commitErr)
	}
	if len(result.Created) != 1 {
		t.Fatalf("result = %+v, want the subscription created", result)
	}
	index := map[PostCommitKind]int{}
	for i, op := range ops {
		if _, seen := index[op.Kind]; !seen {
			index[op.Kind] = i
		}
		if op.Kind == PostCommitSetPrimaryCluster && (op.ClusterID != "media-a" || strings.Join(op.PrimaryCandidates, ",") != "media-a,media-b") {
			t.Fatalf("set-primary op = %+v, want media-a from media-a,media-b", op)
		}
	}
	for _, kind := range []PostCommitKind{PostCommitSetPrimaryCluster, PostCommitSetDeploymentTier, PostCommitInvalidateTenantCache} {
		if _, ok := index[kind]; !ok {
			t.Fatalf("post-commit ops %+v lack %s", ops, kind)
		}
	}
	if index[PostCommitInvalidateTenantCache] < index[PostCommitSetPrimaryCluster] || index[PostCommitInvalidateTenantCache] < index[PostCommitSetDeploymentTier] {
		t.Fatalf("cache invalidation precedes the tenant's routing ops: %+v", ops)
	}
}
