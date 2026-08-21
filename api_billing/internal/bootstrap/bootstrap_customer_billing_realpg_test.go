//go:build schema_verify

package bootstrap

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestBootstrapCustomerBillingRepository_RealPG(t *testing.T) { //nolint:funlen // One transaction proves the complete customer bootstrap boundary.
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()
	tierID := uuid.NewString()
	tenantID := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
			id, tier_name, display_name, tier_level, currency
		) VALUES ($1, 'bootstrap-contract', 'Bootstrap contract', 2, 'EUR')
	`, tierID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id       string
		required int
	}{
		{id: "official-a", required: 1},
		{id: "private-b", required: 1},
		{id: "official-too-high", required: 3},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.cluster_pricing (
				id, cluster_id, pricing_model, required_tier_level, allow_free_tier
			) VALUES ($1, $2, 'tier_inherit', $3, FALSE)
		`, uuid.NewString(), row.id, row.required); err != nil {
			t.Fatal(err)
		}
	}

	qm := &fakeQM{
		tenantUUIDs: map[string]string{"acme": tenantID},
		official:    []string{"official-a", "official-too-high"},
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
	result, post, err := ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(result.Created) != 1 {
		t.Fatalf("first result = %+v", result)
	}
	assertBootstrapPostCommitOps(t, post)

	var subscriptionID, storedTierID, model string
	if err := db.QueryRowContext(ctx, `
		SELECT id::text, tier_id::text, billing_model
		FROM purser.tenant_subscriptions WHERE tenant_id = $1
	`, tenantID).Scan(&subscriptionID, &storedTierID, &model); err != nil {
		t.Fatal(err)
	}
	if storedTierID != tierID || model != "prepaid" {
		t.Fatalf("subscription tier/model = %s/%s", storedTierID, model)
	}
	var overrideCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.subscription_entitlement_overrides
		WHERE subscription_id = $1
	`, subscriptionID).Scan(&overrideCount); err != nil {
		t.Fatal(err)
	}
	if overrideCount != 2 {
		t.Fatalf("override count = %d", overrideCount)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = 700
		WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, post, err = ReconcileCustomerBilling(ctx, tx, []CustomerBilling{entry}, qm)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(result.Noop) != 1 {
		t.Fatalf("second result = %+v", result)
	}
	assertBootstrapPostCommitOps(t, post)
	var balance int64
	var balanceRows int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(balance_cents)
		FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'
	`, tenantID).Scan(&balanceRows, &balance); err != nil {
		t.Fatal(err)
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
			primary = op.ClusterID == "official-a"
		case PostCommitSetDeploymentTier:
			tier = op.Tier == "bootstrap-contract"
		}
	}
	if !grant || !primary || !tier {
		t.Fatalf("post-commit operations missing grant/primary/tier: %+v", ops)
	}
}
