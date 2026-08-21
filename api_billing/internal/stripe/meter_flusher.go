package stripe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	stripeapi "github.com/stripe/stripe-go/v85"
	stripemeter "github.com/stripe/stripe-go/v85/billing/meterevent"

	"frameworks/api_billing/internal/database/purserdb"
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
	// SendMeterEvent delivers one meter event to Stripe. Tests replace this
	// transport; nil falls back to the Stripe SDK sender.
	SendMeterEvent func(ctx context.Context, p *stripeapi.BillingMeterEventParams) error
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
		SendMeterEvent: func(_ context.Context, p *stripeapi.BillingMeterEventParams) error {
			_, err := stripemeter.New(p)
			return err
		},
		TenantStripeID: func(ctx context.Context, tenantID string) (string, error) {
			tenantUUID, err := uuid.Parse(tenantID)
			if err != nil {
				return "", fmt.Errorf("invalid tenant id %q: %w", tenantID, err)
			}
			customerID, err := purserdb.New(db).ResolveActiveStripeCustomer(ctx, tenantUUID)
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
	pending, err := purserdb.New(f.DB).ListPendingStripeMeterEvents(ctx, purserdb.ListPendingStripeMeterEventsParams{
		MaxAttempts: int32(f.MaxAttempts), BatchSize: int32(f.BatchSize),
	})
	if err != nil {
		return 0, 0, fmt.Errorf("query outbox: %w", err)
	}

	for _, r := range pending {
		if pushErr := f.pushOne(ctx, r.ID.String(), r.TenantID.String(), r.ClusterID, r.Meter, r.StripeMeterEventName, r.Quantity, string(r.Dimensions), r.PeriodStart); pushErr != nil {
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
func (f *MeterFlusher) pushOne(ctx context.Context, rowID, tenantID, clusterID, meter, meterEventName, quantity, dimensionsJSON string, periodStart time.Time) error {
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
	var dimensions map[string]string
	if err := json.Unmarshal([]byte(dimensionsJSON), &dimensions); err != nil {
		return fmt.Errorf("decode meter dimensions: %w", err)
	}
	for key, value := range dimensions {
		params.Payload["dimension_"+key] = value
	}
	sendMeterEvent := f.SendMeterEvent
	if sendMeterEvent == nil {
		sendMeterEvent = func(_ context.Context, p *stripeapi.BillingMeterEventParams) error {
			_, err := stripemeter.New(p)
			return err
		}
	}
	if sendErr := sendMeterEvent(ctx, params); sendErr != nil {
		return fmt.Errorf("stripe meter event: %w", sendErr)
	}
	return nil
}

func (f *MeterFlusher) markSent(ctx context.Context, rowID uuid.UUID) error {
	return purserdb.New(f.DB).MarkStripeMeterEventSent(ctx, rowID)
}

func (f *MeterFlusher) recordFailure(ctx context.Context, rowID uuid.UUID, failErr error) {
	if err := purserdb.New(f.DB).RecordStripeMeterEventFailure(ctx, purserdb.RecordStripeMeterEventFailureParams{
		ID: rowID, LastError: sql.NullString{String: failErr.Error(), Valid: true},
	}); err != nil {
		// Failure to record the failure is non-fatal: the next tick
		// will see (sent_at IS NULL) and retry. Surface so ops can
		// notice if this consistently fails.
		fmt.Fprintf(os.Stderr, "stripe meter flusher: record failure for %s: %v\n", rowID, err)
	}
}
