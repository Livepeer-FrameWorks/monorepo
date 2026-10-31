//go:build schema_verify

package handlers

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/google/uuid"
)

func assertClosureRefundsOnlyUnactivatedFirstPayment(t *testing.T) {
	t.Helper()
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
