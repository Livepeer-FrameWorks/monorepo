//go:build schema_verify

package handlers

import (
	"context"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestInvoiceRatingRepository_RealPG(t *testing.T) { //nolint:funlen // One engine fixture proves aggregation and snapshot replacement together.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	tenantID := uuid.NewString()
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)
	dimensionKey := strings.Repeat("a", 64)
	for index, quantity := range []string{"2.000000", "3.000000"} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.usage_records (
				id, tenant_id, cluster_id, usage_type, unit, dimensions,
				dimension_key, source_id, report_id, usage_value, value_kind,
				period_start, period_end, granularity
			) VALUES (
				$1, $2, 'cluster-a', 'transcode_rendition_seconds', 'second',
				'{"output_codec":"h264"}'::jsonb, $3, $4, $5, $6::numeric,
				'delta', $7, $8, 'minute_5'
			)
		`, uuid.NewString(), tenantID, dimensionKey, "source-"+string(rune('a'+index)),
			strings.Repeat(string(rune('b'+index)), 64), quantity, periodStart, periodStart.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_adjustments (
			id, tenant_id, cluster_id, usage_type, unit, dimensions,
			dimension_key, delta_value, period_start, period_end,
			source_system, source_id
		) VALUES (
			$1, $2, 'cluster-a', 'transcode_rendition_seconds', 'second',
			'{"output_codec":"h264"}'::jsonb, $3, -1, $4, $5, 'contract', $6
		)
	`, uuid.NewString(), tenantID, dimensionKey, periodStart, periodStart.Add(5*time.Minute), uuid.NewString()); err != nil {
		t.Fatal(err)
	}

	jobs := &JobManager{db: db, billing: &Service{}}
	usage, err := jobs.collectInvoiceUsage(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		t.Fatal(err)
	}
	if usage["cluster-a"]["transcode_rendition_seconds"] != 4 {
		t.Fatalf("aggregated usage = %+v", usage)
	}
	dimensioned, err := jobs.collectInvoiceDimensionedUsage(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		t.Fatal(err)
	}
	if len(dimensioned["cluster-a"]) != 1 || !dimensioned["cluster-a"][0].Quantity.Equal(decimal.NewFromInt(4)) || dimensioned["cluster-a"][0].Dimensions["output_codec"] != "h264" {
		t.Fatalf("dimensioned usage = %+v", dimensioned)
	}

	ownerID := uuid.New()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.platform_fee_policy (
			id, cluster_kind, cluster_owner_tenant_id, pricing_source,
			fee_basis_points, effective_from
		) VALUES ($1, 'third_party_marketplace', $2, 'cluster_metered', 725, NOW())
	`, uuid.NewString(), ownerID); err != nil {
		t.Fatal(err)
	}
	bps, err := jobs.lookupPlatformFeeBps(ctx, ownerID, pricing.SourceClusterMetered)
	if err != nil || bps != 725 {
		t.Fatalf("platform fee bps = %d, %v", bps, err)
	}

	ratingResult := &clusterRatingResult{
		BaseLine: pricedLine{LineItem: rating.LineItem{
			LineKey: rating.LineKeyBaseSubscription, Description: "Base subscription",
			Quantity: decimal.NewFromInt(1), BillableQuantity: decimal.NewFromInt(1),
			UnitPrice: decimal.NewFromInt(10), Amount: decimal.NewFromInt(10), Currency: "EUR",
		}, PricingSource: pricing.SourceTier},
		UsageLines: []pricedLine{{
			LineItem: rating.LineItem{
				LineKey: "meter:transcode:cluster-a", Meter: rating.Meter("transcode_rendition_seconds"),
				Unit: "second", Dimensions: map[string]string{"output_codec": "h264"},
				Description: "H.264 transcode", Quantity: decimal.NewFromInt(4),
				BillableQuantity: decimal.NewFromInt(4), UnitPrice: decimal.NewFromFloat(0.25),
				Amount: decimal.NewFromInt(1), Currency: "EUR",
			}, ClusterID: stringPointer("cluster-a"), ClusterKind: stringPointer("third_party_marketplace"),
			ClusterOwnerTenantID: &ownerID, PricingSource: pricing.SourceClusterMetered,
			OperatorCreditCents: 93, PlatformFeeCents: 7,
		}},
		BaseAmount: decimal.NewFromInt(10), UsageAmount: decimal.NewFromInt(1),
		GrossUsageAmount: decimal.NewFromInt(1), TotalAmount: decimal.NewFromInt(11),
	}
	if err := jobs.persistManualReviewDraft(ctx, tenantID, periodStart, periodEnd, "EUR", ratingResult); err != nil {
		t.Fatalf("first manual-review snapshot: %v", err)
	}
	ratingResult.UsageLines = nil
	ratingResult.UsageAmount = decimal.Zero
	ratingResult.GrossUsageAmount = decimal.Zero
	ratingResult.TotalAmount = decimal.NewFromInt(10)
	if err := jobs.persistManualReviewDraft(ctx, tenantID, periodStart, periodEnd, "EUR", ratingResult); err != nil {
		t.Fatalf("replacement manual-review snapshot: %v", err)
	}

	var invoiceCount, lineCount int
	var dimensions string
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MIN(usage_details::text)
		FROM purser.billing_invoices
		WHERE tenant_id = $1 AND period_start = $2 AND status = 'manual_review'
	`, tenantID, periodStart).Scan(&invoiceCount, &dimensions); err != nil {
		t.Fatal(err)
	}
	if invoiceCount != 1 || dimensions != "{}" {
		t.Fatalf("invoice count/usage details = %d/%s", invoiceCount, dimensions)
	}
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.invoice_line_items WHERE tenant_id = $1
	`, tenantID).Scan(&lineCount); err != nil {
		t.Fatal(err)
	}
	if lineCount != 1 {
		t.Fatalf("replacement line count = %d, want only base line", lineCount)
	}
	var baseDimensions string
	if err := db.QueryRowContext(ctx, `
		SELECT dimensions::text FROM purser.invoice_line_items
		WHERE tenant_id = $1 AND line_key = 'base_subscription'
	`, tenantID).Scan(&baseDimensions); err != nil {
		t.Fatal(err)
	}
	if baseDimensions != "{}" {
		t.Fatalf("nil dimensions normalized to %s, want {}", baseDimensions)
	}
}

func stringPointer(value string) *string { return &value }
