package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	billingmollie "frameworks/api_billing/internal/mollie"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

type sqlArgFunc func(driver.Value) bool

func (f sqlArgFunc) Match(v driver.Value) bool {
	return f(v)
}

func TestValidateUsageRecordAllowsCustomCanonicalMeter(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)

	got := validateUsageRecord("ai_transcription_seconds", 180, start, end, "minute_5", "delta")
	if got != "" {
		t.Fatalf("validateUsageRecord custom meter = %q, want accepted", got)
	}
}

func TestValidateUsageRecordRejectsMalformedMeter(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)

	got := validateUsageRecord("Bad-Meter", 180, start, end, "minute_5", "delta")
	if got != "invalid_meter" {
		t.Fatalf("validateUsageRecord malformed meter = %q, want invalid_meter", got)
	}
}

func TestValidateUsageRecordRequiresDeltaForPriceableMetrics(t *testing.T) {
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)

	got := validateUsageRecord("egress_gb", 3.5, start, end, "hourly", "gauge")
	if got != "value_kind_mismatch" {
		t.Fatalf("validateUsageRecord egress gauge = %q, want value_kind_mismatch", got)
	}
}

func TestValidateUsageRecordRequiresCanonicalWindowForOperationalMeters(t *testing.T) {
	got := validateUsageRecord("max_viewers", 42, time.Time{}, time.Time{}, "hourly", "gauge")
	if got != "missing_period" {
		t.Fatalf("validateUsageRecord operational gauge = %q, want missing_period", got)
	}
}

func TestBuildUsageDataFromSummaryIncludesGenericMeters(t *testing.T) {
	got := buildUsageDataFromSummary(models.UsageSummary{
		Meters: map[string]float64{
			"ai_detection_seconds": 42,
			"egress_gb":            99,
		},
		EgressGB: 12,
	})
	if got["ai_detection_seconds"] != 42 {
		t.Fatalf("ai_detection_seconds = %v, want 42", got["ai_detection_seconds"])
	}
	if got["egress_gb"] != 12 {
		t.Fatalf("egress_gb = %v, want typed field to win over generic duplicate", got["egress_gb"])
	}
}

