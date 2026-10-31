//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/google/uuid"
)

func assertClosureRefundsOnlyUnactivatedFirstPayment(t *testing.T) {
	t.Helper()
	t.Run("multiple historically delivered first payments", assertHistoricalMollieMultipleDeliveredPayments)
	t.Run("delivery decision guards", assertHistoricalDeliveryDecisionGuards)
	for _, kind := range []string{"unactivated current", "unactivated zero mandate", "unactivated legacy", "delivered legacy", "delivered legacy with reopened receipt", "cancelled create without activation", "cancelled create already refunded"} {
		t.Run(kind, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC().Truncate(time.Second)
			tenant := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			fixture, client := installMollieFixture(t)
			tenant.svc.mollieClient = client
			id := uuid.NewString()
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,expires_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','provider_open','EUR',2000,$1::text,'customer','initial',NOW()+INTERVAL '1 hour')`, id, tenant.id); err != nil {
				t.Fatal(err)
			}
			if kind == "unactivated zero mandate" {
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET amount_cents=0 WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unactivated current" || kind == "unactivated zero mandate" {
				if err := tenant.svc.completeMollieFirstPayment(t.Context(), tenant.id, "initial", id, billingmollie.MandateInfo{MollieCustomerID: "customer", MollieMandateID: "mandate", Status: "valid", CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET status='succeeded',succeeded_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "delivered legacy") {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,status,currency,idempotency_key,provider_customer_id,provider_subscription_id,succeeded_at) VALUES($1,'mollie','mollie_subscription_create','provider_open','EUR','historical-native','customer','historical',NOW())`, tenant.id); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "delivered legacy with reopened receipt" {
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET status='provider_open' WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			}
			refunded := kind == "cancelled create already refunded"
			historicalCancelled := strings.HasPrefix(kind, "cancelled create")
			fixture.route("GET", "/v2/payments/initial", func(recordedProviderRequest) (int, any) {
				returned := "0.00"
				if refunded {
					returned = "20.00"
				}
				collected := "20.00"
				if kind == "unactivated zero mandate" {
					collected = "0.00"
				}
				return 200, map[string]any{"id": "initial", "customerId": "customer", "mandateId": "mandate", "status": "paid", "sequenceType": "first", "createdAt": now.Add(-time.Minute).Format(time.RFC3339), "paidAt": now.Format(time.RFC3339), "amount": map[string]string{"value": collected, "currency": "EUR"}, "amountRefunded": map[string]string{"value": returned, "currency": "EUR"}}
			})
			fixture.route("GET", "/v2/customers/customer/subscriptions", func(recordedProviderRequest) (int, any) {
				subscriptions := []any{}
				if strings.HasPrefix(kind, "delivered legacy") || strings.HasPrefix(kind, "cancelled create") {
					subscriptions = append(subscriptions, map[string]any{"id": "historical", "mandateId": "mandate", "createdAt": now.Format(time.RFC3339), "status": "active"})
				}
				return 200, map[string]any{"_embedded": map[string]any{"subscriptions": subscriptions}, "_links": map[string]any{}}
			})
			fixture.route("GET", "/v2/customers/customer/subscriptions/historical", func(recordedProviderRequest) (int, any) {
				state := "active"
				if historicalCancelled {
					state = "canceled"
				}
				return 200, map[string]any{"id": "historical", "status": state}
			})
			fixture.route("DELETE", "/v2/customers/customer/subscriptions/historical", func(recordedProviderRequest) (int, any) {
				historicalCancelled = true
				return 200, map[string]any{"id": "historical", "status": "canceled"}
			})
			fixture.route("POST", "/v2/payments/initial/refunds", func(recordedProviderRequest) (int, any) {
				refunded = true
				return 201, map[string]any{"id": "refund", "status": "pending"}
			})
			event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(now))
			if err != nil {
				t.Fatal(err)
			}
			key, headers, body, err := events.EncodeRecord(event.Envelope())
			if err != nil {
				t.Fatal(err)
			}
			message := kafka.Message{Key: key, Headers: map[string]string{}, Value: body, Topic: topology.TopicDomainEvents}
			for _, header := range headers {
				message.Headers[header.Key] = string(header.Value)
			}
			if err = tenant.jobs.handleBillingMessage(t.Context(), message); err != nil {
				t.Fatal(err)
			}
			worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
			for range 3 {
				worker.ProcessBatch(t.Context())
			}
			if kind == "cancelled create already refunded" || kind == "unactivated zero mandate" {
				row, readErr := purserdb.New(db).GetProviderCheckoutReservation(t.Context(), purserdb.GetProviderCheckoutReservationParams{TenantID: tenant.id, IntentID: id})
				if readErr != nil || !row.CompensationCurrent || len(fixture.matching("POST", "/v2/payments/initial/refunds")) != 0 {
					t.Fatalf("confirmed bank return did not resolve ambiguous funding safely: row=%+v err=%v", row, readErr)
				}
				return
			}
			if strings.HasPrefix(kind, "delivered legacy") || strings.HasPrefix(kind, "cancelled create") {
				if refunded {
					t.Fatal("closure refunded a first payment that funded delivered service")
				}
				if strings.HasPrefix(kind, "delivered legacy") {
					var activated sql.NullTime
					if err := db.QueryRowContext(t.Context(), `SELECT activated_at FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&activated); err != nil || !activated.Valid {
						t.Fatalf("delivered funding did not resolve through activation proof: %+v %v", activated, err)
					}
				}
				var detail string
				if kind == "cancelled create without activation" {
					if err := db.QueryRowContext(t.Context(), `SELECT last_error FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND headers->>'tenant_id'=$1`, tenant.id).Scan(&detail); err != nil || !strings.Contains(detail, "cannot prove activation") {
						t.Fatalf("ambiguous activation history silently settled: detail=%s err=%v", detail, err)
					}
					preview, previewErr := tenant.svc.PreviewRetainDeliveredCheckout(t.Context(), tenant.id, id)
					if previewErr != nil || preview.GetOutcome() != "preview" {
						t.Fatalf("historical delivery preview: %v %v", preview, previewErr)
					}
					var activated sql.NullTime
					if err := db.QueryRowContext(t.Context(), `SELECT activated_at FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&activated); err != nil || activated.Valid {
						t.Fatalf("preview changed funding: %v %v", activated, err)
					}
					ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, "historical-funding-operator")
					out, decisionErr := tenant.svc.RetainDeliveredCheckout(ctx, tenant.id, id, "Confirmed delivered service from historical records")
					if decisionErr != nil || out.GetOutcome() != "delivered" || out.GetAuditId() == "" {
						t.Fatalf("operator could not settle delivered history: %v %v", out, decisionErr)
					}
					var actor, outcome, reason string
					if err := db.QueryRowContext(t.Context(), `SELECT actor_id, outcome, reason FROM purser.checkout_recovery_audit WHERE tenant_id=$1 AND id=$2`, tenant.id, out.GetAuditId()).Scan(&actor, &outcome, &reason); err != nil || actor != "historical-funding-operator" || outcome != "delivered" || reason != "Confirmed delivered service from historical records" {
						t.Fatalf("delivery decision lost its audit: actor=%s outcome=%s reason=%s err=%v", actor, outcome, reason, err)
					}
					worker.ProcessBatch(t.Context())
					if refunded {
						t.Fatal("delivery attestation refunded delivered service")
					}
				}
				if strings.HasPrefix(kind, "delivered legacy") && len(fixture.matching("DELETE", "/v2/customers/customer/subscriptions/historical")) != 1 {
					t.Fatal("closure left orphan historical subscription charging")
				}
			} else if !refunded {
				t.Fatal("closure abandoned a paid first payment whose subscription never activated")
			}
		})
	}
}

func assertHistoricalDeliveryDecisionGuards(t *testing.T) {
	for _, kind := range []string{"current mandate", "unpaid", "refund promised", "provider refunded", "another tenant refund", "recovery owns intent"} {
		t.Run(kind, func(t *testing.T) {
			db := startProviderReservationDatabase(t)
			now := time.Now().UTC()
			tenant := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
			fixture, client := installMollieFixture(t)
			tenant.svc.mollieClient = client
			id := uuid.NewString()
			if _, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,succeeded_at) VALUES($1::uuid,$2::uuid,'mollie','mollie_first_payment','succeeded','EUR',2000,$1::text,'customer','initial',NOW())`, id, tenant.id); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "current mandate":
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET provider_mandate_id='mandate' WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			case "unpaid":
				if _, err := db.ExecContext(t.Context(), `UPDATE purser.payment_provider_intents SET status='provider_open',succeeded_at=NULL WHERE tenant_id=$1 AND id=$2`, tenant.id, id); err != nil {
					t.Fatal(err)
				}
			case "refund promised", "another tenant refund":
				owner := tenant.id
				if kind == "another tenant refund" {
					owner = uuid.NewString()
				}
				if err := purserdb.New(db).EnqueueRefundSettlement(t.Context(), purserdb.EnqueueRefundSettlementParams{EventKey: "refund-test", TenantID: owner, Provider: "mollie", PaymentID: "initial", ReferenceID: id, ReferenceKind: "checkout", Currency: "EUR", AmountCents: 2000, Payload: []byte(`{}`)}); err != nil {
					t.Fatal(err)
				}
			}
			fixture.route("GET", "/v2/payments/initial", func(recordedProviderRequest) (int, any) {
				returned := "0.00"
				if kind == "provider refunded" {
					returned = "20.00"
				}
				return 200, map[string]any{"id": "initial", "customerId": "customer", "status": "paid", "amount": map[string]string{"value": "20.00", "currency": "EUR"}, "amountRefunded": map[string]string{"value": returned, "currency": "EUR"}}
			})
			if kind == "recovery owns intent" {
				release, acquired, err := database.TrySessionLock(t.Context(), db, database.NamedLock("checkout-recovery:"+tenant.id+":"+id))
				if err != nil || !acquired {
					t.Fatalf("fixture could not claim intent: %v", err)
				}
				defer func() {
					if err := release(t.Context()); err != nil {
						t.Error(err)
					}
				}()
			}
			ctx := context.WithValue(t.Context(), ctxkeys.KeyUserID, "operator")
			out, err := tenant.svc.RetainDeliveredCheckout(ctx, tenant.id, id, "Historical service attestation")
			if kind == "another tenant refund" {
				if err != nil || out.GetOutcome() != "delivered" {
					t.Fatalf("another tenant's refund blocked attestation: %v %v", out, err)
				}
			} else {
				want := ErrCheckoutDeliveryDecisionNotAllowed
				if kind == "recovery owns intent" {
					want = ErrAwaitingProviderSettlement
				}
				if !errors.Is(err, want) {
					t.Fatalf("unsafe attestation accepted: %v %v", out, err)
				}
				var audits int
				if readErr := db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.checkout_recovery_audit WHERE tenant_id=$1`, tenant.id).Scan(&audits); readErr != nil || audits != 0 {
					t.Fatalf("refused decision wrote audit: %d %v", audits, readErr)
				}
			}
			if len(fixture.matching("POST", "/v2/payments/initial/refunds")) != 0 {
				t.Fatal("delivery attestation moved money")
			}
		})
	}
}

func assertHistoricalMollieMultipleDeliveredPayments(t *testing.T) {
	t.Helper()
	db := startProviderReservationDatabase(t)
	now := time.Now().UTC().Truncate(time.Second)
	tenant := newPhaseTenant(t, db, "prepaid", now.Add(-time.Hour), now.AddDate(0, 1, 0), 0)
	fixture, client := installMollieFixture(t)
	tenant.svc.mollieClient = client
	ids := []string{uuid.NewString(), uuid.NewString()}
	subscriptions := []any{}
	for i, id := range ids {
		created := now.Add(time.Duration(i-3) * 24 * time.Hour)
		nativeAt := created.Add(time.Hour)
		paymentID := fmt.Sprintf("initial-%d", i)
		subscriptionID := fmt.Sprintf("historical-%d", i)
		mandateID := fmt.Sprintf("mandate-%d", i)
		_, err := db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,created_at,succeeded_at) VALUES($1::uuid,$2::uuid,'mollie','mollie_first_payment','succeeded','EUR',2000,$1::text,'customer',$3,$4,$4)`, id, tenant.id, paymentID, created)
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(t.Context(), `INSERT INTO purser.payment_provider_intents(tenant_id,provider,purpose,status,currency,idempotency_key,provider_customer_id,provider_subscription_id,created_at,succeeded_at) VALUES($1,'mollie','mollie_subscription_create','provider_open','EUR',$2,'customer',$2,$3,$3)`, tenant.id, subscriptionID, nativeAt)
		if err != nil {
			t.Fatal(err)
		}
		subscriptions = append(subscriptions, map[string]any{"id": subscriptionID, "mandateId": mandateID, "createdAt": nativeAt.Format(time.RFC3339), "status": "active"})
		fixture.route("GET", "/v2/payments/"+paymentID, func(recordedProviderRequest) (int, any) {
			return 200, map[string]any{"id": paymentID, "customerId": "customer", "mandateId": mandateID, "status": "paid", "sequenceType": "first", "createdAt": created.Format(time.RFC3339), "amount": map[string]string{"value": "20.00", "currency": "EUR"}}
		})
		fixture.route("GET", "/v2/customers/customer/subscriptions/"+subscriptionID, func(recordedProviderRequest) (int, any) {
			return 200, map[string]any{"id": subscriptionID, "status": "active"}
		})
		fixture.route("DELETE", "/v2/customers/customer/subscriptions/"+subscriptionID, func(recordedProviderRequest) (int, any) {
			return 200, map[string]any{"id": subscriptionID, "status": "canceled"}
		})
	}
	fixture.route("GET", "/v2/customers/customer/subscriptions", func(recordedProviderRequest) (int, any) {
		return 200, map[string]any{"_embedded": map[string]any{"subscriptions": subscriptions}, "_links": map[string]any{}}
	})
	event, err := events.New("commodore", tenant.id, tenant.id, &internalv1.TenantAccountClosed{}, events.WithTime(now))
	if err != nil {
		t.Fatal(err)
	}
	key, headers, body, err := events.EncodeRecord(event.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	message := kafka.Message{Key: key, Headers: map[string]string{}, Value: body, Topic: topology.TopicDomainEvents}
	for _, header := range headers {
		message.Headers[header.Key] = string(header.Value)
	}
	if err = tenant.jobs.handleBillingMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	worker := &outbox.Worker[providerWebhookPayload]{Config: providerWebhookWorkerConfig(), Store: &providerWebhookInboxStore{db: db}, Dispatcher: &providerWebhookDispatcher{service: tenant.svc}, Logger: tenant.jobs.logger}
	for range 5 {
		worker.ProcessBatch(t.Context())
	}
	for _, id := range ids {
		var activated sql.NullTime
		if err = db.QueryRowContext(t.Context(), `SELECT activated_at FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant.id, id).Scan(&activated); err != nil || !activated.Valid {
			t.Fatalf("historically delivered receipt stuck after closure: id=%s activated=%v err=%v", id, activated, err)
		}
	}
	for i := range ids {
		if len(fixture.matching("POST", fmt.Sprintf("/v2/payments/initial-%d/refunds", i))) != 0 {
			t.Fatal("closure refunded delivered historical service")
		}
	}
	var pending int
	if err = db.QueryRowContext(t.Context(), `SELECT count(*) FROM purser.provider_webhook_inbox WHERE provider='checkout_recovery' AND headers->>'tenant_id'=$1 AND status<>'processed'`, tenant.id).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("historical closure recovery did not drain: pending=%d err=%v", pending, err)
	}
}
