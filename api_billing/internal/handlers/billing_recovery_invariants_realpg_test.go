//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/google/uuid"
)

func TestHistoricalMollieSubscriptionCheckoutSettlesWithoutRefund_RealPG(t *testing.T) {
	t.Run("delivered Stripe receipt", assertHistoricalStripeCheckoutRetained)
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET mollie_subscription_id='current-subscription' WHERE tenant_id=$1`, tenant.id); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_customer_id,provider_subscription_id,created_at)
 VALUES($1::uuid,$2,'mollie','mollie_subscription_create','provider_open','EUR',$1::text,'customer','historical-subscription',NOW()-INTERVAL '90 days')`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	fixture.route("GET", "/v2/customers/customer/subscriptions/historical-subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "historical-subscription", "status": "active"}
	})
	fixture.route("DELETE", "/v2/customers/customer/subscriptions/historical-subscription", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "historical-subscription", "status": "canceled"}
	})
	fixture.route("GET", "/v2/customers/customer/subscriptions/historical-subscription/payments", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"_embedded": map[string]any{"payments": []any{map[string]any{"id": "delivered-1"}, map[string]any{"id": "delivered-2"}}}}
	})
	for _, payment := range []string{"delivered-1", "delivered-2"} {
		fixture.route("GET", "/v2/payments/"+payment, func(recordedProviderRequest) (int, any) {
			return 200, map[string]any{"id": payment, "status": "paid", "subscriptionId": "historical-subscription", "sequenceType": "recurring", "amount": map[string]string{"value": "20.00", "currency": "EUR"}}
		})
	}
	if err := purserdb.New(db).EnqueueUnresolvedCheckoutRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
	worker.ProcessBatch(t.Context())
	row, err := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
	if err != nil || !row.CompensationCurrent {
		t.Fatalf("bound historical subscription left open: status=%s err=%v", row.Status, err)
	}
	if len(fixture.matching("DELETE", "/v2/customers/customer/subscriptions/historical-subscription")) != 1 {
		t.Fatal("historical orphan subscription was not cancelled")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, request := range fixture.requests {
		if request.path != "/v2/customers/customer/subscriptions/historical-subscription" {
			t.Fatalf("historical service reached payment compensation: %+v", request)
		}
	}
}

func TestCheckoutWakeStartsNewRecoveryAttempt_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	q := purserdb.New(db)
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','expired','EUR',$1::text)`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	if err := q.EnqueueCheckoutReservationExpiry(t.Context(), purserdb.EnqueueCheckoutReservationExpiryParams{TenantID: tenant.id, IntentID: id}); err != nil {
		t.Fatal(err)
	}
	for _, wake := range []struct {
		name string
		run  func() error
	}{
		{"request", func() error {
			return q.RequestCheckoutRecovery(t.Context(), purserdb.RequestCheckoutRecoveryParams{TenantID: tenant.id, IntentID: id, Reason: "inspect late receipt"})
		}},
		{"late callback", func() error {
			return q.RequeueExpiredCheckoutReservation(t.Context(), purserdb.RequeueExpiredCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
		}},
		{"session", func() error {
			return q.SetProviderIntentSessionOpen(t.Context(), purserdb.SetProviderIntentSessionOpenParams{TenantID: tenant.id, IntentID: id, SessionID: sql.NullString{String: "session", Valid: true}})
		}},
		{"payment", func() error {
			return q.SetProviderIntentPaymentOpen(t.Context(), purserdb.SetProviderIntentPaymentOpenParams{TenantID: tenant.id, IntentID: id, PaymentID: sql.NullString{String: "payment", Valid: true}})
		}},
		{"subscription", func() error {
			return q.SetProviderIntentSubscriptionOpen(t.Context(), purserdb.SetProviderIntentSubscriptionOpenParams{TenantID: tenant.id, IntentID: id, SubscriptionID: sql.NullString{String: "subscription", Valid: true}})
		}},
	} {
		t.Run(wake.name, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET status='processed',attempts=40,wait_attempts=50,unresolved_since=NOW()-INTERVAL '90 days',processed_at=NOW() WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+id); err != nil {
				t.Fatal(err)
			}
			if err := wake.run(); err != nil {
				t.Fatal(err)
			}
			var attempts, waits int
			var unresolved sql.NullTime
			if err := db.QueryRow(`SELECT attempts,wait_attempts,unresolved_since FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND event_key=$1`, "checkout-expiry:"+id).Scan(&attempts, &waits, &unresolved); err != nil {
				t.Fatal(err)
			}
			if attempts != 0 || waits != 0 || unresolved.Valid {
				t.Fatalf("new receipt inherited an old alert: attempts=%d waits=%d unresolved=%v", attempts, waits, unresolved)
			}
		})
	}
}

func TestDefiniteCheckoutRejectionReleasesOnlyUnboundIntent_RealPG(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
	for _, tc := range []struct {
		name            string
		definite, bound bool
		want            string
	}{{"rejected", true, false, "terminal_failed"}, {"ambiguous", false, false, "provider_call_failed"}, {"bound", true, true, "provider_call_failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.NewString()
			var session any
			if tc.bound {
				session = "payable-session"
			}
			if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,submission_state,provider_request,provider_session_id) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,'submitted','{}',$3)`, id, tenant.id, session); err != nil {
				t.Fatal(err)
			}
			if err := purserdb.New(db).SetProviderPaymentIntentFailure(t.Context(), purserdb.SetProviderPaymentIntentFailureParams{TenantID: tenant.id, IntentID: id, Status: "terminal_failed", DefiniteRejection: tc.definite}); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRow(`SELECT status FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("state=%s want %s", got, tc.want)
			}
		})
	}
}

