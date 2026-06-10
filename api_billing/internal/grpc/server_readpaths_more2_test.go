package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// clearPaymentEnv neutralizes the env-driven branches in
// getAvailablePaymentMethods so tests don't depend on the runner's shell.
func clearPaymentEnv(t *testing.T) {
	t.Helper()
	t.Setenv("STRIPE_SECRET_KEY", "")
	t.Setenv("MOLLIE_API_KEY", "")
	t.Setenv("ARBISCAN_API_KEY", "")
}

// GetPaymentMethods is purely env + hd_wallet_state driven. With no providers
// configured and no HD wallet xpub, the advertised set must be empty — never a
// crypto method when the on-chain explorer key is absent.
func TestGetPaymentMethodsEmptyWhenUnconfigured(t *testing.T) {
	clearPaymentEnv(t)
	s, mock := newReadServer(t, false)

	// hasHDWalletXpub runs unconditionally inside the && chain.
	mock.ExpectQuery(`SELECT xpub FROM purser\.hd_wallet_state`).
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetPaymentMethods(context.Background(), &purserpb.GetPaymentMethodsRequest{})
	if err != nil {
		t.Fatalf("GetPaymentMethods: %v", err)
	}
	if len(resp.Methods) != 0 {
		t.Fatalf("methods = %v, want empty", resp.Methods)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestGetBillingDetailsMapsRowAndComplete(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	addr := `{"street":"1 A St","city":"Berlin","state":"","postal_code":"10115","country":"DE"}`
	rows := sqlmock.NewRows([]string{"billing_email", "billing_company", "tax_id", "billing_address", "updated_at"}).
		AddRow("ops@example.com", "Example GmbH", "DE123456789", []byte(addr), now)

	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	resp, err := s.GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetBillingDetails: %v", err)
	}
	if resp.Email != "ops@example.com" || resp.Company != "Example GmbH" || resp.VatNumber != "DE123456789" {
		t.Fatalf("scalar mapping wrong: %+v", resp)
	}
	if resp.Address == nil || resp.Address.City != "Berlin" || resp.Address.Country != "DE" {
		t.Fatalf("address mapping wrong: %+v", resp.Address)
	}
	// Email + full address (street/city/postal/country) → complete.
	if !resp.IsComplete {
		t.Fatalf("IsComplete = false, want true for fully-populated details")
	}
}

// A NULL company/tax and an address missing postal_code must surface as unset
// fields and IsComplete=false — not empty-string defaults masquerading as data.
func TestGetBillingDetailsNullsAndIncomplete(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	addr := `{"street":"1 A St","city":"Berlin","state":"","postal_code":"","country":"DE"}`
	rows := sqlmock.NewRows([]string{"billing_email", "billing_company", "tax_id", "billing_address", "updated_at"}).
		AddRow("ops@example.com", nil, nil, []byte(addr), now)

	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	resp, err := s.GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetBillingDetails: %v", err)
	}
	if resp.Company != "" || resp.VatNumber != "" {
		t.Fatalf("NULL company/tax should stay empty, got %+v", resp)
	}
	if resp.IsComplete {
		t.Fatalf("IsComplete = true, want false (missing postal_code)")
	}
}

func TestGetBillingDetailsNotFound(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-x").
		WillReturnError(sqlmockNoRows())

	_, err := s.GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{TenantId: "tenant-x"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("err = %v, want NotFound", err)
	}
}

func TestGetBillingDetailsEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.GetBillingDetails(context.Background(), &purserpb.GetBillingDetailsRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

func TestListPendingTopupsMapsRowsAndNulls(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "tenant_id", "provider", "checkout_id", "amount_cents", "currency",
		"status", "expires_at", "completed_at", "balance_transaction_id", "created_at", "updated_at",
	}).
		AddRow("tu1", "tenant-1", "stripe", "cs_1", int64(2000), "EUR", "pending", now, nil, nil, now, now).
		AddRow("tu2", "tenant-1", "stripe", "cs_2", int64(500), "EUR", "completed", now, now, "btx-9", now, now)

	mock.ExpectQuery(`FROM purser\.pending_topups WHERE tenant_id = \$1`).
		WithArgs("tenant-1").
		WillReturnRows(rows)

	resp, err := s.ListPendingTopups(context.Background(), &purserpb.ListPendingTopupsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("ListPendingTopups: %v", err)
	}
	if len(resp.Topups) != 2 {
		t.Fatalf("got %d topups, want 2", len(resp.Topups))
	}
	// Pending row: completed_at + balance_transaction_id NULL → unset.
	if resp.Topups[0].CompletedAt != nil || resp.Topups[0].BalanceTransactionId != nil {
		t.Fatalf("pending topup NULLs should be unset: %+v", resp.Topups[0])
	}
	// Completed row: both populated.
	if resp.Topups[1].CompletedAt == nil || resp.Topups[1].GetBalanceTransactionId() != "btx-9" {
		t.Fatalf("completed topup refs not mapped: %+v", resp.Topups[1])
	}
}

// A non-empty status filter appends an AND clause + second arg; assert both args
// reach the driver so the filter actually constrains the query.
func TestListPendingTopupsStatusFilterPassesArg(t *testing.T) {
	s, mock := newReadServer(t, true)
	st := "completed"
	mock.ExpectQuery(`FROM purser\.pending_topups WHERE tenant_id = \$1 AND status = \$2`).
		WithArgs("tenant-1", st).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "provider", "checkout_id", "amount_cents", "currency",
			"status", "expires_at", "completed_at", "balance_transaction_id", "created_at", "updated_at",
		}))

	resp, err := s.ListPendingTopups(context.Background(), &purserpb.ListPendingTopupsRequest{TenantId: "tenant-1", Status: &st})
	if err != nil {
		t.Fatalf("ListPendingTopups: %v", err)
	}
	if len(resp.Topups) != 0 {
		t.Fatalf("got %d topups, want 0", len(resp.Topups))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet: %v", err)
	}
}

func TestListPendingTopupsEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.ListPendingTopups(context.Background(), &purserpb.ListPendingTopupsRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// ListMarketplaceClusterPricings stores base_price as a decimal string and
// converts to integer cents on the way out. Assert the cents conversion (the
// money-math seam) and the tier-gated count/list query pair.
func TestListMarketplaceClusterPricingsConvertsCents(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	// tenant tier-level lookup (tenantID non-empty path)
	mock.ExpectQuery(`SELECT COALESCE\(bt\.tier_level, 0\)`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tier_level"}).AddRow(int32(2)))

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM purser\.cluster_pricing`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int32(1)))

	mock.ExpectQuery(`FROM purser\.cluster_pricing`).
		WillReturnRows(sqlmock.NewRows([]string{
			"cluster_id", "pricing_model", "base_price", "currency", "required_tier_level", "created_at",
		}).AddRow("cluster-a", "flat", "9.99", "EUR", int32(1), now))

	resp, err := s.ListMarketplaceClusterPricings(context.Background(), &purserpb.ListMarketplaceClusterPricingsRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("ListMarketplaceClusterPricings: %v", err)
	}
	if len(resp.Pricings) != 1 {
		t.Fatalf("got %d pricings, want 1", len(resp.Pricings))
	}
	// 9.99 * 100 = 999 cents, rounded.
	if resp.Pricings[0].MonthlyPriceCents != 999 {
		t.Fatalf("MonthlyPriceCents = %d, want 999", resp.Pricings[0].MonthlyPriceCents)
	}
	if resp.Pricings[0].Currency != "EUR" || resp.Pricings[0].ClusterId != "cluster-a" {
		t.Fatalf("pricing mapping wrong: %+v", resp.Pricings[0])
	}
}

// GetBillingStatus over a tenant with no active subscription must degrade
// gracefully: status "none", default currency, empty invoice/payment lists —
// never a nil-deref on the absent subscription/tier.
func TestGetBillingStatusNoActiveSubscription(t *testing.T) {
	clearPaymentEnv(t)
	s, mock := newReadServer(t, false)

	// getSubscriptionAndTier → ErrNoRows → (nil, nil, nil)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions ts`).
		WithArgs("tenant-1").
		WillReturnError(sqlmockNoRows())
	// getPendingInvoices → empty
	mock.ExpectQuery(`FROM purser\.billing_invoices`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "amount", "base_amount", "metered_amount", "prepaid_credit_applied",
			"currency", "status", "due_date", "paid_at", "usage_details", "created_at", "updated_at",
			"period_start", "period_end", "gross_metered_amount",
		}))
	// getRecentPayments → empty
	mock.ExpectQuery(`FROM purser\.billing_payments bp`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "invoice_id", "method", "amount", "currency", "tx_id", "status",
			"confirmed_at", "created_at", "updated_at",
		}))
	// getAvailablePaymentMethods → hd_wallet_state
	mock.ExpectQuery(`SELECT xpub FROM purser\.hd_wallet_state`).
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetBillingStatus(context.Background(), &purserpb.GetBillingStatusRequest{TenantId: "tenant-1"})
	if err != nil {
		t.Fatalf("GetBillingStatus: %v", err)
	}
	if resp.BillingStatus != "none" {
		t.Fatalf("BillingStatus = %q, want none", resp.BillingStatus)
	}
	if resp.OutstandingAmount != 0 {
		t.Fatalf("OutstandingAmount = %v, want 0", resp.OutstandingAmount)
	}
	if resp.Currency == "" {
		t.Fatalf("Currency should default, got empty")
	}
	if len(resp.PendingInvoices) != 0 || len(resp.RecentPayments) != 0 {
		t.Fatalf("expected empty lists, got %d/%d", len(resp.PendingInvoices), len(resp.RecentPayments))
	}
}

