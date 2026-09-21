//go:build schema_verify

package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
)

// A failed charge on a Stripe-managed subscription records
// billing.payment_failed once, in the transaction of its legacy
// invoice_payment_failed row and under the same event ID: a failed event
// write rolls the whole failure back, a delivery that lost its claim to a
// takeover after the lease records nothing, and a redelivery of a settled
// event is skipped.
func TestStripeSubscriptionPaymentFailedRecordedOncePerCharge_RealPG(t *testing.T) { //nolint:funlen // Rollback, takeover, and redelivery share one fixture.
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	service := &Service{db: db, logger: logging.NewLogger()}
	tenantID := seedPrepaidLedgerTenant(t, db)
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions SET stripe_customer_id = 'cus_failed_charge' WHERE tenant_id = $1::uuid`, tenantID); err != nil {
		t.Fatal(err)
	}

	raw, err := json.Marshal(map[string]any{
		"id": "in_failed_charge", "customer": "cus_failed_charge", "amount_due": 2400,
		"currency": "usd", "status": "open", "attempt_count": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := StripeWebhookPayload{ID: "evt_failed_charge", Type: "invoice.payment_failed"}
	payload.Data.Object = raw
	body, err := json.Marshal(map[string]any{"id": payload.ID, "type": payload.Type, "data": map[string]any{"object": json.RawMessage(raw)}})
	if err != nil {
		t.Fatal(err)
	}

	dunning := func() int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, `SELECT dunning_attempts FROM purser.tenant_subscriptions WHERE tenant_id = $1::uuid`, tenantID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	type recorded struct{ domainIDs, legacyIDs []string }
	outboxes := func() recorded {
		t.Helper()
		var r recorded
		for query, dst := range map[string]*[]string{
			`SELECT event_id::text FROM purser.domain_event_outbox WHERE event_type = 'billing.payment_failed' AND tenant_id = $1::uuid`: &r.domainIDs,
			`SELECT id::text FROM purser.billing_event_outbox WHERE event_type = 'invoice_payment_failed' AND tenant_id = $1::uuid`:      &r.legacyIDs,
		} {
			rows, err := db.QueryContext(ctx, query, tenantID)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatal(err)
				}
				*dst = append(*dst, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			_ = rows.Close()
		}
		return r
	}
	claim := func() bool {
		t.Helper()
		c, err := service.claimWebhookEvent(ctx, "stripe", payload.ID, payload.Type, "", body)
		if err != nil {
			t.Fatal(err)
		}
		return c.claimed
	}
	expireClaim := func() {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			UPDATE purser.webhook_events SET received_at = NOW() - INTERVAL '1 hour'
			WHERE provider = 'stripe' AND event_id = $1`, payload.ID); err != nil {
			t.Fatal(err)
		}
	}
	webhookStatus := func() string {
		t.Helper()
		var s string
		if err := db.QueryRowContext(ctx, `SELECT status FROM purser.webhook_events WHERE provider = 'stripe' AND event_id = $1`, payload.ID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The domain event cannot be written: the dunning increment, the legacy
	// row, and the webhook settlement roll back, so the webhook retries.
	if !claim() {
		t.Fatal("first delivery must claim the event")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE purser.domain_event_outbox RENAME TO domain_event_outbox_offline`); err != nil {
		t.Fatal(err)
	}
	if err := service.handleStripeInvoiceFailed(payload); err == nil {
		t.Fatal("the failure must not commit without billing.payment_failed")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE purser.domain_event_outbox_offline RENAME TO domain_event_outbox`); err != nil {
		t.Fatal(err)
	}
	if got := outboxes(); dunning() != 0 || len(got.domainIDs) != 0 || len(got.legacyIDs) != 0 || webhookStatus() != "claimed" {
		t.Fatalf("a failed event write must roll everything back: dunning=%d outboxes=%+v status=%s", dunning(), got, webhookStatus())
	}

	// Delivery A's claim expires and delivery B takes it over and commits.
	// A then settles nothing and records nothing.
	expireClaim()
	if !claim() {
		t.Fatal("delivery B must take over the expired claim")
	}
	if err := service.handleStripeInvoiceFailed(payload); err != nil {
		t.Fatalf("delivery B: %v", err)
	}
	if err := service.handleStripeInvoiceFailed(payload); err != nil {
		t.Fatalf("delivery A after the takeover: %v", err)
	}
	got := outboxes()
	if len(got.domainIDs) != 1 || len(got.legacyIDs) != 1 || got.domainIDs[0] != got.legacyIDs[0] {
		t.Fatalf("one failed charge must record one billing.payment_failed sharing its legacy row's ID, got %+v", got)
	}
	if n := dunning(); n != 1 {
		t.Fatalf("dunning attempts = %d, want 1", n)
	}
	if s := webhookStatus(); s != "processed" {
		t.Fatalf("the webhook row must settle with the failure, status=%s", s)
	}

	// A redelivery of the settled event is skipped by the claim and records nothing.
	ok, _, code := service.processStripeWebhookPayload(ctx, payload, "", body)
	if !ok || code != 200 {
		t.Fatalf("redelivery ok=%v code=%d", ok, code)
	}
	if again := outboxes(); len(again.domainIDs) != 1 || len(again.legacyIDs) != 1 || dunning() != 1 {
		t.Fatalf("a redelivery recorded the failure again: %+v dunning=%d", again, dunning())
	}

	var eventPayload []byte
	var aggregateID string
	if err := db.QueryRowContext(ctx, `
		SELECT aggregate_id, payload FROM purser.domain_event_outbox WHERE event_id = $1::uuid`, got.domainIDs[0]).Scan(&aggregateID, &eventPayload); err != nil {
		t.Fatal(err)
	}
	_, msg, err := events.Decode("billing.payment_failed", eventPayload)
	if err != nil {
		t.Fatal(err)
	}
	failed := msg.(*publicv1.PaymentFailed)
	if aggregateID != "in_failed_charge" || failed.GetPaymentId() != "" || failed.GetInvoiceId() != "" ||
		failed.GetProvider() != "stripe" || failed.GetProviderReferenceId() != "in_failed_charge" ||
		failed.GetAmount().GetAmountMinor() != 2400 || failed.GetAmount().GetCurrency() != "USD" {
		t.Fatalf("billing.payment_failed aggregate=%s payload=%+v", aggregateID, failed)
	}
}
