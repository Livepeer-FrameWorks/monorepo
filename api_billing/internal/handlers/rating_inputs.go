package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

// buildRatingInputFromSummary turns a single UsageSummary into a rating.Input.
// Used by the per-event prepaid deduction path. BasePrice is zero; per-event
// deductions never re-charge the monthly base fee.
//
// rules is the rule set the rating engine should apply. For tenant-tier
// pricing it is tier.Rules; for cluster-aware pricing it is the result of
// pricing.ResolveClusterPricing for summary.ClusterID. The usage map is seeded
// from the summary fields Purser currently understands; rating itself stays
// rule-driven so new persisted usage_records can be priced without changing
// this helper.
func buildRatingInputFromSummary(summary models.UsageSummary, currency string, rules []rating.Rule) rating.Input {
	usage := map[rating.Meter]decimal.Decimal{}
	quantities := make([]rating.DimensionedQuantity, 0, len(summary.Meters)+len(summary.UsageAdjustments))
	for _, meter := range summary.Meters {
		dimensions := make(map[string]string, len(meter.Dimensions))
		for key, raw := range meter.Dimensions {
			if value, ok := raw.(string); ok {
				dimensions[key] = value
			}
		}
		quantities = append(quantities, rating.DimensionedQuantity{
			Meter: rating.Meter(meter.Meter), Unit: meter.Unit,
			Dimensions: dimensions, Quantity: decimal.NewFromFloat(meter.Quantity),
		})
	}
	for meter, value := range buildUsageDataFromSummary(summary) {
		m := rating.Meter(meter)
		if !rating.ValidMeter(m) {
			continue
		}
		usage[m] = decimal.NewFromFloat(value)
	}
	for _, adj := range summary.UsageAdjustments {
		m := rating.Meter(adj.UsageType)
		if !rating.ValidMeter(m) {
			continue
		}
		delta := decimal.NewFromFloat(adj.DeltaValue)
		usage[m] = usage[m].Add(delta)
		dimensions := make(map[string]string, len(adj.Dimensions))
		for key, raw := range adj.Dimensions {
			if value, ok := raw.(string); ok {
				dimensions[key] = value
			}
		}
		quantities = append(quantities, rating.DimensionedQuantity{
			Meter: m, Unit: adj.Unit, Dimensions: dimensions, Quantity: delta,
		})
	}

	return rating.Input{
		Currency:          currency,
		BasePrice:         decimal.Zero,
		Rules:             rules,
		Usage:             usage,
		Quantities:        quantities,
		WaiveUsageCharges: config.WaiveUsageChargesEnabled(),
	}
}

func buildRatingInputFromCanonicalUsage(rows []canonicalUsageDelta, currency string, rules []rating.Rule) rating.Input {
	usage := map[rating.Meter]decimal.Decimal{}
	quantities := make([]rating.DimensionedQuantity, 0, len(rows))
	for _, row := range rows {
		m := rating.Meter(row.usageType)
		if !rating.ValidMeter(m) {
			continue
		}
		delta := decimal.NewFromFloat(row.usageValue)
		usage[m] = usage[m].Add(delta)
		dimensions := make(map[string]string, len(row.dimensions))
		for key, raw := range row.dimensions {
			if value, ok := raw.(string); ok {
				dimensions[key] = value
			}
		}
		quantities = append(quantities, rating.DimensionedQuantity{
			Meter: m, Unit: row.unit, Dimensions: dimensions, Quantity: delta,
		})
	}
	return rating.Input{
		Currency:          currency,
		BasePrice:         decimal.Zero,
		Rules:             rules,
		Usage:             usage,
		Quantities:        quantities,
		WaiveUsageCharges: config.WaiveUsageChargesEnabled(),
	}
}