func TestProcessUsageSummaryPersistsProviderUsageAndAdjustments(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	summary := models.UsageSummary{
		TenantID:  "00000000-0000-0000-0000-000000000001",
		ClusterID: "cluster-a",
		Period:    start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339),
		Timestamp: start,
		StorageProviderUsage: []models.StorageProviderUsage{{
			CustomerClusterID:        "cluster-a",
			StorageProviderTenantID:  "provider-tenant",
			StorageProviderClusterID: "provider-cluster",
			StorageBackend:           "edge_disk",
			StorageScope:             "hot",
			UsageType:                "storage_gb_seconds_hot",
			GBSeconds:                300,
		}},
		UsageAdjustments: []models.UsageAdjustment{{
			SourceSystem: "periscope.projection_divergences",
			SourceID:     "storage-correction-1",
			UsageType:    "storage_gb_seconds_hot",
			ClusterID:    "cluster-a",
			DeltaValue:   -60,
			PeriodStart:  start,
			PeriodEnd:    end,
			Reason:       "projection_divergence",
			Details:      models.JSONB{"window_start": start.Format(time.RFC3339)},
		}},
	}

	mock.ExpectExec(`INSERT INTO purser\.storage_provider_usage_records`).
		WithArgs(summary.TenantID, "cluster-a", "provider-tenant", "provider-cluster", "edge_disk", "hot", "storage_gb_seconds_hot", 300.0, start, end, "kafka-test", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.usage_adjustments`).
		WithArgs(summary.TenantID, "cluster-a", "storage_gb_seconds_hot", -60.0, start, end, "periscope.projection_divergences", "storage-correction-1", "projection_divergence", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if _, err := jm.processUsageSummary(context.Background(), summary, "kafka-test"); err != nil {
		t.Fatalf("processUsageSummary: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestProcessUsageSummaryPersistsCanonicalDeltaMeters(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()
	mock.MatchExpectationsInOrder(false)

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	summary := models.UsageSummary{
		TenantID:             "00000000-0000-0000-0000-000000000001",
		ClusterID:            "cluster-a",
		Period:               start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339),
		ViewerHours:          1.0 / 60.0,
		EgressGB:             2.5,
		StorageGBSecondsCold: 300,
		NativeAvAACSeconds:   60,
		Timestamp:            start,
	}

	for usageType, usageValue := range map[string]float64{
		"delivered_minutes":       1,
		"egress_gb":               2.5,
		"storage_gb_seconds_cold": 300,
		"media_seconds":           60,
	} {
		mock.ExpectExec(`INSERT INTO purser\.usage_records`).
			WithArgs(summary.TenantID, "cluster-a", usageType, usageValue, sqlmock.AnyArg(), start, end, "minute_5", "delta").
			WillReturnResult(sqlmock.NewResult(0, 1))
	}

	if _, err := jm.processUsageSummary(context.Background(), summary, "kafka-test"); err != nil {
		t.Fatalf("processUsageSummary: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestProcessUsageSummaryQuarantinesMissingClusterID(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	summary := models.UsageSummary{
		TenantID:    "00000000-0000-0000-0000-000000000001",
		Period:      start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339),
		ViewerHours: 1.0 / 60.0,
		Timestamp:   start,
	}

	mock.ExpectExec(`INSERT INTO purser\.usage_records_quarantine`).
		WithArgs(summary.TenantID, "", "delivered_minutes", 1.0, sqlmock.AnyArg(), start, end, "minute_5", "delta", "missing_cluster_id", "kafka-test", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))

	accepted, err := jm.processUsageSummary(context.Background(), summary, "kafka-test")
	if err != nil {
		t.Fatalf("processUsageSummary: %v", err)
	}
	if len(accepted) != 0 {
		t.Fatalf("expected no accepted usage rows, got %#v", accepted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestBuildRatingInputFromCanonicalUsageUsesAcceptedRowsOnly(t *testing.T) {
	rows := []canonicalUsageDelta{
		{
			usageType:  "delivered_minutes",
			usageValue: 2,
		},
		{
			usageType:  "media_seconds",
			usageValue: 60,
			usageDetails: models.JSONB{
				"codec_seconds": map[string]float64{"AV:opus": 60, "opus": 60},
			},
		},
	}

	got := buildRatingInputFromCanonicalUsage(rows, "EUR", nil)
	if got.Usage["delivered_minutes"].String() != "2" {
		t.Fatalf("delivered_minutes = %s, want 2", got.Usage["delivered_minutes"])
	}
	if got.Usage["media_seconds"].String() != "60" {
		t.Fatalf("media_seconds = %s, want 60", got.Usage["media_seconds"])
	}
	if got.CodecSeconds["AV:opus"].String() != "60" || got.CodecSeconds["opus"].String() != "60" {
		t.Fatalf("codec seconds not preserved: %#v", got.CodecSeconds)
	}
}

func TestCollectInvoiceUsageAggregatesRows(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	// Rows carry cluster_id; unattributed rows arrive as "".
	// Two distinct clusters split the same meter to verify partitioning.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "storage_gb_seconds_hot", 2.5).
			AddRow("cluster-a", "delivered_minutes", 180.0).
			AddRow("cluster-b", "delivered_minutes", 90.0))

	got, err := jm.collectInvoiceUsage(context.Background(), "tenant-1", start, end)
	if err != nil {
		t.Fatalf("collectInvoiceUsage: %v", err)
	}
	if got[""]["storage_gb_seconds_hot"] != 2.5 {
		t.Errorf("unattributed bucket missing storage_gb_seconds_hot: %v", got[""])
	}
	if got["cluster-a"]["delivered_minutes"] != 180.0 {
		t.Errorf("cluster-a delivered_minutes = %v, want 180.0", got["cluster-a"]["delivered_minutes"])
	}
	if got["cluster-b"]["delivered_minutes"] != 90.0 {
		t.Errorf("cluster-b delivered_minutes = %v, want 90.0", got["cluster-b"]["delivered_minutes"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestCollectInvoiceUsageRowsErrorFailsClosed(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs("tenant-1", start, end).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "delivered_minutes", 180.0).
			RowError(0, errors.New("cursor failed")))

	_, err = jm.collectInvoiceUsage(context.Background(), "tenant-1", start, end)
	if err == nil {
		t.Fatalf("collectInvoiceUsage err = nil, want cursor failure")
	}
	if !strings.Contains(err.Error(), "usage row") && !strings.Contains(err.Error(), "usage rows") {
		t.Fatalf("collectInvoiceUsage err = %v, want usage row context", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestUpdateInvoiceDraftWritesRatedLineItemsTransactionally(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-1"
	tierID := "tier-1"
	subscriptionID := "sub-1"
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	currency := billing.DefaultCurrency()

	mock.ExpectQuery(`SELECT bt\.id, bt\.tier_name, bt\.base_price::text`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).
			AddRow(tierID, "supporter", "100.00", currency, true, subscriptionID))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("storage_gb_seconds_hot", "all_usage", currency, "0", "1.00", "{}"))
	mock.ExpectQuery(`FROM purser\.subscription_pricing_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`FROM purser\.tier_entitlements`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery(`FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	mock.ExpectQuery(`SELECT billing_period_start, billing_period_end`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end", "mollie_next_payment_date"}).
			AddRow(periodStart, periodEnd, nil))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM purser\.billing_invoices`).
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		// New per-cluster shape: rows carry cluster_id. Empty cluster_id
		// resolves through platform-official tier pricing.
	// 7200 GiB-seconds = 2 GiB-hours under the GiB-seconds→GiB-hour
	// rating conversion. The tier prices hot at $1/GiB-hour, so the
	// resulting metered line is $2.00 — same as before, but the
	// underlying ledger value reflects the canonical unit.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "storage_gb_seconds_hot", 7200.0))
	mock.ExpectQuery(`usage_details->'codec_seconds'`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "key", "seconds"}))
	mock.ExpectQuery(`SELECT stripe_subscription_id, mollie_subscription_id\s+FROM purser\.tenant_subscriptions`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"stripe_subscription_id", "mollie_subscription_id"}).
			AddRow(nil, nil))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(-amount_cents\), 0\)`).
		WithArgs(tenantID, "Invoice credit: 2026-04").
		WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(int64(0)))
	mock.ExpectExec(`INSERT INTO purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT balance_cents FROM purser\.prepaid_balances`).
		WithArgs(tenantID, currency).
		WillReturnRows(sqlmock.NewRows([]string{"balance_cents"}).AddRow(int64(0)))
	mock.ExpectQuery(`INSERT INTO purser\.billing_invoices`).
		WithArgs(tenantID, "102", currency, sqlmock.AnyArg(), "100", "2", "0", sqlmock.AnyArg(), periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("invoice-1"))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}).
			AddRow("base_subscription").
			AddRow("meter:storage_gb_seconds_hot"))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET billing_period_start = COALESCE`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestUpdateInvoiceDraftClampsPriorPrepaidCreditToZeroNet(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	tenantID := "tenant-1"
	tierID := "tier-1"
	subscriptionID := "sub-1"
	periodStart := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	currency := billing.DefaultCurrency()

	mock.ExpectQuery(`SELECT bt\.id, bt\.tier_name, bt\.base_price::text`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tier_name", "base_price", "currency", "metering_enabled", "subscription_id"}).
			AddRow(tierID, "supporter", "100.00", currency, true, subscriptionID))
	mock.ExpectQuery(`FROM purser\.tier_pricing_rules`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}).
			AddRow("storage_gb_seconds_hot", "all_usage", currency, "0", "1.00", "{}"))
	mock.ExpectQuery(`FROM purser\.subscription_pricing_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"meter", "model", "currency", "included_quantity", "unit_price", "config"}))
	mock.ExpectQuery(`FROM purser\.tier_entitlements`).
		WithArgs(tierID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))
	mock.ExpectQuery(`FROM purser\.subscription_entitlement_overrides`).
		WithArgs(subscriptionID).
		WillReturnRows(sqlmock.NewRows([]string{"key", "value"}))

	mock.ExpectQuery(`SELECT billing_period_start, billing_period_end`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"billing_period_start", "billing_period_end", "mollie_next_payment_date"}).
			AddRow(periodStart, periodEnd, nil))
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM purser\.billing_invoices`).
		WithArgs(tenantID, periodStart).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// 7200 GiB-seconds = 2 GiB-hours under the GiB-seconds→GiB-hour
	// rating conversion. Tier prices hot at $1/GiB-hour → $2 metered.
	mock.ExpectQuery(`FROM purser\.usage_records`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "aggregated_value"}).
			AddRow("", "storage_gb_seconds_hot", 7200.0))
	mock.ExpectQuery(`usage_details->'codec_seconds'`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"cluster_id", "usage_type", "key", "seconds"}))
	mock.ExpectQuery(`SELECT stripe_subscription_id, mollie_subscription_id\s+FROM purser\.tenant_subscriptions`).
		WithArgs(tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"stripe_subscription_id", "mollie_subscription_id"}).
			AddRow(nil, nil))
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(-amount_cents\), 0\)`).
		WithArgs(tenantID, "Invoice credit: 2026-04").
		WillReturnRows(sqlmock.NewRows([]string{"applied"}).AddRow(int64(20_000)))
	mock.ExpectQuery(`INSERT INTO purser\.billing_invoices`).
		WithArgs(tenantID, "0", currency, sqlmock.AnyArg(), "100", "2", "200", sqlmock.AnyArg(), periodStart, periodEnd).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("invoice-1"))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.invoice_line_items`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT line_key FROM purser\.invoice_line_items WHERE invoice_id = \$1 AND tenant_id = \$2`).
		WithArgs("invoice-1", tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"line_key"}).
			AddRow("base_subscription").
			AddRow("meter:storage_gb_seconds_hot"))
	mock.ExpectExec(`UPDATE purser\.tenant_subscriptions\s+SET billing_period_start = COALESCE`).
		WithArgs(tenantID, periodStart, periodEnd).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if err := jm.updateInvoiceDraft(context.Background(), tenantID); err != nil {
		t.Fatalf("updateInvoiceDraft: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet sqlmock expectations: %v", err)
	}
}

func TestChargeMollieOverageCreatesLocalPaymentBeforeProviderCharge(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	jm := &JobManager{db: mockDB, logger: logging.NewLogger()}
	mc, err := billingmollie.NewClient(billingmollie.Config{APIKey: "test_unused", Logger: logging.NewLogger()})
	if err != nil {
		t.Fatalf("mollie client: %v", err)
	}
	mollieClient = mc
	t.Cleanup(func() { mollieClient = nil })

	var inserted bool
	var localPaymentID string
	withDefaultTransport(t, testRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if !inserted {
			t.Fatal("provider charge happened before local billing_payment insert")
		}
		if req.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", req.Method)
		}
		if !strings.Contains(req.URL.Path, "/v2/customers/cst_123/payments") {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		if got := req.Header.Get("Idempotency-Key"); got != "mollie-overage:invoice-1:1" {
			t.Fatalf("idempotency key = %q, want mollie-overage:invoice-1:1", got)
		}

		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if got := body["sequenceType"]; got != "recurring" {
			t.Fatalf("sequenceType = %v, want recurring", got)
		}
		if got := body["mandateId"]; got != "mdt_123" {
			t.Fatalf("mandateId = %v, want mdt_123", got)
		}
		metadata, ok := body["metadata"].(map[string]any)
		if !ok {
			t.Fatalf("metadata missing or wrong type: %#v", body["metadata"])
		}
		if got := metadata["invoice_id"]; got != "invoice-1" {
			t.Fatalf("invoice_id = %v, want invoice-1", got)
		}
		if got := metadata["billing_payment_id"]; got != localPaymentID {
			t.Fatalf("billing_payment_id = %v, want %s", got, localPaymentID)
		}

		return newJSONResponse(http.StatusCreated, `{
			"resource":"payment",
			"id":"tr_overage",
			"mode":"test",
			"createdAt":"2026-05-12T10:00:00+00:00",
			"amount":{"value":"12.34","currency":"EUR"},
			"description":"Usage overage for invoice invoice-1",
			"method":"creditcard",
			"metadata":{},
			"status":"open",
			"sequenceType":"recurring",
			"_links":{"self":{"href":"https://api.mollie.com/v2/payments/tr_overage","type":"application/hal+json"}}
		}`), nil
	}))

	mock.ExpectQuery(`SELECT mc\.mollie_customer_id`).
		WithArgs("tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"mollie_customer_id", "mollie_mandate_id", "mollie_mandate_status"}).
			AddRow("cst_123", "mdt_123", "valid"))
	mock.ExpectQuery(`SELECT bpa\.attempt_number, bpa\.status\s+FROM purser\.billing_payment_attempts bpa`).
		WithArgs("mollie", "invoice-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO purser\.billing_payments`).
		WithArgs(
			sqlArgFunc(func(v driver.Value) bool {
				localPaymentID, _ = v.(string)
				return localPaymentID != ""
			}),
			"invoice-1",
			"12.34",
			"EUR",
			sqlArgFunc(func(v driver.Value) bool {
				inserted = true
				intentID, _ := v.(string)
				return intentID == "mollie-overage-intent:"+localPaymentID
			}),
		).
		WillReturnRows(sqlmock.NewRows([]string{"tx_id", "status"}).AddRow("", "pending"))
	mock.ExpectQuery(`INSERT INTO purser\.payment_provider_intents`).
		WithArgs("tenant-1", "invoice-1", "cst_123", "EUR", int64(1234), "mollie-overage:invoice-1:1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("00000000-0000-0000-0000-000000000111"))
	mock.ExpectExec(`UPDATE purser\.billing_payments SET intent_id`).
		WithArgs("00000000-0000-0000-0000-000000000111", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO purser\.billing_payment_attempts`).
		WithArgs(sqlmock.AnyArg(), "00000000-0000-0000-0000-000000000111", 1, "mollie-overage:invoice-1:1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs(sqlmock.AnyArg(), 1).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`UPDATE purser\.billing_payments\s+SET tx_id`).
		WithArgs("tr_overage", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.payment_provider_intents`).
		WithArgs("tr_overage", "00000000-0000-0000-0000-000000000111").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE purser\.billing_payment_attempts`).
		WithArgs("tr_overage", sqlmock.AnyArg(), 1).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := jm.chargeMollieOverage(context.Background(), "tenant-1", "invoice-1", decimal.NewFromFloat(12.34), "EUR"); err != nil {
		t.Fatalf("chargeMollieOverage: %v", err)
	}
	if !inserted {
		t.Fatal("billing_payment insert did not run")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}
