//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"frameworks/api_billing/internal/handlers"
	billingmollie "frameworks/api_billing/internal/mollie"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	mollielib "github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type retainedFundingMollieClient struct {
	fundingRaceMollieClient
	payments int
	pending  bool
}

func (c *retainedFundingMollieClient) CreateFirstPayment(context.Context, billingmollie.FirstPaymentParams) (*mollielib.Payment, error) {
	c.payments++
	var payment mollielib.Payment
	err := json.Unmarshal([]byte(`{"id":"duplicate-payment","_links":{"checkout":{"href":"https://example.test/pay-again"}}}`), &payment)
	payment.ID = "new-" + uuid.NewString()
	return &payment, err
}

func (c *retainedFundingMollieClient) GetMandate(_ context.Context, _ string, id string) (*mollielib.Mandate, error) {
	state := "valid"
	if c.pending {
		state = "pending"
	}
	return &mollielib.Mandate{ID: id, Status: mollielib.MandateStatus(state)}, nil
}

func (c *retainedFundingMollieClient) ListMandates(ctx context.Context, customer string) ([]*mollielib.Mandate, error) {
	mandate, err := c.GetMandate(ctx, customer, "mandate")
	return []*mollielib.Mandate{mandate}, err
}

type retainedFundingTransport func(*http.Request) (*http.Response, error)