// persistInvoiceLineItems upserts every line in result onto the invoice. Each
// row is keyed by (invoice_id, line_key); the UNIQUE index makes re-rating a
// draft idempotent. tenantID is denormalized into every row so financial-audit
// reads can filter by tenant per the cross-service tenant rule. Finalized
// invoices are guarded at the call site.
//
// Cluster context (cluster_id, cluster_kind, cluster_owner_tenant_id,
// pricing_source, operator_credit_cents, platform_fee_cents,
// price_version_id) is written from the pricedLine fields. Tenant-scoped
// lines (base_subscription) write NULLs for the cluster columns.
func persistInvoiceLineItems(ctx context.Context, db dbExec, invoiceID, tenantID string, result *clusterRatingResult) error {
	if invoiceID == "" {
		return errors.New("persistInvoiceLineItems: empty invoice_id")
	}
	if tenantID == "" {
		return errors.New("persistInvoiceLineItems: empty tenant_id")
	}
	if result == nil {
		return errors.New("persistInvoiceLineItems: nil result")
	}
	all := append([]pricedLine{result.BaseLine}, result.UsageLines...)

	desired := make(map[string]bool, len(all))
	for _, pl := range all {
		desired[pl.LineKey] = true
		meter := sql.NullString{}
		if pl.Meter != "" {
			meter = sql.NullString{String: string(pl.Meter), Valid: true}
		}
		clusterID := sql.NullString{}
		if pl.ClusterID != nil {
			clusterID = sql.NullString{String: *pl.ClusterID, Valid: true}
		}
		clusterKind := sql.NullString{}
		if pl.ClusterKind != nil {
			clusterKind = sql.NullString{String: *pl.ClusterKind, Valid: true}
		}
		ownerID := sql.NullString{}
		if pl.ClusterOwnerTenantID != nil {
			ownerID = sql.NullString{String: pl.ClusterOwnerTenantID.String(), Valid: true}
		}
		versionID := sql.NullString{}
		if pl.PriceVersionID != nil {
			versionID = sql.NullString{String: pl.PriceVersionID.String(), Valid: true}
		}
		pricingSource := string(pl.PricingSource)
		if pricingSource == "" {
			pricingSource = string(pricing.SourceTier)
		}
		dimensionsJSON, err := json.Marshal(pl.Dimensions)
		if err != nil {
			return fmt.Errorf("marshal line dimensions %q: %w", pl.LineKey, err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.invoice_line_items (
				invoice_id, tenant_id, line_key, meter, unit, dimensions, description,
				quantity, included_quantity, billable_quantity,
				unit_price, amount, currency,
				cluster_id, cluster_kind, cluster_owner_tenant_id,
				pricing_source, operator_credit_cents, platform_fee_cents,
				price_version_id, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
				$14, $15, $16::uuid, $17, $18, $19, $20::uuid,
				NOW(), NOW()
			)
			ON CONFLICT (invoice_id, line_key) DO UPDATE SET
				meter = EXCLUDED.meter,
				unit = EXCLUDED.unit,
				dimensions = EXCLUDED.dimensions,
				description = EXCLUDED.description,
				quantity = EXCLUDED.quantity,
				included_quantity = EXCLUDED.included_quantity,
				billable_quantity = EXCLUDED.billable_quantity,
				unit_price = EXCLUDED.unit_price,
				amount = EXCLUDED.amount,
				currency = EXCLUDED.currency,
				cluster_id = EXCLUDED.cluster_id,
				cluster_kind = EXCLUDED.cluster_kind,
				cluster_owner_tenant_id = EXCLUDED.cluster_owner_tenant_id,
				pricing_source = EXCLUDED.pricing_source,
				operator_credit_cents = EXCLUDED.operator_credit_cents,
				platform_fee_cents = EXCLUDED.platform_fee_cents,
				price_version_id = EXCLUDED.price_version_id,
				updated_at = NOW()
		`, invoiceID, tenantID, pl.LineKey, meter, pl.Unit, dimensionsJSON, pl.Description,
			pl.Quantity.String(), pl.IncludedQuantity.String(), pl.BillableQuantity.String(),
			pl.UnitPrice.String(), pl.Amount.Round(2).String(), pl.Currency,
			clusterID, clusterKind, ownerID,
			pricingSource, pl.OperatorCreditCents, pl.PlatformFeeCents,
			versionID); err != nil {
			return fmt.Errorf("upsert line %q: %w", pl.LineKey, err)
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
// together.
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

// _ exists only to keep the billing import alive when build tags excise the
// rest of the file. Refer to billing.EffectiveTier for the resolver context.
var _ = billing.EffectiveTier{}
