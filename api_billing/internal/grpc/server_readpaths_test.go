package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// sqlmockNoRows is the error a QueryRow scan surfaces when no row matched.
// errors.Is(..., sql.ErrNoRows) drives several "absent → default" branches.
func sqlmockNoRows() error { return sql.ErrNoRows }

// newReadServer builds a PurserServer over a regexp-matching sqlmock. Matching
// on query fragments (not full SQL) keeps these tests resilient to whitespace
// and column reordering while still pinning the row->proto mapping behavior.
func newReadServer(t *testing.T, inOrder bool) (*PurserServer, sqlmock.Sqlmock) {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	mock.MatchExpectationsInOrder(inOrder)
	t.Cleanup(func() { _ = mockDB.Close() })
	return &PurserServer{db: mockDB, logger: logging.NewLogger()}, mock
}

func TestListBalanceTransactionsMapsRows(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "tenant_id", "amount_cents", "balance_after_cents",
		"transaction_type", "description", "reference_id", "reference_type", "created_at",
	}).
		AddRow("tx1", "tenant-1", int64(500), int64(1500), "topup", "Card top-up", "inv-9", "invoice", now).
		AddRow("tx2", "tenant-1", int64(-200), int64(1300), "usage", "Delivery", nil, nil, now)

	mock.ExpectQuery(`FROM purser\.balance_transactions`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	resp, err := s.ListBalanceTransactions(context.Background(), &purserpb.ListBalanceTransactionsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("ListBalanceTransactions: %v", err)
	}
	if len(resp.Transactions) != 2 {
		t.Fatalf("got %d transactions, want 2", len(resp.Transactions))
	}

	credit := resp.Transactions[0]
	if credit.AmountCents != 500 || credit.BalanceAfterCents != 1500 {
		t.Fatalf("credit amounts wrong: %+v", credit)
	}
	if credit.GetReferenceId() != "inv-9" || credit.GetReferenceType() != "invoice" {
		t.Fatalf("credit refs not mapped: %+v", credit)
	}

	// Debits carry a negative amount; NULL references stay unset (not "").
	debit := resp.Transactions[1]
	if debit.AmountCents != -200 {
		t.Fatalf("debit amount = %d, want -200", debit.AmountCents)
	}
	if debit.ReferenceId != nil || debit.ReferenceType != nil {
		t.Fatalf("NULL refs should be nil, got %+v", debit)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetClusterPricingMapsRow(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	row := sqlmock.NewRows([]string{
		"id", "cluster_id", "pricing_model",
		"stripe_product_id", "stripe_price_id_monthly", "stripe_meter_event_name",
		"base_price", "currency", "metered_rates",
		"required_tier_level", "allow_free_tier",
		"default_quotas", "created_at", "updated_at",
	}).AddRow(
		"cp1", "cluster-7", "metered",
		"prod_x", nil, "meter_evt",
		"12.5", "USD", []byte(`{"egress_gb":0.01}`),
		int32(2), true,
		[]byte(`{"max_streams":10}`), now, now,
	)

	mock.ExpectQuery(`FROM purser\.cluster_pricing`).
		WithArgs("cluster-7").
		WillReturnRows(row)

	got, err := s.GetClusterPricing(context.Background(), &purserpb.GetClusterPricingRequest{ClusterId: "cluster-7"})
	if err != nil {
		t.Fatalf("GetClusterPricing: %v", err)
	}
	if got.Id != "cp1" || got.ClusterId != "cluster-7" || got.PricingModel != "metered" {
		t.Fatalf("identity fields wrong: %+v", got)
	}
	// base_price::text "12.5" must render at 2 decimals; currency passes through.
	if got.BasePrice != "12.50" || got.Currency != "USD" {
		t.Fatalf("money/currency mapping wrong: base=%q currency=%q", got.BasePrice, got.Currency)
	}
	if got.RequiredTierLevel != 2 || !got.AllowFreeTier {
		t.Fatalf("tier/free flags wrong: %+v", got)
	}
	if got.GetStripeProductId() != "prod_x" || got.StripePriceIdMonthly != nil {
		t.Fatalf("nullable stripe fields wrong: %+v", got)
	}
	if got.MeteredRates == nil || got.MeteredRates.GetFields()["egress_gb"].GetNumberValue() != 0.01 {
		t.Fatalf("metered_rates JSONB not decoded: %+v", got.MeteredRates)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetClusterPricingDefaultsWhenAbsent(t *testing.T) {
	s, mock := newReadServer(t, true)

	// No row → the method synthesizes default tier-inherit pricing rather than
	// erroring, so unconfigured clusters are still resolvable.
	mock.ExpectQuery(`FROM purser\.cluster_pricing`).
		WithArgs("cluster-unknown").
		WillReturnError(sqlmockNoRows())

	got, err := s.GetClusterPricing(context.Background(), &purserpb.GetClusterPricingRequest{ClusterId: "cluster-unknown"})
	if err != nil {
		t.Fatalf("GetClusterPricing: %v", err)
	}
	if got.ClusterId != "cluster-unknown" || got.PricingModel != "tier_inherit" {
		t.Fatalf("default pricing wrong: %+v", got)
	}
	if got.Currency != "EUR" || got.AllowFreeTier {
		t.Fatalf("default currency/free-tier wrong: %+v", got)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListClusterPricingsAdminListAll(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "cluster_id", "pricing_model",
		"stripe_product_id", "stripe_price_id_monthly", "stripe_meter_event_name",
		"base_price", "currency", "metered_rates",
		"required_tier_level", "allow_free_tier",
		"default_quotas", "created_at", "updated_at",
	}).AddRow(
		"cp1", "cluster-a", "monthly",
		nil, nil, nil,
		"30", "EUR", []byte(`{}`),
		int32(0), false,
		[]byte(`{}`), now, now,
	)

	// owner_tenant_id empty → admin "list all" path, no Quartermaster call.
	mock.ExpectQuery(`FROM purser\.cluster_pricing`).WillReturnRows(rows)

	resp, err := s.ListClusterPricings(context.Background(), &purserpb.ListClusterPricingsRequest{})
	if err != nil {
		t.Fatalf("ListClusterPricings: %v", err)
	}
	if len(resp.Pricings) != 1 {
		t.Fatalf("got %d pricings, want 1", len(resp.Pricings))
	}
	if resp.Pricings[0].ClusterId != "cluster-a" || resp.Pricings[0].PricingModel != "monthly" {
		t.Fatalf("pricing mapping wrong: %+v", resp.Pricings[0])
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListInvoicesMapsRowsAndLineItems(t *testing.T) {
	s, mock := newReadServer(t, false)
	now := time.Now()

	invoiceRows := sqlmock.NewRows([]string{
		"id", "tenant_id", "amount", "base_amount", "metered_amount", "prepaid_credit_applied",
		"currency", "status", "due_date", "paid_at", "usage_details",
		"created_at", "updated_at", "period_start", "period_end", "gross_metered_amount",
	}).AddRow(
		"inv-1", "tenant-1", 42.5, 30.0, 12.5, 0.0,
		"EUR", "pending", now, nil, []byte(`{"delivered_minutes":100}`),
		now, now, now, now, 12.5,
	)
	mock.ExpectQuery(`FROM purser\.billing_invoices`).
		WithArgs("tenant-1", int32(51)).
		WillReturnRows(invoiceRows)

	lineRows := sqlmock.NewRows([]string{
		"line_key", "meter", "description", "quantity", "included_quantity", "billable_quantity",
		"unit_price", "amount", "currency", "cluster_id", "cluster_kind", "pricing_source",
	}).AddRow(
		"base_subscription", "", "Monthly subscription", "1", "0", "1",
		"30.00", "30.00", "EUR", "", "", "tier",
	)
	mock.ExpectQuery(`FROM purser\.invoice_line_items`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(lineRows)

	resp, err := s.ListInvoices(context.Background(), &purserpb.ListInvoicesRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("ListInvoices: %v", err)
	}
	if len(resp.Invoices) != 1 {
		t.Fatalf("got %d invoices, want 1", len(resp.Invoices))
	}
	inv := resp.Invoices[0]
	if inv.Id != "inv-1" || inv.Status != "pending" || inv.Currency != "EUR" {
		t.Fatalf("invoice identity wrong: %+v", inv)
	}
	if inv.Amount != 42.5 || inv.MeteredAmount != 12.5 {
		t.Fatalf("invoice money wrong: %+v", inv)
	}
	if inv.UsageDetails == nil || inv.UsageDetails.GetFields()["delivered_minutes"].GetNumberValue() != 100 {
		t.Fatalf("usage_details JSONB not decoded: %+v", inv.UsageDetails)
	}
	if len(inv.LineItems) != 1 || inv.LineItems[0].LineKey != "base_subscription" {
		t.Fatalf("line items not loaded: %+v", inv.LineItems)
	}
	// pricing_source "tier" maps to the human label.
	if inv.LineItems[0].PricingLabel != "Subscription tier" {
		t.Fatalf("pricing label wrong: %q", inv.LineItems[0].PricingLabel)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetBillingTiersMapsTierWithRulesAndEntitlements(t *testing.T) {
	s, mock := newReadServer(t, false)
	now := time.Now()

	tierRows := sqlmock.NewRows([]string{
		"id", "tier_name", "display_name", "description", "base_price", "currency", "billing_period",
		"features", "support_level", "sla_level", "metering_enabled",
		"is_active", "tier_level", "is_enterprise", "created_at", "updated_at",
		"is_default_prepaid", "is_default_postpaid",
		"processes_live", "processes_dvr", "processes_clip", "processes_dvr_finalize", "processes_vod",
	}).AddRow(
		"tier-pro", "pro", "Pro", "Pro plan", 29.0, "EUR", "monthly",
		[]byte(`{"recording":true}`), "premium", "gold", true,
		true, int32(2), false, now, now,
		false, true,
		"livepeer", "", "", "", "",
	)
	mock.ExpectQuery(`FROM purser\.billing_tiers`).WillReturnRows(tierRows)

	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs("tier-pro").
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("delivered_minutes", "tiered_graduated", "EUR", "1000", "0.0005", "{}"))

	mock.ExpectQuery(`FROM purser\.tier_entitlements`).
		WithArgs("tier-pro").
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
			AddRow("retention_days", "90"))

	// getAvailablePaymentMethods probes hd_wallet_state; an error there is
	// swallowed (no crypto methods), so we let it fail loudly-but-harmlessly.
	mock.ExpectQuery(`FROM purser\.hd_wallet_state`).WillReturnError(sqlmockNoRows())

	resp, err := s.GetBillingTiers(context.Background(), &purserpb.GetBillingTiersRequest{})
	if err != nil {
		t.Fatalf("GetBillingTiers: %v", err)
	}
	if len(resp.Tiers) != 1 {
		t.Fatalf("got %d tiers, want 1", len(resp.Tiers))
	}
	tier := resp.Tiers[0]
	if tier.Id != "tier-pro" || tier.TierName != "pro" || tier.TierLevel != 2 {
		t.Fatalf("tier identity wrong: %+v", tier)
	}
	if tier.Features == nil || !tier.Features.Recording {
		t.Fatalf("features JSONB not decoded: %+v", tier.Features)
	}
	if tier.ProcessesLive != "livepeer" {
		t.Fatalf("processes_live not mapped: %q", tier.ProcessesLive)
	}
	if len(tier.PricingRules) != 1 || tier.PricingRules[0].Meter != "delivered_minutes" {
		t.Fatalf("pricing rules not loaded: %+v", tier.PricingRules)
	}
	if tier.Entitlements["retention_days"] != "90" {
		t.Fatalf("entitlements not loaded: %+v", tier.Entitlements)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
