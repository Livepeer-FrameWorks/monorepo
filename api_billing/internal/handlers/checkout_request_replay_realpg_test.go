//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/google/uuid"
)

func TestCheckoutRequestJournalAndLateResponseHandoff_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	tenant.svc.stripeClient = client
	ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, uuid.NewString())
	id, err := subscriptions.ReserveCheckout(ctx, db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertStripeTenantCheckoutIntent(ctx, purserdb.UpsertStripeTenantCheckoutIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "journal"})
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := purserdb.New(db).GetProviderCheckoutReservationKey(ctx, purserdb.GetProviderCheckoutReservationKeyParams{TenantID: tenant.id, IntentID: id})
	if err != nil {
		t.Fatal(err)
	}
	original := billingstripe.CheckoutSessionParams{CustomerID: "customer", TenantID: tenant.id, TierID: tenant.supporterID, Purpose: "subscription", ReferenceID: tenant.supporterID, PriceID: "original_price", Currency: "EUR", SuccessURL: "https://original.example/success", CancelURL: "https://original.example/cancel", IdempotencyKey: key, ReservationID: id, ExpiresAt: now.Add(23 * time.Hour)}
	params := original
	journal, err := subscriptions.SaveProviderRequest(ctx, db, tenant.id, id, "stripe", "stripe_checkout", client.CredentialFingerprint(), &params)
	if err != nil {
		t.Fatal(err)
	}
	if !journal.ReplayBefore.After(now.Add(22*time.Hour)) || journal.ReplayBefore.After(now.Add(23*time.Hour+time.Minute)) {
		t.Fatalf("Stripe replay window=%s", journal.ReplayBefore)
	}
	params.SuccessURL = "https://changed.example"
	params.PriceID = "changed_price"
	replay, err := subscriptions.SaveProviderRequest(ctx, db, tenant.id, id, "stripe", "stripe_checkout", client.CredentialFingerprint(), &params)
	if err != nil || !reflect.DeepEqual(original, params) || !replay.ReplayBefore.Equal(journal.ReplayBefore) {
		t.Fatalf("retry changed original parameters/window: %v %v", params, err)
	}
	if _, err := subscriptions.SaveProviderRequest(ctx, db, tenant.id, id, "stripe", "stripe_checkout", "rotated-credential", &params); err == nil {
		t.Fatal("request replay accepted different credential")
	}
	calls, created := 0, 0
	closed := false
	session := "cs_" + id
	fixture.route("POST", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		calls++
		if calls == 1 {
			return 503, map[string]any{"error": map[string]string{"type": "api_error", "message": "ambiguous create"}}
		}
		created++
		return 200, map[string]any{"id": session, "status": "open", "metadata": map[string]string{"checkout_reservation_id": id}}
	})
	fixture.route("GET", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		data := []any{}
		if created > 0 {
			data = append(data, map[string]any{"id": session, "status": "open", "metadata": map[string]string{"checkout_reservation_id": id}})
		}
		return 200, map[string]any{"object": "list", "data": data, "has_more": false}
	})
	fixture.route("GET", "/v1/checkout/sessions/"+session, func(recordedProviderRequest) (int, any) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		state := "open"
		if closed {
			state = "expired"
		}
		return 200, map[string]any{"id": session, "status": state}
	})
	fixture.route("POST", "/v1/checkout/sessions/"+session+"/expire", func(recordedProviderRequest) (int, any) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		closed = true
		return 200, map[string]any{"id": session, "status": "expired"}
	})
	if _, err := client.CreateCheckoutSession(ctx, params); err == nil {
		t.Fatal("lost create response was treated as successful")
	}
	params.ResumeOnly = true
	params.ReplayBefore = journal.ReplayBefore
	if _, err := client.CreateCheckoutSession(ctx, params); err != nil {
		t.Fatal(err)
	}
	result, err := tenant.svc.RecoverCheckoutResult(ctx, tenant.id, id, "recover timed-out create")
	if err != nil || result.GetOutcome() != "expired" {
		t.Fatalf("journal recovery=%v err=%v", result, err)
	}
	requests := fixture.matching("POST", "/v1/checkout/sessions")
	if len(requests) != 2 || requests[0].idempotencyKey != key || requests[1].idempotencyKey != key || !reflect.DeepEqual(requests[0].form, requests[1].form) {
		t.Fatalf("replay changed provider request: %v", requests)
	}
	fixture.mu.Lock()
	count := created
	fixture.mu.Unlock()
	if count != 1 {
		t.Fatalf("recovery created another checkout: %d", count)
	}
	// A response arriving during an older recovery claim must survive its ack.
	store := &providerWebhookInboxStore{db: db}
	if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE event_key=$1`, "checkout-expiry:"+id); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimBatch(ctx, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim=%v err=%v", claims, err)
	}
	if err := purserdb.New(db).SetProviderIntentSessionOpen(ctx, purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: id, SessionID: sql.NullString{String: session, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCompletedToken(ctx, claims[0].ID, claims[0].LeaseToken); err == nil {
		t.Fatal("old claim swallowed late provider response")
	}
	next, err := store.ClaimBatch(ctx, 1, time.Minute)
	if err != nil || len(next) != 1 || next[0].LeaseToken == claims[0].LeaseToken {
		t.Fatalf("late response did not get fresh claim: %v %v", next, err)
	}
}

func TestMollieRequestJournalHasOneHourCredentialScope_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	id, err := subscriptions.ReserveCheckout(t.Context(), db, tenant.id, func(tx *sql.Tx) (string, error) {
		return purserdb.New(tx).UpsertMollieFirstPaymentIntent(t.Context(), purserdb.UpsertMollieFirstPaymentIntentParams{TenantID: tenant.id, TierID: tenant.supporterID, Currency: "EUR", IdempotencyKey: "journal-mollie"})
	})
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{"IdempotencyKey": "original", "CustomerID": "customer"}
	journal, err := subscriptions.SaveProviderRequest(t.Context(), db, tenant.id, id, "mollie", "mollie_first", "credential", &params)
	if err != nil {
		t.Fatal(err)
	}
	if !journal.ReplayBefore.After(now.Add(40*time.Minute)) || journal.ReplayBefore.After(now.Add(50*time.Minute)) {
		t.Fatalf("Mollie replay exceeded key retention: %s", journal.ReplayBefore)
	}
}
