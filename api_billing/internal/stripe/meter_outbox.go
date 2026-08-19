// EnqueueMeterEvents records the Stripe meter events that should be
// delivered for an invoice. The companion MeterFlusher in
// meter_flusher.go reads pending rows and pushes them to Stripe. A
// finalization rollback discards the row, preserving the at-most-once
// invariant per invoice attempt.
package stripe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// EnqueueMeterEvents inserts one outbox row per priced invoice line. The
// function is idempotent on (invoice_line_item_id, stripe_meter_event_name),
// so re-running finalization is a no-op without collapsing dimension buckets
// of the same meter into one event.
//
// Skipped when invoice is in manual_review (Decision 8: hard hold).
//
// Routes per pricing_source:
//   - cluster_metered → stripe_meter on cluster_pricing row
//   - cluster_custom  → stripe_meter on cluster_pricing row
//   - everything else → no meter event (no destination configured)
func EnqueueMeterEvents(ctx context.Context, tx *sql.Tx, invoiceID, tenantID, status string) error {
	if tx == nil {
		return errors.New("stripe.EnqueueMeterEvents: nil tx")
	}
	if invoiceID == "" {
		return errors.New("stripe.EnqueueMeterEvents: empty invoice_id")
	}
	if tenantID == "" {
		return errors.New("stripe.EnqueueMeterEvents: empty tenant_id")
	}
	if status == "manual_review" {
		return nil
	}

	// COALESCE the cluster_id so tenant-scoped lines (cluster_id IS NULL)
	// scan into a plain string without aborting finalization. Tenant-
	// scoped lines have no Stripe meter destination — the JOIN's CASE
	// returns NULL and the row is skipped below.
	rows, err := tx.QueryContext(ctx, `
		WITH lines AS (
			SELECT li.id, COALESCE(li.cluster_id, '') AS cluster_id,
			       COALESCE(li.meter, '') AS meter,
			       li.pricing_source, li.quantity, li.dimensions, li.amount, li.currency,
			       inv.period_start, inv.period_end
			FROM purser.invoice_line_items li
			JOIN purser.billing_invoices inv ON inv.id = li.invoice_id
			WHERE li.invoice_id = $1
			  AND li.tenant_id = $2
			  AND li.amount > 0
		)
		SELECT l.id, l.cluster_id, l.meter, l.pricing_source, l.quantity::text,
			       l.dimensions::text, l.period_start, l.period_end,
		       CASE l.pricing_source
		           WHEN 'cluster_metered' THEN COALESCE(cp.metered_rates -> l.meter ->> 'stripe_meter_event_name', cp.stripe_meter_event_name)
		           WHEN 'cluster_custom'  THEN COALESCE(cp.metered_rates -> l.meter ->> 'stripe_meter_event_name', cp.stripe_meter_event_name)
		           ELSE NULL
		       END AS stripe_meter_event_name
		FROM lines l
		LEFT JOIN purser.cluster_pricing cp ON cp.cluster_id = NULLIF(l.cluster_id, '')
	`, invoiceID, tenantID)
	if err != nil {
		return fmt.Errorf("query meter event candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type pending struct {
		LineID         uuid.UUID
		ClusterID      string
		Meter          string
		Quantity       string
		Dimensions     string
		PeriodStart    any
		PeriodEnd      any
		MeterEventName string
	}
	var queue []pending
	for rows.Next() {
		var (
			lineID, clusterID, meter, pricingSource, quantity string
			dimensions                                        string
			periodStart, periodEnd                            any
			meterEventName                                    sql.NullString
		)
		if err := rows.Scan(&lineID, &clusterID, &meter, &pricingSource, &quantity, &dimensions, &periodStart, &periodEnd, &meterEventName); err != nil {
			return fmt.Errorf("scan meter event row: %w", err)
		}
		_ = pricingSource
		if meter == "" || !meterEventName.Valid || meterEventName.String == "" {
			continue
		}
		liUUID, err := uuid.Parse(lineID)
		if err != nil {
			return fmt.Errorf("parse line_id %q: %w", lineID, err)
		}
		queue = append(queue, pending{
			LineID:         liUUID,
			ClusterID:      clusterID,
			Meter:          meter,
			Quantity:       quantity,
			Dimensions:     dimensions,
			PeriodStart:    periodStart,
			PeriodEnd:      periodEnd,
			MeterEventName: meterEventName.String,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate meter event rows: %w", err)
	}

	for _, p := range queue {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO purser.stripe_meter_events_outbox (
				tenant_id, cluster_id, meter, stripe_meter_event_name, quantity,
				dimensions, period_start, period_end, invoice_id, invoice_line_item_id
			) VALUES ($1, $2, $3, $4, $5::numeric, $6::jsonb, $7, $8, $9, $10)
			ON CONFLICT (invoice_line_item_id, stripe_meter_event_name) DO NOTHING
		`, tenantID, p.ClusterID, p.Meter, p.MeterEventName, p.Quantity, p.Dimensions,
			p.PeriodStart, p.PeriodEnd, invoiceID, p.LineID)
		if err != nil {
			return fmt.Errorf("insert meter event for line %s: %w", p.LineID, err)
		}
	}
	return nil
}
