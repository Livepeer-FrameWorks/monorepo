//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/google/uuid"
)

func TestProviderWebhookAlternatingWaitFailureAlerts_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	ctx := t.Context()
	svc := &Service{db: db}
	if ok, _, _ := svc.enqueueProviderWebhook(ctx, "mollie", "alternating", nil, []byte(`{}`)); !ok {
		t.Fatal("enqueue")
	}
	store := &providerWebhookInboxStore{db: db}
	for _, failure := range []error{errors.New("network failure"), ErrAwaitingProviderSettlement, errors.New("network failure"), ErrAwaitingProviderSettlement} {
		claims, err := store.ClaimBatch(ctx, 1, time.Minute)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim: %v %v", claims, err)
		}
		if err := store.RecordFailureToken(ctx, claims[0].ID, claims[0].Attempts, nil, failure, 0, claims[0].LeaseToken); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := purserdb.New(db).ProviderWebhookInboxRepeatedFailures(ctx); err != nil || count != 1 {
		t.Fatalf("alternating waits erased repeated failures: count=%d err=%v", count, err)
	}
}

func TestCheckoutRecoveryTerminalOutcomeStable_RealPG(t *testing.T) {
	t.Run("one compensation owner across operator and worker", assertSingleCheckoutCompensationOwner)
	t.Run("already paid", func(t *testing.T) { assertCheckoutRecoveryTerminalOutcome(t, false) })
	t.Run("account closure during first debit", func(t *testing.T) { assertCheckoutRecoveryTerminalOutcome(t, true) })
}

