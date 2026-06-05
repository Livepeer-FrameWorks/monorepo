package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func newInvoiceServer(t *testing.T) (*PurserServer, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	// quartermasterClient left nil → enrichLineItemClusterNames is a no-op.
	s := &PurserServer{db: db, logger: logging.NewLogger()}
	return s, mock, func() { _ = db.Close() }
}

func lineItemRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"line_key", "meter", "description",
		"quantity", "included_quantity", "billable_quantity",
		"unit_price", "amount", "currency",
		"cluster_id", "cluster_kind", "pricing_source",
	}).AddRow(
		"base_subscription", "", "Monthly subscription",
		"1", "0", "1",
		"79.00", "79.00", "EUR",
		"", "", "tier",
	).AddRow(
		"meter:egress_gb", "egress_gb", "Bandwidth",
		"120", "100", "20",
		"0.01", "0.20", "EUR",
		"cluster-1", "platform_official", "cluster_metered",
	)
}

func TestLoadInvoiceLineItems(t *testing.T) {
	s, mock, done := newInvoiceServer(t)
	defer done()
	const inv, tenant = "inv-1", "tenant-1"

	mock.ExpectQuery(`FROM purser\.invoice_line_items\s+WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs(inv, tenant).
		WillReturnRows(lineItemRows())

	items, err := s.loadInvoiceLineItems(context.Background(), inv, tenant)
	if err != nil {
		t.Fatalf("loadInvoiceLineItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d line items, want 2", len(items))
	}
	// Row→proto mapping: base line first (SQL orders it first), text decimals preserved.
	if items[0].GetLineKey() != "base_subscription" || items[0].GetTotal() != "79.00" {
		t.Errorf("base line mapped wrong: %+v", items[0])
	}
	if items[1].GetMeter() != "egress_gb" || items[1].GetBillableQuantity() != "20" || items[1].GetClusterId() != "cluster-1" {
		t.Errorf("metered line mapped wrong: %+v", items[1])
	}
	if items[1].GetPricingLabel() != "Cluster metered" {
		t.Errorf("pricing_label = %q, want \"Cluster metered\" (derived from cluster_metered/platform_official)", items[1].GetPricingLabel())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetInvoice_TenantScoped(t *testing.T) {
	s, mock, done := newInvoiceServer(t)
	defer done()
	const inv, tenant = "inv-1", "tenant-1"
	now := time.Unix(1_700_000_000, 0)

	// Main invoice SELECT (tenant-scoped → AND i.tenant_id = $2). tier_id "" so
	// the GetBillingTier branch is skipped. 17 scanned columns.
	mock.ExpectQuery(`FROM purser\.billing_invoices i\s+LEFT JOIN purser\.tenant_subscriptions`).
		WithArgs(inv, tenant).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "amount", "base_amount", "metered_amount", "prepaid_credit_applied",
			"currency", "status", "due_date", "paid_at", "usage_details", "created_at", "updated_at",
			"tier_id", "period_start", "period_end", "gross_metered_amount",
		}).AddRow(
			inv, tenant, 79.20, 79.00, 0.20, 0.00,
			"EUR", "paid", now, nil, nil, now, now,
			"", nil, nil, 0.20,
		))
	// loadInvoiceLineItems follow-up query.
	mock.ExpectQuery(`FROM purser\.invoice_line_items`).
		WithArgs(inv, tenant).
		WillReturnRows(lineItemRows())

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenant)
	resp, err := s.GetInvoice(ctx, &purserpb.GetInvoiceRequest{InvoiceId: inv})
	if err != nil {
		t.Fatalf("GetInvoice: %v", err)
	}
	if resp.GetInvoice().GetId() != inv || resp.GetInvoice().GetStatus() != "paid" {
		t.Errorf("invoice header mapped wrong: %+v", resp.GetInvoice())
	}
	if len(resp.GetInvoice().GetLineItems()) != 2 {
		t.Errorf("expected 2 line items, got %d", len(resp.GetInvoice().GetLineItems()))
	}
	if resp.GetTier() != nil {
		t.Error("tier should be nil when tier_id is empty")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetInvoice_RejectsMissingTenant(t *testing.T) {
	s, _, done := newInvoiceServer(t)
	defer done()
	// A user-scoped call with no tenant is NOT a service call → PermissionDenied
	// before any query. (An empty context would be treated as a service call.)
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	_, err := s.GetInvoice(ctx, &purserpb.GetInvoiceRequest{InvoiceId: "inv-1"})
	if err == nil {
		t.Fatal("expected PermissionDenied without tenant context")
	}
}

func TestGetInvoice_RequiresInvoiceID(t *testing.T) {
	s, _, done := newInvoiceServer(t)
	defer done()
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	if _, err := s.GetInvoice(ctx, &purserpb.GetInvoiceRequest{}); err == nil {
		t.Fatal("expected InvalidArgument when invoice_id is empty")
	}
}
