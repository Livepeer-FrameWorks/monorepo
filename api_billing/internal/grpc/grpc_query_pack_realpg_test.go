//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/google/uuid"
)

func TestGRPCQueryPack_RealPG(t *testing.T) { //nolint:funlen // One engine startup exercises the cohesive gRPC query contract.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	queries := purserdb.New(db)
	tenantID := uuid.NewString()
	tierID := uuid.NewString()
	subscriptionID := uuid.NewString()
	windowStart := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)
	windowEnd := windowStart.Add(5 * time.Minute)

	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.billing_tiers (
			id, tier_name, display_name, base_price, currency, tier_level,
			stripe_price_id_monthly, stripe_price_id_yearly
		) VALUES ($1,$2,'Contract Pro',20,'EUR',2,'price_month','price_year')
	`, tierID, "contract-pro-"+uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.tenant_subscriptions (
			id, tenant_id, tier_id, status, billing_model, billing_email, billing_name,
			billing_address, payment_method
		) VALUES ($1,$2,$3,'suspended','postpaid','old@example.com','Contract Customer',
			'{"street":"Main 1","city":"Leiden","postal_code":"2332 ED","country":"NL"}'::jsonb,'stripe')
	`, subscriptionID, tenantID, tierID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.mollie_customers (tenant_id, mollie_customer_id)
		VALUES ($1,'cst_contract')
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	t.Run("usage reads", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.usage_records (
				tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
				source_id, report_id, usage_value, value_kind, period_start, period_end, granularity
			) VALUES ($1,'cluster-contract','egress_gb','gibibyte','{"direction":"egress"}'::jsonb,$2,
				'source-contract',$3,4.5,'delta',$4,$5,'minute_5')
		`, tenantID, strings.Repeat("a", 64), strings.Repeat("b", 64), windowStart, windowEnd); err != nil {
			t.Fatal(err)
		}
		records, err := queries.ListUsageRecords(ctx, purserdb.ListUsageRecordsParams{
			TenantID: tenantID, WindowStart: windowStart, WindowEnd: windowEnd,
			ResultLimit: 10,
		})
		if err != nil || len(records) != 1 || records[0].UsageValue != 4.5 {
			t.Fatalf("usage records=%+v err=%v", records, err)
		}
		aggregates, err := queries.ListUsageAggregates(ctx, purserdb.ListUsageAggregatesParams{
			Granularity: "hourly", TenantID: tenantID, WindowStart: windowStart,
			WindowEnd: windowEnd, UsageTypes: []string{},
		})
		if err != nil || len(aggregates) != 1 || aggregates[0].UsageValue != 4.5 {
			t.Fatalf("usage aggregates=%+v err=%v", aggregates, err)
		}
		totals, err := queries.ListTenantUsageTotals(ctx, purserdb.ListTenantUsageTotalsParams{
			TenantID: tenantID, StartDate: windowStart, EndDate: windowStart,
		})
		if err != nil || len(totals) != 1 || totals[0].Total != 4.5 {
			t.Fatalf("usage totals=%+v err=%v", totals, err)
		}
		dimensioned, err := queries.ListTenantDimensionedUsage(ctx, purserdb.ListTenantDimensionedUsageParams{
			TenantID: tenantID, StartDate: windowStart, EndDate: windowStart,
		})
		if err != nil || len(dimensioned) != 1 || dimensioned[0].Quantity != "4.500000" {
			t.Fatalf("dimensioned usage=%+v err=%v", dimensioned, err)
		}
	})

	t.Run("billing and provider boundaries", func(t *testing.T) {
		if rows, err := queries.UpdateTenantBillingDetails(ctx, purserdb.UpdateTenantBillingDetailsParams{
			SetEmail: true, Email: "new@example.com", Address: json.RawMessage(`{}`), TenantID: tenantID,
		}); err != nil || rows != 1 {
			t.Fatalf("update billing details rows=%d err=%v", rows, err)
		}
		details, err := queries.GetTenantBillingDetails(ctx, tenantID)
		if err != nil || details.BillingEmail.String != "new@example.com" || len(details.BillingAddress) == 0 {
			t.Fatalf("billing details=%+v err=%v", details, err)
		}
		tier, err := queries.GetStripeTierCheckoutConfig(ctx, purserdb.GetStripeTierCheckoutConfigParams{TierID: tierID})
		if err != nil || tier.PriceID != "price_month" {
			t.Fatalf("stripe tier=%+v err=%v", tier, err)
		}
		intentID, err := queries.UpsertStripeTenantCheckoutIntent(ctx, purserdb.UpsertStripeTenantCheckoutIntentParams{
			TenantID: tenantID, TierID: tierID, Currency: "EUR", IdempotencyKey: "contract-stripe-" + tenantID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := queries.SetProviderIntentCustomer(ctx, purserdb.SetProviderIntentCustomerParams{
			CustomerID: sql.NullString{String: "cus_contract", Valid: true}, IntentID: intentID,
		}); err != nil {
			t.Fatal(err)
		}
		if rows, err := queries.StageStripeCheckoutTier(ctx, purserdb.StageStripeCheckoutTierParams{
			TierID: tierID, IntentID: intentID, TenantID: tenantID,
		}); err != nil || rows != 1 {
			t.Fatalf("stage stripe tier rows=%d err=%v", rows, err)
		}
		if rows, err := queries.ReactivateSuspendedTenant(ctx, tenantID); err != nil || rows != 1 {
			t.Fatalf("reactivate rows=%d err=%v", rows, err)
		}
	})

	t.Run("x402 mutation ownership", func(t *testing.T) {
		quoteID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.x402_payment_quotes (
				id, tenant_id, resource, resource_class, network, asset, pay_to,
				amount_atomic, credit_amount_cents, eur_per_usd_rate, requirements_json,
				status, expires_at
			) VALUES ($1,$2,'contract','api','eip155:1','0x0000000000000000000000000000000000000001',
				'0x0000000000000000000000000000000000000002',1,1,1,'{}','confirmed',NOW()+INTERVAL '1 hour')
		`, quoteID, tenantID); err != nil {
			t.Fatal(err)
		}
		fingerprint := strings.Repeat("c", 64)
		key := "contract-mutation"
		inserted, err := queries.ClaimX402Mutation(ctx, purserdb.ClaimX402MutationParams{
			TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key,
			RequestFingerprint: fingerprint, Protocol: "http", Operation: "contract",
		})
		if err != nil || inserted != 1 {
			t.Fatalf("claim rows=%d err=%v", inserted, err)
		}
		claim, err := queries.GetX402MutationClaim(ctx, purserdb.GetX402MutationClaimParams{
			TenantID: tenantID, IdempotencyKey: key,
		})
		if err != nil || claim.QuoteID != quoteID || claim.Status != "claimed" {
			t.Fatalf("claim=%+v err=%v", claim, err)
		}
		updated, err := queries.CompleteX402Mutation(ctx, purserdb.CompleteX402MutationParams{
			TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key,
			RequestFingerprint: fingerprint, Result: []byte(`{"ok":true}`),
			ContentType: "application/json", StatusCode: 200,
		})
		if err != nil || updated != 1 {
			t.Fatalf("complete rows=%d err=%v", updated, err)
		}
		completed, err := queries.IsX402MutationCompleted(ctx, purserdb.IsX402MutationCompletedParams{
			TenantID: tenantID, QuoteID: quoteID, IdempotencyKey: key, RequestFingerprint: fingerprint,
		})
		if err != nil || !completed {
			t.Fatalf("completed=%v err=%v", completed, err)
		}
	})

	t.Run("invoice line item scan", func(t *testing.T) {
		invoiceID := uuid.NewString()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_invoices (id, tenant_id, due_date)
			VALUES ($1,$2,NOW()+INTERVAL '7 days')
		`, invoiceID, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.invoice_line_items (
				invoice_id, tenant_id, line_key, unit, description, quantity,
				billable_quantity, unit_price, amount, currency
			) VALUES ($1,$2,'base_subscription','month','Subscription',1,1,20,20,'EUR')
		`, invoiceID, tenantID); err != nil {
			t.Fatal(err)
		}
		items, err := queries.ListInvoiceLineItemsForTenant(ctx, purserdb.ListInvoiceLineItemsForTenantParams{
			InvoiceID: invoiceID, TenantID: tenantID,
		})
		if err != nil || len(items) != 1 || items[0].Amount != "20.00" || len(items[0].Dimensions) == 0 {
			t.Fatalf("line items=%+v err=%v", items, err)
		}
	})
}