func assertSingleCheckoutCompensationOwner(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	tenant.svc.stripeClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_session_id,provider_subscription_id,expires_at,recovery_started_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,'session','subscription',NOW()-INTERVAL '1 hour',NOW())`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
		entered <- struct{}{}
		<-release
		return 200, map[string]any{"id": "session", "status": "complete", "subscription": "subscription"}
	})
	fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "canceled"}
	})
	fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
	})
	expiry := checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}
	first := make(chan error, 1)
	go func() { first <- tenant.svc.expireCheckoutReservation(t.Context(), expiry) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first worker never reached provider reconciliation")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := tenant.svc.expireCheckoutReservation(ctx, expiry); !errors.Is(err, ErrAwaitingProviderSettlement) {
		t.Fatalf("concurrent compensation reached provider instead of retaining its owner: %v", err)
	}
	// Release provider I/O before waiting for cleanup; the deferred close also
	// releases it on a failing assertion.
	release <- struct{}{}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if calls := len(fixture.matching("GET", "/v1/checkout/sessions/session")); calls != 1 {
		t.Fatalf("concurrent provider compensation calls=%d, want one", calls)
	}
}

func assertCheckoutRecoveryTerminalOutcome(t *testing.T, processing bool) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	tenant.svc.stripeClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_session_id,provider_subscription_id,expires_at,recovery_started_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,'session','subscription',NOW()-INTERVAL '1 hour',NOW())`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	split := processing
	refunded, restRefunded := false, false
	fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "session", "status": "complete", "subscription": "subscription", "payment_status": "paid"}
	})
	fixture.route("GET", "/v1/subscriptions/subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "subscription", "status": "canceled"}
	})
	fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
		paid := 2900
		if processing {
			paid = 1000
		}
		return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "invoice", "billing_reason": "subscription_create", "amount_paid": paid}, map[string]any{"id": "renewal-1", "billing_reason": "subscription_cycle", "amount_paid": 2900}, map[string]any{"id": "renewal-2", "billing_reason": "subscription_cycle", "amount_paid": 2900}}, "has_more": false}
	})
	fixture.route("GET", "/v1/invoice_payments", func(r recordedProviderRequest) (int, any) {
		if r.query.Get("invoice") != "invoice" {
			t.Errorf("recovery reached delivered recurring invoice: %s", r.query.Get("invoice"))
		}
		if split {
			return 200, map[string]any{"object": "list", "data": []any{
				map[string]any{"id": "paid-part", "amount_paid": 1000, "payment": map[string]any{"type": "charge", "charge": "charge"}},
				map[string]any{"id": "debit-part", "amount_paid": 1900, "payment": map[string]any{"type": "payment_intent", "payment_intent": "debit"}},
			}, "has_more": false}
		}
		return 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": "payment", "payment": map[string]any{"type": "charge", "charge": "charge"}}}, "has_more": false}
	})
	fixture.route("GET", "/v1/charges/charge", func(recordedProviderRequest) (int, any) {
		original := 2900
		if split {
			original = 1000
		}
		amount := 0
		if refunded {
			amount = original
		}
		return 200, map[string]any{"id": "charge", "currency": "eur", "amount": original, "amount_refunded": amount, "refunded": refunded}
	})
	fixture.route("GET", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
		if r.query.Get("charge") == "" {
			t.Error("refund history was not scoped to the purchase charge")
		}
		return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
	})
	fixture.route("POST", "/v1/refunds", func(r recordedProviderRequest) (int, any) {
		if r.form.Get("payment_intent") == "debit" {
			restRefunded = true
		} else {
			refunded = true
		}
		return 200, map[string]any{"id": "refund", "status": "pending"}
	})
	fixture.route("GET", "/v1/payment_intents/debit", func(recordedProviderRequest) (int, any) {
		state := "succeeded"
		if processing {
			state = "processing"
		}
		returned := 0
		if restRefunded {
			returned = 1900
		}
		return 200, map[string]any{"id": "debit", "status": state, "latest_charge": map[string]any{"id": "rest-charge", "amount": 1900, "currency": "eur", "amount_refunded": returned}}
	})
	if processing {
		event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(now))
		if err != nil {
			t.Fatal(err)
		}
		key, headers, body, err := events.EncodeRecord(event.Envelope())
		if err != nil {
			t.Fatal(err)
		}
		msg := kafka.Message{Key: key, Value: body, Headers: map[string]string{}, Topic: topology.TopicDomainEvents}
		for _, header := range headers {
			msg.Headers[header.Key] = string(header.Value)
		}
		if err = tenant.jobs.handleBillingMessage(t.Context(), msg); err != nil {
			t.Fatal(err)
		}
		if err = tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); !errors.Is(err, ErrAwaitingProviderSettlement) {
			t.Fatalf("closure abandoned processing initial collection: %v", err)
		}
		row, err := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
		if err != nil || row.CompensationCurrent {
			t.Fatalf("processing debit lost durable refund obligation: %+v %v", row, err)
		}
		processing = false
	}
	expiry := checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}
	for attempt := 0; attempt < 2; attempt++ {
		if err := tenant.svc.expireCheckoutReservation(t.Context(), expiry); err != nil {
			t.Fatal(err)
		}
		row, err := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
		if err != nil || row.RecoveryOutcome.String != "cancelled_refund_requested" {
			t.Fatalf("repeat rewrote accepted refund outcome before settlement: %s %v", row.RecoveryOutcome.String, err)
		}
	}
	wantRefunds := 1
	if split {
		wantRefunds = 2
	}
	if calls := len(fixture.matching("POST", "/v1/refunds")); calls != wantRefunds {
		t.Fatalf("initial allocations refunded %d times, want %d", calls, wantRefunds)
	}
	if calls := len(fixture.matching("GET", "/v1/charges/charge")); calls != 1 {
		t.Fatalf("terminal compensation was repeated: %d charge reads", calls)
	}
}

func TestForcedRecoveryOnlyLocatesProviderObjects_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	fixture, client := installStripeFixture(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	tenant.svc.stripeClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_customer_id,expires_at,submission_state,provider_request) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_call_failed','EUR',$1::text,'customer',NOW()+INTERVAL '1 hour','prepared',NULL)`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	params := billingstripe.CheckoutSessionParams{CustomerID: "customer", TenantID: tenant.id, ReservationID: id, IdempotencyKey: id, ExpiresAt: now.Add(time.Hour)}
	if _, err := subscriptions.SaveProviderRequest(t.Context(), db, tenant.id, id, "stripe", "stripe_checkout", client.CredentialFingerprint(), &params); err != nil {
		t.Fatal(err)
	}
	fixture.route("GET", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
	})
	fixture.route("POST", "/v1/checkout/sessions", func(recordedProviderRequest) (int, any) {
		t.Error("forced recovery submitted a create request")
		return 500, map[string]any{}
	})
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); err != nil {
		t.Fatalf("locate-only recovery failed: %v", err)
	}
	if len(fixture.matching("POST", "/v1/checkout/sessions")) != 0 {
		t.Fatal("recovery created a checkout")
	}
}