func (f retainedFundingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise the public payment/activation entry points and the real cancellation
// worker together: a repairable refusal must not require a second payment.
func assertMollieRetainedFundingRetry(t *testing.T) {
	for _, cause := range []string{"expired native checkout", "pending explicit mandate", "pending selected mandate"} {
		t.Run(cause, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, tier, otherTier := seedFallbackLifecycle(t, db)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_at=NULL,free_fallback_provider_ended_at=NULL,status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant)
			exec(`UPDATE purser.billing_tiers SET base_price=30,tier_level=2 WHERE id=$1`, otherTier)
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1,'customer')`, tenant)
			funding := uuid.NewString()
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,local_reference_type,local_reference_id,status,currency,amount_cents,idempotency_key,provider_customer_id,provider_payment_id,provider_mandate_id,succeeded_at,expires_at) VALUES($1::uuid,$2,'mollie','mollie_first_payment','billing_tiers',$3,'succeeded','EUR',2000,$1::text,'customer','original-payment','mandate',NOW(),NOW()-INTERVAL '1 hour')`, funding, tenant, tier)
			client := &retainedFundingMollieClient{}
			client.subscription = &mollielib.Subscription{ID: "native-refused", Status: "active"}
			client.pending = cause != "expired native checkout"
			if !client.pending {
				client.onCreate = func() {
					exec(`UPDATE purser.payment_provider_intents SET expires_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND purpose='mollie_subscription_create'`, tenant)
				}
			}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			activation := &purserpb.CreateMollieSubscriptionRequest{TenantId: tenant, TierId: tier, MandateId: "mandate"}
			if cause == "pending selected mandate" {
				activation.MandateId = ""
			}
			_, err := server.CreateMollieSubscription(t.Context(), activation)
			if status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("initial refusal=%v", err)
			}
			if !strings.Contains(status.Convert(err).Message(), "retry activation") {
				t.Errorf("repairable refusal asks for another payment: %v", err)
			}
			if cause == "expired native checkout" {
				var nativeID string
				if err = db.QueryRow(`SELECT id FROM purser.payment_provider_intents WHERE tenant_id=$1 AND purpose='mollie_subscription_create'`, tenant).Scan(&nativeID); err != nil {
					t.Fatal(err)
				}
				var cancellations int
				cleanupClient, clientErr := billingmollie.NewClient(billingmollie.Config{APIKey: "test_retained", Logger: logging.NewLogger(), HTTPClient: &http.Client{Transport: retainedFundingTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path != "/v2/customers/customer/subscriptions/native-refused" {
						return nil, fmt.Errorf("unexpected provider cleanup request: %s %s", r.Method, r.URL.Path)
					}
					if r.Method == http.MethodDelete {
						cancellations++
					} else if r.Method != http.MethodGet {
						return nil, fmt.Errorf("unexpected cleanup method: %s", r.Method)
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/hal+json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"native-refused","status":"active"}`))}, nil
				})}})
				if clientErr != nil {
					t.Fatal(clientErr)
				}
				service := handlers.NewService(db, logging.NewLogger(), nil, nil, cleanupClient, nil, nil)
				out, cleanupErr := service.RecoverCheckoutResult(operatorAssignCtx(uuid.NewString()), tenant, nativeID, "retry refused activation")
				if cleanupErr != nil || out.Outcome != "cancelled" || cancellations != 1 {
					t.Fatalf("native cleanup=%+v cancellations=%d err=%v", out, cancellations, cleanupErr)
				}
			}
			for _, target := range []string{tier, otherTier} {
				_, err = server.CreateFirstPayment(t.Context(), &purserpb.CreateMollieFirstPaymentRequest{TenantId: tenant, TierId: target, Method: "creditcard", RedirectUrl: "https://example.test/return"})
				if status.Code(err) != codes.FailedPrecondition || !strings.Contains(status.Convert(err).Message(), "retry activation") || client.payments != 0 {
					t.Fatalf("paid funding accepted another payment: target=%s payments=%d err=%v", target, client.payments, err)
				}
			}
			client.pending, client.onCreate = false, nil
			client.subscription = &mollielib.Subscription{ID: "native-activated", Status: "active"}
			out, err := server.CreateMollieSubscription(t.Context(), activation)
			if err != nil || out.GetSubscriptionId() != "native-activated" {
				t.Fatalf("retry did not activate retained funding: out=%+v err=%v", out, err)
			}
			var activated, recovering sql.NullTime
			var linked, payments int
			if err = db.QueryRow(`SELECT activated_at,recovery_started_at,(SELECT count(*) FROM purser.payment_provider_intents n WHERE n.tenant_id=$1 AND n.funding_intent_id=$2 AND n.activated_at IS NOT NULL),(SELECT count(*) FROM purser.payment_provider_intents p WHERE p.tenant_id=$1 AND p.purpose='mollie_first_payment') FROM purser.payment_provider_intents WHERE tenant_id=$1 AND id=$2`, tenant, funding).Scan(&activated, &recovering, &linked, &payments); err != nil {
				t.Fatal(err)
			}
			if !activated.Valid || recovering.Valid || linked != 1 || payments != 1 || client.payments != 0 {
				t.Fatalf("original funding not consumed exactly once: activated=%v recovering=%v links=%d payments=%d provider_payments=%d", activated, recovering, linked, payments, client.payments)
			}
		})
	}
}

func assertMollieRetainedFundingControls(t *testing.T) {
	for _, disposition := range []string{"used", "refunded", "other tenant", "zero amount", "retained other customer", "refund pending"} {
		t.Run(disposition, func(t *testing.T) {
			db := startOperatorGrantDatabase(t)
			tenant, tier, _ := seedFallbackLifecycle(t, db)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE purser.tenant_subscriptions SET stripe_subscription_id=NULL,stripe_customer_id=NULL,free_fallback_at=NULL,free_fallback_provider_ended_at=NULL,status='active',billing_name='Customer',billing_email='billing@example.test',billing_address='{"street":"Main 1","city":"Amsterdam","postal_code":"1000AA","country":"NL"}' WHERE tenant_id=$1`, tenant)
			exec(`INSERT INTO purser.mollie_customers(tenant_id,mollie_customer_id) VALUES($1::text::uuid,'customer-'||$1::text)`, tenant)
			funding := uuid.NewString()
			exec(`INSERT INTO purser.payment_provider_intents(id,tenant_id,provider,purpose,status,currency,amount_cents,idempotency_key,provider_payment_id,provider_customer_id,provider_mandate_id) VALUES($1::uuid,$2,'mollie','mollie_first_payment','succeeded','EUR',2000,$1::text,'initial-'||$1::text,'old-customer','mandate')`, funding, tenant)
			switch disposition {
			case "used":
				exec(`UPDATE purser.payment_provider_intents SET activated_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			case "refunded":
				exec(`UPDATE purser.payment_provider_intents SET status='expired',recovery_started_at=NOW(),recovery_outcome='refunded' WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			case "other tenant":
				exec(`UPDATE purser.payment_provider_intents SET tenant_id=$3 WHERE tenant_id=$1 AND id=$2`, tenant, funding, uuid.NewString())
			case "zero amount":
				exec(`UPDATE purser.payment_provider_intents SET amount_cents=0 WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			case "refund pending":
				exec(`UPDATE purser.payment_provider_intents SET recovery_started_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenant, funding)
			}
			client := &retainedFundingMollieClient{}
			server := &PurserServer{db: db, logger: logging.NewLogger(), mollieClient: client}
			_, err := server.CreateFirstPayment(t.Context(), &purserpb.CreateMollieFirstPaymentRequest{TenantId: tenant, TierId: tier, Method: "creditcard", RedirectUrl: "https://example.test/return"})
			blocked := disposition == "retained other customer" || disposition == "refund pending"
			if blocked {
				if status.Code(err) != codes.FailedPrecondition || client.payments != 0 {
					t.Fatalf("retained funding charged again: calls=%d err=%v", client.payments, err)
				}
			} else if err != nil || client.payments != 1 {
				t.Fatalf("settled, foreign or zero funding blocks a legitimate payment: calls=%d err=%v", client.payments, err)
			}
		})
	}
}