func TestGetBillingStatusEmptyTenantGuard(t *testing.T) {
	s := newGuardServer(t)
	_, err := s.GetBillingStatus(context.Background(), &purserpb.GetBillingStatusRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("err = %v, want InvalidArgument", err)
	}
}

// GetTenantUsage aggregates raw usage deltas, then rates them. When the tenant
// has no active tier (LoadEffectiveTier → ErrNoRows) the handler returns the
// usage map with empty costs — a no-subscription steady state, not an error.
func TestGetTenantUsageAggregatesNoTier(t *testing.T) {
	s, mock := newReadServer(t, true)

	// 1. usage_records/usage_adjustments aggregation
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", "2026-01-01", "2026-01-31").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "total"}).
			AddRow("cluster-a", "egress_bytes", float64(1000)).
			AddRow("", "media_seconds", float64(50)))
	// 2. codec breakdowns (empty)
	mock.ExpectQuery(`jsonb_each_text`).
		WithArgs("tenant-1", "2026-01-01", "2026-01-31").
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "key", "seconds"}))
	// 3. LoadEffectiveTier → no active subscription
	mock.ExpectQuery(`metering_enabled`).
		WithArgs("tenant-1").
		WillReturnError(sqlmockNoRows())

	resp, err := s.GetTenantUsage(context.Background(), &purserpb.TenantUsageRequest{
		TenantId:  "tenant-1",
		StartDate: "2026-01-01",
		EndDate:   "2026-01-31",
	})
	if err != nil {
		t.Fatalf("GetTenantUsage: %v", err)
	}
	if resp.Usage["egress_bytes"] != 1000 || resp.Usage["media_seconds"] != 50 {
		t.Fatalf("usage aggregation wrong: %+v", resp.Usage)
	}
	if len(resp.Costs) != 0 {
		t.Fatalf("Costs should be empty with no tier, got %+v", resp.Costs)
	}
	if resp.BillingPeriod != "2026-01-01 to 2026-01-31" {
		t.Fatalf("BillingPeriod = %q", resp.BillingPeriod)
	}
}

func TestGetTenantUsageInputGuards(t *testing.T) {
	s := newGuardServer(t)
	ctx := context.Background() // service call → skips tenant-context guard

	if _, err := s.GetTenantUsage(ctx, &purserpb.TenantUsageRequest{StartDate: "x", EndDate: "y"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty tenant: err = %v, want InvalidArgument", err)
	}
	if _, err := s.GetTenantUsage(ctx, &purserpb.TenantUsageRequest{TenantId: "t1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty dates: err = %v, want InvalidArgument", err)
	}
}