func TestCheckoutRecoveryAuditTerminalResultIsImmutable_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','expired','EUR',$1::text)`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	q := purserdb.New(db)
	audit, err := q.InsertCheckoutRecoveryAudit(t.Context(), purserdb.InsertCheckoutRecoveryAuditParams{TenantID: tenant.id, IntentID: id, ActorID: "operator", Reason: "confirmed"})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"cancelled_refund_requested", "cancelled_refund_reconciled"} {
		if err := q.FinishCheckoutRecoveryAudit(t.Context(), purserdb.FinishCheckoutRecoveryAuditParams{TenantID: tenant.id, ID: audit, Outcome: outcome, Detail: outcome}); err != nil {
			t.Fatal(err)
		}
	}
	var outcome, detail string
	if err := db.QueryRow(`SELECT outcome,detail FROM purser.checkout_recovery_audit WHERE tenant_id=$1 AND id=$2`, tenant.id, audit).Scan(&outcome, &detail); err != nil {
		t.Fatal(err)
	}
	if outcome != "cancelled_refund_requested" || detail != outcome {
		t.Fatalf("terminal operator audit was rewritten: %s %s", outcome, detail)
	}
}

func TestHistoricalRecoveryDoesNotReplaceEstablishedSubscription_RealPG(t *testing.T) {
	t.Run("Mollie customer binding", testRecoveryProbesBoundMollieCustomer)
	t.Run("operator cancels unactivated purchase with unknown previous binding", testRecoveryUnknownPreviousMollieCustomer)
	for _, scenario := range []string{"cancelled Stripe", "replaced by Mollie", "cancelled prior reference", "missing checkout tier"} {
		t.Run(scenario, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			fixture, client := installStripeFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.stripeClient = client
			if scenario == "replaced by Mollie" {
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='established' WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "cancelled prior reference" {
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='cancelled',stripe_subscription_id='previous' WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
				fixture.route("GET", "/v1/subscriptions/previous", func(recordedProviderRequest) (int, any) {
					return 200, map[string]any{"id": "previous", "status": "canceled"}
				})
			}
			id := uuid.NewString()
			created := now.Add(-25 * time.Hour)
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,local_reference_id,provider_customer_id,provider_session_id,created_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,$3,'customer','session',$4)`, id, tenant.id, tenant.supporterID, created); err != nil {
				t.Fatal(err)
			}
			if scenario == "missing checkout tier" {
				if _, err := db.Exec(`UPDATE purser.payment_provider_intents SET local_reference_id=NULL WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			}
			fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "session", "status": "complete", "payment_status": "paid", "mode": "subscription", "created": created.Unix(), "expires_at": created.Add(24 * time.Hour).Unix(), "customer": "customer", "subscription": "historical", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}
			})
			fixture.route("GET", "/v1/subscriptions/historical", func(recordedProviderRequest) (int, any) {
				state := "canceled"
				if scenario == "replaced by Mollie" || scenario == "cancelled prior reference" || scenario == "missing checkout tier" {
					state = "active"
				}
				return 200, map[string]any{"id": "historical", "status": state}
			})
			fixture.route("DELETE", "/v1/subscriptions/historical", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "historical", "status": "canceled"}
			})
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err != nil {
				t.Fatal(err)
			}
			var stripe, mollie sql.NullString
			if err := db.QueryRow(`SELECT stripe_subscription_id,mollie_subscription_id FROM purser.tenant_subscriptions WHERE tenant_id=$1`, tenant.id).Scan(&stripe, &mollie); err != nil {
				t.Fatal(err)
			}
			if scenario == "cancelled prior reference" {
				if stripe.String != "historical" || len(fixture.matching("POST", "/v1/refunds")) != 0 || len(fixture.matching("DELETE", "/v1/subscriptions/historical")) != 0 {
					t.Fatalf("new on-time purchase was compensated: stripe=%v", stripe)
				}
				return
			}
			if stripe.String != "" || scenario == "replaced by Mollie" && mollie.String != "established" {
				t.Fatalf("historical receipt replaced established pricing: stripe=%v mollie=%v", stripe, mollie)
			}
		})
	}
}

