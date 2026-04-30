package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/rating"
	"frameworks/pkg/models"
)

// buildRatingInputFromSummary turns a single UsageSummary into a rating.Input.
// Used by per-event prepaid deduction. BasePrice is zero; the per-event
// deduction never re-charges the monthly base fee.
func buildRatingInputFromSummary(summary models.UsageSummary, tier *billing.EffectiveTier) rating.Input {
	usage := map[rating.Meter]decimal.Decimal{
		rating.MeterDeliveredMinutes: decimal.NewFromFloat(summary.ViewerHours).Mul(decimal.NewFromInt(60)),
		rating.MeterAverageStorageGB: decimal.NewFromFloat(summary.AverageStorageGB),
		rating.MeterAIGPUHours:       decimal.NewFromFloat(summary.GPUHours),
	}
	codecSeconds := codecSecondsFromSummary(summary)

	return rating.Input{
		Currency:     tier.Currency,
		BasePrice:    decimal.Zero, // per-event path: never charge base subscription
		Rules:        tier.Rules,
		Usage:        usage,
		CodecSeconds: codecSeconds,
	}
}

// codecSecondsFromSummary sums Livepeer + native_av seconds per codec from a
// single UsageSummary record.
func codecSecondsFromSummary(s models.UsageSummary) map[string]decimal.Decimal {
	totals := map[string]float64{
		"h264": s.LivepeerH264Seconds + s.NativeAvH264Seconds,
		"hevc": s.LivepeerHEVCSeconds + s.NativeAvHEVCSeconds,
		"vp9":  s.LivepeerVP9Seconds + s.NativeAvVP9Seconds,
		"av1":  s.LivepeerAV1Seconds + s.NativeAvAV1Seconds,
		"aac":  s.NativeAvAACSeconds,
		"opus": s.NativeAvOpusSeconds,
	}
	out := map[string]decimal.Decimal{}
	for codec, total := range totals {
		if total > 0 {
			out[codec] = decimal.NewFromFloat(total)
		}
	}
	return out
}

// buildRatingInputFromAggregates turns aggregated usage values (e.g. from
// purser.usage_records) into a rating.Input. BasePrice is the tier's monthly
// fee; this path is for invoice generation.
func buildRatingInputFromAggregates(usageData map[string]float64, tier *billing.EffectiveTier) rating.Input {
	viewerHours := decimal.NewFromFloat(usageData["viewer_hours"])
	usage := map[rating.Meter]decimal.Decimal{
		rating.MeterDeliveredMinutes: viewerHours.Mul(decimal.NewFromInt(60)),
		rating.MeterAverageStorageGB: decimal.NewFromFloat(usageData["average_storage_gb"]),
		rating.MeterAIGPUHours:       decimal.NewFromFloat(usageData["gpu_hours"]),
	}
	codecSeconds := codecSecondsFromAggregates(usageData)

	basePrice := decimal.Zero
	if tier.MeteringEnabled || !tier.BasePrice.IsZero() {
		basePrice = tier.BasePrice
	}

	return rating.Input{
		Currency:     tier.Currency,
		BasePrice:    basePrice,
		Rules:        tier.Rules,
		Usage:        usage,
		CodecSeconds: codecSeconds,
	}
}

// codecSecondsFromAggregates sums livepeer_<codec>_seconds and
// native_av_<codec>_seconds into a single per-codec map.
func codecSecondsFromAggregates(usageData map[string]float64) map[string]decimal.Decimal {
	codecs := []string{"h264", "hevc", "vp9", "av1", "aac", "opus"}
	out := map[string]decimal.Decimal{}
	for _, c := range codecs {
		total := usageData["livepeer_"+c+"_seconds"] + usageData["native_av_"+c+"_seconds"]
		if total > 0 {
			out[c] = decimal.NewFromFloat(total)
		}
	}
	return out
}

// persistInvoiceLineItems upserts every line in the rating result onto the
// invoice. Each row is keyed by (invoice_id, line_key); the UNIQUE index makes
// re-rating a draft idempotent. tenantID is denormalized into every row so
// financial-audit reads can filter by tenant per the cross-service tenant rule.
// Finalized invoices are guarded at the call site (the writer must check
// status before upserting).
func persistInvoiceLineItems(ctx context.Context, db dbExec, invoiceID, tenantID string, result rating.Result) error {
	if invoiceID == "" {
		return errors.New("persistInvoiceLineItems: empty invoice_id")
	}
	if tenantID == "" {
		return errors.New("persistInvoiceLineItems: empty tenant_id")
	}
	all := append([]rating.LineItem{result.BaseLine}, result.UsageLines...)

	// Track desired keys to delete obsolete rows from prior runs (e.g. a meter
	// that previously had usage but no longer does).
	desired := make(map[string]bool, len(all))
	for _, li := range all {
		desired[li.LineKey] = true
		meter := sql.NullString{}
		if li.Meter != "" {
			meter = sql.NullString{String: string(li.Meter), Valid: true}
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.invoice_line_items (
				invoice_id, tenant_id, line_key, meter, description,
				quantity, included_quantity, billable_quantity,
				unit_price, amount, currency, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
			ON CONFLICT (invoice_id, line_key) DO UPDATE SET
				meter = EXCLUDED.meter,
				description = EXCLUDED.description,
				quantity = EXCLUDED.quantity,
				included_quantity = EXCLUDED.included_quantity,
				billable_quantity = EXCLUDED.billable_quantity,
				unit_price = EXCLUDED.unit_price,
				amount = EXCLUDED.amount,
				currency = EXCLUDED.currency,
				updated_at = NOW()
		`, invoiceID, tenantID, li.LineKey, meter, li.Description,
			li.Quantity.String(), li.IncludedQuantity.String(), li.BillableQuantity.String(),
			li.UnitPrice.String(), li.Amount.Round(2).String(), li.Currency); err != nil {
			return fmt.Errorf("upsert line %q: %w", li.LineKey, err)
		}
	}

	// Sweep rows that aren't in the desired set anymore. Tenant-filtered as
	// belt-and-braces against any future cross-tenant invoice id mishap.
	rows, err := db.QueryContext(ctx,
		`SELECT line_key FROM purser.invoice_line_items WHERE invoice_id = $1 AND tenant_id = $2`,
		invoiceID, tenantID,
	)
	if err != nil {
		return fmt.Errorf("list existing line keys: %w", err)
	}
	defer rows.Close()
	var stale []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return fmt.Errorf("scan line key: %w", err)
		}
		if !desired[key] {
			stale = append(stale, key)
		}
	}
	if err := rows.Err(); err != nil {
		// Don't issue DELETEs from a possibly-truncated stale set; that
		// would leave obsolete line items in place.
		return fmt.Errorf("iterate line keys: %w", err)
	}
	for _, key := range stale {
		if _, err := db.ExecContext(ctx,
			`DELETE FROM purser.invoice_line_items WHERE invoice_id = $1 AND tenant_id = $2 AND line_key = $3`,
			invoiceID, tenantID, key,
		); err != nil {
			return fmt.Errorf("delete stale line %q: %w", key, err)
		}
	}
	return nil
}

// dbExec is the subset of *sql.DB / *sql.Tx persistInvoiceLineItems uses.
type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// withTx runs fn inside a single SQL transaction, committing on nil error and
// rolling back otherwise. Used by invoice writes so totals and line items move
// together. A failed line-item upsert undoes the invoice header write.
func withTx(ctx context.Context, db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("rollback failed (%w) after error: %w", rbErr, err)
		}
		return err
	}
	return tx.Commit()
}
