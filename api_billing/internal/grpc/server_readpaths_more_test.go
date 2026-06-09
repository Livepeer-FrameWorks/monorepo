package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestLoadStoragePricingMapsRow(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs("tier-1", "storage_gb_seconds_cold").
		WillReturnRows(sqlmock.NewRows([]string{"included", "unit_price", "model", "currency"}).
			AddRow(100.0, 0.02, "tiered", "USD"))

	got := s.loadStoragePricing(context.Background(), "tier-1")
	if got == nil {
		t.Fatal("expected pricing, got nil")
	}
	if got.IncludedGbHours != 100.0 || got.UnitPricePerGbHour != 0.02 || got.Model != "tiered" || got.Currency != "USD" {
		t.Fatalf("storage pricing mapped wrong: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadStoragePricingNilWhenAbsent(t *testing.T) {
	s, mock := newReadServer(t, true)
	// No rule configured -> nil (not an error): tiers without a cold-storage
	// rule simply have no storage pricing to surface.
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs("tier-1", "storage_gb_seconds_cold").
		WillReturnError(sqlmockNoRows())

	if got := s.loadStoragePricing(context.Background(), "tier-1"); got != nil {
		t.Fatalf("absent rule should map to nil, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetRecentPaymentsMapsRows(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()
	rows := sqlmock.NewRows([]string{
		"id", "invoice_id", "method", "amount", "currency",
		"tx_id", "status", "confirmed_at", "created_at", "updated_at",
	}).
		AddRow("pay-1", "inv-1", "card", 12.50, "USD", "tx-abc", "confirmed", now, now, now).
		AddRow("pay-2", "inv-2", "crypto_eth", 5.00, "USD", nil, "pending", nil, now, now)

	mock.ExpectQuery(`FROM purser\.billing_payments bp\s+JOIN purser\.billing_invoices`).
		WithArgs("tenant-1", 10).
		WillReturnRows(rows)

	got, err := s.getRecentPayments(context.Background(), "tenant-1", 10)
	if err != nil {
		t.Fatalf("getRecentPayments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d payments, want 2", len(got))
	}
	if got[0].TxId != "tx-abc" || got[0].ConfirmedAt == nil {
		t.Fatalf("confirmed payment mapping wrong: %+v", got[0])
	}
	// NULL tx_id stays "" and NULL confirmed_at stays unset (nil timestamp).
	if got[1].TxId != "" || got[1].ConfirmedAt != nil {
		t.Fatalf("pending payment should have empty tx + nil confirmed_at: %+v", got[1])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetSubscriptionPeriodActiveRow(t *testing.T) {
	s, mock := newReadServer(t, true)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions\s+WHERE tenant_id = \$1 AND status = 'active'`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end"}).
			AddRow(start, end))

	gotStart, gotEnd := s.getSubscriptionPeriod(context.Background(), "tenant-1", time.Now())
	if !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("active period not returned: %v..%v", gotStart, gotEnd)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetSubscriptionPeriodFallsBackToMonth(t *testing.T) {
	s, mock := newReadServer(t, true)
	// No active subscription row: must fall back to the calendar month rather
	// than hard-fail period resolution.
	mock.ExpectQuery(`FROM purser\.tenant_subscriptions`).
		WithArgs("tenant-1").
		WillReturnError(sqlmockNoRows())

	now := time.Date(2026, 3, 20, 8, 0, 0, 0, time.UTC)
	gotStart, gotEnd := s.getSubscriptionPeriod(context.Background(), "tenant-1", now)
	wantStart := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !gotStart.Equal(wantStart) || !gotEnd.Equal(wantEnd) {
		t.Fatalf("month fallback wrong: %v..%v", gotStart, gotEnd)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetPendingInvoicesMapsRowsAndLineItems(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	invoiceRows := sqlmock.NewRows([]string{
		"id", "tenant_id", "amount", "base_amount", "metered_amount",
		"prepaid_credit_applied", "currency", "status", "due_date", "paid_at",
		"usage_details", "created_at", "updated_at", "period_start", "period_end",
		"gross_metered_amount",
	}).AddRow(
		"inv-1", "tenant-1", 30.0, 20.0, 10.0,
		0.0, "USD", "pending", now, nil,
		[]byte(`{"k":"v"}`), now, now, now, now,
		10.0,
	)
	mock.ExpectQuery(`FROM purser\.billing_invoices\s+WHERE tenant_id = \$1 AND status IN`).
		WithArgs("tenant-1").
		WillReturnRows(invoiceRows)

	// loadInvoiceLineItems runs once per invoice. quartermasterClient is nil so
	// enrichment is skipped.
	lineRows := sqlmock.NewRows([]string{
		"line_key", "meter", "description", "quantity", "included_quantity",
		"billable_quantity", "unit_price", "amount", "currency",
		"cluster_id", "cluster_kind", "pricing_source",
	}).AddRow(
		"base_subscription", "", "Base plan", "1", "0",
		"1", "20.00", "20.00", "USD",
		"", "", "tier",
	)
	mock.ExpectQuery(`FROM purser\.invoice_line_items\s+WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("inv-1", "tenant-1").
		WillReturnRows(lineRows)

	got, err := s.getPendingInvoices(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("getPendingInvoices: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d invoices, want 1", len(got))
	}
	inv := got[0]
	if inv.Id != "inv-1" || inv.Amount != 30.0 || inv.BaseAmount != 20.0 || inv.MeteredAmount != 10.0 {
		t.Fatalf("invoice amounts mapped wrong: %+v", inv)
	}
	// NULL paid_at must stay unset.
	if inv.PaidAt != nil {
		t.Fatalf("unpaid invoice should have nil paid_at: %+v", inv.PaidAt)
	}
	if inv.UsageDetails == nil || inv.UsageDetails.GetFields()["k"].GetStringValue() != "v" {
		t.Fatalf("usage_details JSONB not decoded: %+v", inv.UsageDetails)
	}
	if len(inv.LineItems) != 1 || inv.LineItems[0].LineKey != "base_subscription" {
		t.Fatalf("line items not attached: %+v", inv.LineItems)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetUsageRecordsRequiresTimeRange(t *testing.T) {
	// A bare context is a service call, so the time_range guard (not the tenant
	// guard) is what rejects this request.
	s := newGuardServer(t)
	_, err := s.GetUsageRecords(context.Background(), &purserpb.GetUsageRecordsRequest{TenantId: "tenant-1"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for missing time_range, got %v", err)
	}
}

func TestGetUsageRecordsMapsRows(t *testing.T) {
	s, mock := newReadServer(t, false)
	now := time.Now()
	start := timestamppb.New(now.Add(-time.Hour))
	end := timestamppb.New(now)

	rows := sqlmock.NewRows([]string{
		"id", "tenant_id", "cluster_id", "usage_type", "usage_value",
		"usage_details", "created_at", "period_start", "period_end", "granularity",
	}).
		AddRow("u1", "tenant-1", "cl-1", "delivered_minutes", 12.5, []byte(`{"codec":"h264"}`), now, now, now, "minute_5").
		AddRow("u2", "tenant-1", nil, "egress_gb", 3.0, nil, now, nil, nil, nil)

	// args: tenantID, period-range end, period-range start, limit+1.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(rows)

	resp, err := s.GetUsageRecords(context.Background(), &purserpb.GetUsageRecordsRequest{
		TenantId:  "tenant-1",
		TimeRange: &commonpb.TimeRange{Start: start, End: end},
	})
	if err != nil {
		t.Fatalf("GetUsageRecords: %v", err)
	}
	if len(resp.UsageRecords) != 2 {
		t.Fatalf("got %d records, want 2", len(resp.UsageRecords))
	}
	r0 := resp.UsageRecords[0]
	if r0.ClusterId != "cl-1" || r0.UsageValue != 12.5 {
		t.Fatalf("record0 mapped wrong: %+v", r0)
	}
	if r0.UsageDetails == nil || r0.UsageDetails.GetFields()["codec"].GetStringValue() != "h264" {
		t.Fatalf("usage_details JSONB not decoded: %+v", r0.UsageDetails)
	}
	// NULL cluster_id stays empty; NULL period bounds stay unset.
	r1 := resp.UsageRecords[1]
	if r1.ClusterId != "" || r1.PeriodStart != nil || r1.PeriodEnd != nil {
		t.Fatalf("record1 null handling wrong: %+v", r1)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestGetBillingTierNotFound(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE id = \$1`).
		WithArgs("missing").
		WillReturnError(sqlmockNoRows())

	_, err := s.GetBillingTier(context.Background(), &purserpb.GetBillingTierRequest{TierId: "missing"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestGetBillingTierRequiresTierID(t *testing.T) {
	s, _ := newReadServer(t, true)
	_, err := s.GetBillingTier(context.Background(), &purserpb.GetBillingTierRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestGetBillingTierMapsRowAndSubqueries(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	tierRow := sqlmock.NewRows([]string{
		"id", "tier_name", "display_name", "description", "base_price", "currency", "billing_period",
		"features", "support_level", "sla_level", "metering_enabled",
		"is_active", "tier_level", "is_enterprise",
		"created_at", "updated_at",
		"is_default_prepaid", "is_default_postpaid",
		"processes_live", "processes_dvr", "processes_clip", "processes_dvr_finalize", "processes_vod",
	}).AddRow(
		"tier-1", "pro", "Pro", "Pro plan", 49.0, "USD", "monthly",
		[]byte(`{"recording":true,"analytics":true}`), "priority", "gold", true,
		true, int32(3), false,
		now, now,
		true, false,
		"live+", nil, nil, nil, nil,
	)
	mock.ExpectQuery(`FROM purser\.billing_tiers\s+WHERE id = \$1`).
		WithArgs("tier-1").
		WillReturnRows(tierRow)
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules\s+WHERE tier_id = \$1`).
		WithArgs("tier-1").
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included", "unit_price", "config"}).
			AddRow("delivered_minutes", "per_unit", "USD", "100", "0.01", "{}"))
	mock.ExpectQuery(`FROM purser\.tier_entitlements WHERE tier_id = \$1`).
		WithArgs("tier-1").
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}).
			AddRow("recording_retention_days", "30"))

	got, err := s.GetBillingTier(context.Background(), &purserpb.GetBillingTierRequest{TierId: "tier-1"})
	if err != nil {
		t.Fatalf("GetBillingTier: %v", err)
	}
	if got.Id != "tier-1" || got.TierName != "pro" || got.BasePrice != 49.0 || got.TierLevel != 3 {
		t.Fatalf("tier identity mapped wrong: %+v", got)
	}
	if got.Features == nil || !got.Features.Recording || !got.Features.Analytics {
		t.Fatalf("features JSONB not decoded: %+v", got.Features)
	}
	// NULL process-mode columns stay empty strings.
	if got.ProcessesLive != "live+" || got.ProcessesDvr != "" {
		t.Fatalf("process modes mapped wrong: live=%q dvr=%q", got.ProcessesLive, got.ProcessesDvr)
	}
	if len(got.PricingRules) != 1 || got.PricingRules[0].Meter != "delivered_minutes" {
		t.Fatalf("pricing rules not attached: %+v", got.PricingRules)
	}
	if got.Entitlements["recording_retention_days"] != "30" {
		t.Fatalf("entitlements not attached: %+v", got.Entitlements)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