func testRecoveryProbesBoundMollieCustomer(t *testing.T) {
	for _, oldState := range []string{"active", "canceled"} {
		t.Run(oldState, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			fixture, client := installMollieFixture(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.mollieClient = client
			if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='cancelled',mollie_subscription_id='old-subscription' WHERE tenant_id=$1;`, tenant.id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'current-customer')`, tenant.id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,idempotency_key,provider_customer_id,provider_subscription_id) VALUES($1,'mollie','mollie_subscription_create','billing_tiers',$2,'succeeded','EUR','old-native','old-customer','old-subscription')`, tenant.id, tenant.supporterID); err != nil {
				t.Fatal(err)
			}
			fixture.route("GET", "/v2/customers/old-customer/subscriptions/old-subscription", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "old-subscription", "status": oldState}
			})
			fixture.route("GET", "/v2/customers/current-customer/subscriptions/old-subscription", func(recordedProviderRequest) (int, any) {
				return 404, map[string]any{"status": 404, "detail": "not owned by current customer"}
			})
			locked := subscriptions.LockedSubscription{Current: purserdb.LockTenantSubscriptionForProviderChangeRow{Status: "cancelled", MollieSubscriptionID: sql.NullString{String: "old-subscription", Valid: true}}}
			conflict, err := tenant.svc.checkoutHasConflictingSubscription(t.Context(), db, tenant.id, locked, "stripe", "incoming")
			if err != nil || conflict != (oldState == "active") {
				t.Fatalf("bound Mollie collector liveness: conflict=%v state=%s err=%v", conflict, oldState, err)
			}
			if len(fixture.matching("GET", "/v2/customers/old-customer/subscriptions/old-subscription")) != 1 || len(fixture.matching("GET", "/v2/customers/current-customer/subscriptions/old-subscription")) != 0 {
				t.Fatal("liveness probe used mutable current customer instead of native receipt binding")
			}
		})
	}
}

func testRecoveryUnknownPreviousMollieCustomer(t *testing.T) {
	for _, binding := range []string{"missing receipt", "unbound receipt"} {
		t.Run(binding, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			fixture, client := installStripeFixture(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			tenant.svc.stripeClient = client
			_, tenant.svc.mollieClient = installMollieFixture(t)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.tenant_subscriptions SET status='cancelled',mollie_subscription_id='previous' WHERE tenant_id=$1`, tenant.id)
			if binding == "unbound receipt" {
				exec(`INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,status,currency,idempotency_key,provider_subscription_id) VALUES($1,'mollie','mollie_subscription_create','succeeded','EUR','old-unbound','previous')`, tenant.id)
			}
			id := uuid.NewString()
			created := now.Add(-25 * time.Hour)
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,local_reference_id,provider_customer_id,provider_session_id,created_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,$3,'customer','session',$4)`, id, tenant.id, tenant.supporterID, created)
			fixture.route("GET", "/v1/checkout/sessions/session", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "session", "status": "complete", "payment_status": "paid", "mode": "subscription", "created": created.Unix(), "expires_at": created.Add(24 * time.Hour).Unix(), "customer": "customer", "subscription": "incoming", "metadata": map[string]string{"tenant_id": tenant.id, "purpose": "subscription", "reference_id": tenant.supporterID, "checkout_reservation_id": id}}
			})
			fixture.route("GET", "/v1/subscriptions/incoming", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "incoming", "status": "active"}
			})
			fixture.route("DELETE", "/v1/subscriptions/incoming", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"id": "incoming", "status": "canceled"}
			})
			fixture.route("GET", "/v1/invoices", func(recordedProviderRequest) (int, any) {
				return 200, map[string]any{"object": "list", "data": []any{}, "has_more": false}
			})
			if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id}); err == nil || !strings.Contains(err.Error(), "original customer") {
				t.Fatalf("automatic recovery guessed a previous collector: %v", err)
			}
			ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, uuid.NewString())
			out, err := tenant.svc.RecoverCheckoutResult(ctx, tenant.id, id, "cancel new purchase; previous binding is not provable")
			if err != nil || out.Outcome != "cancelled" || len(fixture.matching("DELETE", "/v1/subscriptions/incoming")) != 1 {
				t.Fatalf("confirmed compensation cannot clear unknown binding: out=%+v err=%v", out, err)
			}
			var previous, state string
			if err := db.QueryRow(`SELECT s.mollie_subscription_id,i.status FROM purser.tenant_subscriptions s JOIN purser.payment_provider_intents i ON i.tenant_id=s.tenant_id WHERE s.tenant_id=$1 AND i.id=$2`, tenant.id, id).Scan(&previous, &state); err != nil || previous != "previous" || state != "expired" {
				t.Fatalf("compensation changed the previous collector or kept the fence: previous=%s state=%s err=%v", previous, state, err)
			}
			for _, request := range fixture.requests {
				if strings.Contains(request.path, "previous") {
					t.Fatalf("confirmed recovery touched unproven previous collector: %+v", request)
				}
			}
		})
	}
}
