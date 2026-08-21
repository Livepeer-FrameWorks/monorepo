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

	"frameworks/api_billing/internal/database/purserdb"
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

	invoiceUUID, err := uuid.Parse(invoiceID)
	if err != nil {
		return fmt.Errorf("stripe.EnqueueMeterEvents: invalid invoice_id %q: %w", invoiceID, err)
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("stripe.EnqueueMeterEvents: invalid tenant_id %q: %w", tenantID, err)
	}
	if err := purserdb.New(tx).EnqueueStripeMeterEvents(ctx, purserdb.EnqueueStripeMeterEventsParams{
		TenantID: tenantUUID, InvoiceID: invoiceUUID,
	}); err != nil {
		return fmt.Errorf("enqueue Stripe meter events: %w", err)
	}
	return nil
}
