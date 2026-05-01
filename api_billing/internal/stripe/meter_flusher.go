package stripe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	stripeapi "github.com/stripe/stripe-go/v82"
	stripemeter "github.com/stripe/stripe-go/v82/billing/meterevent"
)

// MeterFlusher reads pending stripe_meter_events_outbox rows and pushes
// them to Stripe via /v1/billing/meter_events. It is the delivery half of
// the at-least-once outbox pattern: EnqueueMeterEvents writes the row in
// the invoice finalization tx; this flusher reads and pushes after commit.
//
// Idempotency: each Stripe MeterEvent carries the outbox row's id as its
// identifier — Stripe enforces uniqueness within a 24h+ rolling window so
// retries collapse on the Stripe side.
//
// Retry policy: on failure increment attempt_count and stash last_error;
// the next tick re-reads (sent_at IS NULL) rows. After MaxAttempts the
// row is left for ops to inspect.
type MeterFlusher struct {
	DB             *sql.DB
	TenantStripeID func(ctx context.Context, tenantID string) (string, error)
	MaxAttempts    int
	BatchSize      int
}

// NewMeterFlusher returns a flusher with sensible defaults. tenantStripeID
// resolves a tenant's Stripe customer id from purser.tenant_subscriptions.
// stripeAPIKey is set via stripeapi.Key globally (the existing client.go
// already does this in production).
func NewMeterFlusher(db *sql.DB) *MeterFlusher {
	return &MeterFlusher{
		DB:          db,
		MaxAttempts: 6, // ~ exponential w/ a 5min base ≈ 5 hours of retries
		BatchSize:   100,
		TenantStripeID: func(ctx context.Context, tenantID string) (string, error) {
			var customerID sql.NullString
			err := db.QueryRowContext(ctx, `
				SELECT stripe_customer_id
				FROM purser.tenant_subscriptions
				WHERE tenant_id = $1 AND status = 'active'
				ORDER BY created_at DESC LIMIT 1
			`, tenantID).Scan(&customerID)
			if errors.Is(err, sql.ErrNoRows) || !customerID.Valid {
				return "", fmt.Errorf("no active subscription for tenant %s", tenantID)
			}
			if err != nil {
				return "", err
			}
			return customerID.String, nil
		},
	}
}

// Flush reads up to BatchSize pending rows and attempts delivery. Returns
// (sent, deferred, error). Errors at the level of individual rows are
// recorded on the row and counted as deferred; the function only returns
// an error when the read itself fails.
func (f *MeterFlusher) Flush(ctx context.Context) (sent, deferred int, err error) {
	if f.DB == nil {
		return 0, 0, errors.New("MeterFlusher.Flush: nil DB")
	}
	rows, err := f.DB.QueryContext(ctx, `
		SELECT id, tenant_id, cluster_id, meter, stripe_meter_event_name, quantity::text,
		       period_start, attempt_count
		FROM purser.stripe_meter_events_outbox
		WHERE sent_at IS NULL
		  AND attempt_count < $1
		ORDER BY created_at
		LIMIT $2
	`, f.MaxAttempts, f.BatchSize)
	if err != nil {
		return 0, 0, fmt.Errorf("query outbox: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type outboxRow struct {
		ID             string
		TenantID       string
		ClusterID      string
		Meter          string
		MeterEventName string
		Quantity       string
		PeriodStart    time.Time
		AttemptCount   int
	}
	var pending []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.ID, &r.TenantID, &r.ClusterID, &r.Meter, &r.MeterEventName, &r.Quantity, &r.PeriodStart, &r.AttemptCount); err != nil {
			return 0, 0, fmt.Errorf("scan outbox row: %w", err)
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate outbox rows: %w", err)
	}

	for _, r := range pending {
		if pushErr := f.pushOne(ctx, r.ID, r.TenantID, r.ClusterID, r.Meter, r.MeterEventName, r.Quantity, r.PeriodStart); pushErr != nil {
			deferred++
			f.recordFailure(ctx, r.ID, pushErr)
			continue
		}
		if markErr := f.markSent(ctx, r.ID); markErr != nil {
			// Edge case: Stripe accepted but we couldn't mark. The
			// identifier-based idempotency means a retry will collapse
			// on Stripe's side, so this is safe but loud.
			deferred++
			f.recordFailure(ctx, r.ID, fmt.Errorf("mark sent: %w", markErr))
			continue
		}
		sent++
	}
	return sent, deferred, nil
}

// pushOne calls Stripe's MeterEvent.Create. The event identifier is the
// outbox row id so a retry within 24h is collapsed by Stripe.
func (f *MeterFlusher) pushOne(ctx context.Context, rowID, tenantID, clusterID, meter, meterEventName, quantity string, periodStart time.Time) error {
	customerID, err := f.TenantStripeID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("resolve stripe customer: %w", err)
	}
	params := &stripeapi.BillingMeterEventParams{
		EventName:  stripeapi.String(meterEventName),
		Identifier: stripeapi.String(rowID),
		Timestamp:  stripeapi.Int64(periodStart.Unix()),
		Payload: map[string]string{
			"stripe_customer_id": customerID,
			"meter":              meter,
			"value":              quantity,
		},
	}
	if clusterID != "" {
		params.Payload["cluster_id"] = clusterID
	}
	if _, sendErr := stripemeter.New(params); sendErr != nil {
		return fmt.Errorf("stripe meter event: %w", sendErr)
	}
	return nil
}

func (f *MeterFlusher) markSent(ctx context.Context, rowID string) error {
	_, err := f.DB.ExecContext(ctx, `
		UPDATE purser.stripe_meter_events_outbox
		SET sent_at = NOW(), updated_at = NOW(), last_error = NULL
		WHERE id = $1
	`, rowID)
	return err
}

func (f *MeterFlusher) recordFailure(ctx context.Context, rowID string, failErr error) {
	if _, err := f.DB.ExecContext(ctx, `
		UPDATE purser.stripe_meter_events_outbox
		SET attempt_count = attempt_count + 1,
		    last_error = $2,
		    updated_at = NOW()
		WHERE id = $1
	`, rowID, failErr.Error()); err != nil {
		// Failure to record the failure is non-fatal: the next tick
		// will see (sent_at IS NULL) and retry. Surface so ops can
		// notice if this consistently fails.
		fmt.Fprintf(os.Stderr, "stripe meter flusher: record failure for %s: %v\n", rowID, err)
	}
}