type suspensionRetryClient struct {
	mockCommodoreClient
	down   bool
	onStop func()
}

func (c *suspensionRetryClient) TerminateTenantStreams(context.Context, string, string) (*foghornpb.TerminateTenantStreamsResponse, error) {
	c.terminations++
	if c.onStop != nil {
		c.onStop()
	}
	if c.down {
		return nil, grpcutil.PendingStreamStop(&foghornpb.TerminateTenantStreamsResponse{StreamsTerminated: 1})
	}
	return &foghornpb.TerminateTenantStreamsResponse{StreamsTerminated: 1}, nil
}

func TestSuspensionStopRetriesAndRespectsResumption_RealPG(t *testing.T) {
	t.Run("fanout holds no subscription lock", func(t *testing.T) {
		db := startProviderReservationDatabase(t)
		now := time.Now().UTC()
		tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
		if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='suspended' WHERE tenant_id=$1`, tenant.id); err != nil {
			t.Fatal(err)
		}
		client := &suspensionRetryClient{onStop: func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET billing_name='Concurrent billing update' WHERE tenant_id=$1`, tenant.id); err != nil {
				t.Fatalf("fanout kept the subscription locked: %v", err)
			}
		}}
		tenant.jobs.commodoreClient = client
		if err := tenant.jobs.stopSuspendedTenantStreams(t.Context(), tenant.id); err != nil || client.terminations != 1 {
			t.Fatalf("stop calls=%d err=%v", client.terminations, err)
		}
	})
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover cell", true: "resume before retry"}[resume], func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now, now.AddDate(0, 1, 0), 0)
			client := &suspensionRetryClient{down: true}
			tenant.jobs.commodoreClient = client
			wireAdvanceBilling(tenant.svc, tenant.jobs)
			enforcer := NewThresholdEnforcer(db, tenant.jobs.logger, client, nil, tenant.svc, nil)
			if err := enforcer.suspendTenantForBalance(t.Context(), tenant.id, -1001); err != nil {
				t.Fatal(err)
			}
			var queued int
			if err := db.QueryRow(`SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='tenant_suspension' AND event_key=$1`, "tenant-suspension:"+tenant.id).Scan(&queued); err != nil || queued != 1 {
				t.Fatalf("suspension omitted durable stop: count=%d err=%v", queued, err)
			}
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			worker.ProcessBatch(t.Context())
			before := client.terminations
			var state string
			if err := db.QueryRow(`SELECT status FROM purser.provider_webhook_inbox WHERE provider='tenant_suspension' AND event_key=$1`, "tenant-suspension:"+tenant.id).Scan(&state); err != nil || state != "failed" {
				t.Fatalf("partial stop did not stay pending: state=%s err=%v", state, err)
			}
			if resume {
				if _, err := db.Exec(`UPDATE purser.tenant_subscriptions SET status='active' WHERE tenant_id=$1`, tenant.id); err != nil {
					t.Fatal(err)
				}
			}
			client.down = false
			if _, err := db.Exec(`UPDATE purser.provider_webhook_inbox SET next_attempt_at=NOW() WHERE provider='tenant_suspension' AND event_key=$1`, "tenant-suspension:"+tenant.id); err != nil {
				t.Fatal(err)
			}
			worker.ProcessBatch(t.Context())
			if err := db.QueryRow(`SELECT status FROM purser.provider_webhook_inbox WHERE provider='tenant_suspension' AND event_key=$1`, "tenant-suspension:"+tenant.id).Scan(&state); err != nil || state != "processed" {
				t.Fatalf("retry did not finish: state=%s err=%v", state, err)
			}
			want := before + 1
			if resume {
				want = before
			}
			if client.terminations != want {
				t.Fatalf("termination calls=%d want %d", client.terminations, want)
			}
		})
	}
}

func assertHistoricalStripeCheckoutRetained(t *testing.T) {
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC()
	tenant := newPhaseTenant(t, db, "postpaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
	fixture, client := installStripeFixture(t)
	tenant.svc.stripeClient = client
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,idempotency_key,provider_customer_id,provider_subscription_id,succeeded_at,created_at) VALUES($1::uuid,$2,'stripe','tenant_subscription_checkout','provider_open','EUR',$1::text,'customer','historical',NOW()-INTERVAL '60 days',NOW()-INTERVAL '90 days')`, id, tenant.id); err != nil {
		t.Fatal(err)
	}
	fixture.route("GET", "/v1/subscriptions/historical", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "historical", "status": "active"}
	})
	fixture.route("DELETE", "/v1/subscriptions/historical", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"id": "historical", "status": "canceled"}
	})
	if err := tenant.svc.expireCheckoutReservation(t.Context(), checkoutReservationExpiry{TenantID: tenant.id, IntentID: id, Force: true}); err != nil {
		t.Fatal(err)
	}
	if len(fixture.matching("DELETE", "/v1/subscriptions/historical")) != 1 {
		t.Fatal("historical Stripe renewal was left charging")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	for _, request := range fixture.requests {
		if request.path != "/v1/subscriptions/historical" {
			t.Fatalf("delivered Stripe service reached compensation: %+v", request)
		}
	}
}
