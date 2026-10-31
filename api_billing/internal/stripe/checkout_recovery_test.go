package stripe

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	api "github.com/stripe/stripe-go/v85"
)

type checkoutRecoveryBackend struct {
	stripeMoneyBackend
	paths           []string
	paid, refunded  bool
	includeRenewals bool
	cancelParams    *api.SubscriptionCancelParams
}

func (b *checkoutRecoveryBackend) Call(method, path, key string, p api.ParamsContainer, result api.LastResponseSetter) error {
	b.paths = append(b.paths, method+" "+path)
	if method == "DELETE" {
		b.cancelParams, _ = p.(*api.SubscriptionCancelParams)
	}
	switch v := result.(type) {
	case *api.CheckoutSession:
		*v = api.CheckoutSession{ID: "cs_reserved", Status: api.CheckoutSessionStatusOpen}
		if b.paid {
			v.Status = api.CheckoutSessionStatusComplete
			v.Subscription = &api.Subscription{ID: "sub_reserved"}
		}
	case *api.Subscription:
		*v = api.Subscription{ID: "sub_reserved", Status: api.SubscriptionStatusActive}
	case *api.PaymentIntent:
		*v = api.PaymentIntent{ID: "pi_reserved", LatestCharge: &api.Charge{ID: "ch_reserved", Currency: api.CurrencyEUR, Amount: 2000}}
		if b.refunded {
			v.LatestCharge.AmountRefunded = 2000
		}
	case *api.Refund:
		b.refunded = true
	}
	return nil
}

func (b *checkoutRecoveryBackend) CallRaw(method, path, key string, body []byte, p *api.Params, result api.LastResponseSetter) error {
	b.paths = append(b.paths, method+" "+path)
	switch v := result.(type) {
	case *api.CheckoutSessionList:
		own := &api.CheckoutSession{ID: "cs_reserved", Status: api.CheckoutSessionStatusOpen, Metadata: map[string]string{"checkout_reservation_id": "reservation"}}
		if b.paid {
			own.Status = api.CheckoutSessionStatusComplete
			own.Subscription = &api.Subscription{ID: "sub_reserved"}
		}
		v.Data = []*api.CheckoutSession{{ID: "cs_other", Status: api.CheckoutSessionStatusOpen, Metadata: map[string]string{"checkout_reservation_id": "other"}}, own}
	case *api.InvoiceList:
		v.Data = []*api.Invoice{{ID: "in_reserved", AmountPaid: 2000, BillingReason: api.InvoiceBillingReasonSubscriptionCreate}}
		if b.includeRenewals {
			v.Data = append(v.Data, &api.Invoice{ID: "in_renewal", AmountPaid: 2000, BillingReason: api.InvoiceBillingReasonSubscriptionCycle})
		}
	case *api.InvoicePaymentList:
		v.Data = []*api.InvoicePayment{{ID: "ip_reserved", Payment: &api.InvoicePaymentPayment{PaymentIntent: &api.PaymentIntent{ID: "pi_reserved"}}}}
	}
	return nil
}

func TestExpiredStripeReservationRecoversLostSessionBinding(t *testing.T) {
	b := &checkoutRecoveryBackend{}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if err := c.CancelReservedCheckout(context.Background(), "cus_reserved", "reservation", ""); err != nil {
		t.Fatal(err)
	}
	expired := false
	for _, path := range b.paths {
		if path == "POST /v1/checkout/sessions/cs_reserved/expire" {
			expired = true
		}
		if path == "POST /v1/checkout/sessions/cs_other/expire" {
			t.Fatal("cancelled another reservation")
		}
	}
	if !expired {
		t.Fatalf("lost binding was not expired: %v", b.paths)
	}
}

func TestExpiredStripeReservationRefundsUnactivatedSubscription(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	for range 2 {
		if err := c.CancelReservedCheckout(context.Background(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
			t.Fatal(err)
		}
	}
	refunds, cancels := 0, 0
	for _, path := range b.paths {
		if path == "POST /v1/refunds" {
			refunds++
		}
		if path == "DELETE /v1/subscriptions/sub_reserved" {
			cancels++
		}
	}
	if refunds != 1 || cancels == 0 {
		t.Fatalf("compensation not idempotent: refunds=%d cancels=%d paths=%v", refunds, cancels, b.paths)
	}
}

func TestStripeCheckoutUsesReservationDeadline(t *testing.T) {
	b := &stripeMoneyBackend{}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	deadline := time.Now().Add(time.Hour).Truncate(time.Second)
	if _, err := c.CreateCheckoutSession(context.Background(), CheckoutSessionParams{TenantID: "tenant", ReservationID: "reservation", ExpiresAt: deadline, IdempotencyKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if b.checkoutParams.ExpiresAt == nil || *b.checkoutParams.ExpiresAt != deadline.Unix() || b.checkoutParams.Metadata["checkout_reservation_id"] != "reservation" {
		t.Fatalf("provider deadline/identity missing: %+v", b.checkoutParams)
	}
	if _, err := c.CreateSetupCheckoutSession(context.Background(), SetupCheckoutParams{CustomerID: "customer", Currency: "USD", TenantID: "tenant", ReservationID: "reservation", ExpiresAt: deadline, IdempotencyKey: "key"}); err != nil {
		t.Fatal(err)
	}
	if b.checkoutParams.ExpiresAt == nil || *b.checkoutParams.ExpiresAt != deadline.Unix() || b.checkoutParams.Metadata["checkout_reservation_id"] != "reservation" {
		t.Fatal("setup checkout omitted reservation deadline")
	}
}

func TestCheckoutRetryResumesExistingProviderSession(t *testing.T) {
	b := &checkoutRecoveryBackend{}
	old := api.GetBackend(api.APIBackend)
	api.SetBackend(api.APIBackend, b)
	t.Cleanup(func() { api.SetBackend(api.APIBackend, old) })
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	for _, setup := range []bool{false, true} {
		var err error
		if setup {
			_, err = c.CreateSetupCheckoutSession(t.Context(), SetupCheckoutParams{CustomerID: "customer", ReservationID: "reservation", ExistingSessionID: "cs_reserved", ResumeOnly: true})
		} else {
			_, err = c.CreateCheckoutSession(t.Context(), CheckoutSessionParams{CustomerID: "customer", ReservationID: "reservation", ExistingSessionID: "cs_reserved", ResumeOnly: true})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(b.paths) != 2 {
		t.Fatalf("unexpected provider calls: %v", b.paths)
	}
	for _, path := range b.paths {
		if path != "GET /v1/checkout/sessions/cs_reserved" {
			t.Fatalf("checkout retry created another subscription: %s", path)
		}
	}
}

func TestUnboundCheckoutRetryReplaysWithinProviderWindow(t *testing.T) {
	for _, setup := range []bool{false, true} {
		t.Run(fmt.Sprint("setup=", setup), func(t *testing.T) {
			b := &stripeMoneyBackend{checkoutSession: &api.CheckoutSession{ID: "same-session", Status: "open"}}
			installBackend(t, b)
			c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
			var err error
			if setup {
				_, err = c.CreateSetupCheckoutSession(t.Context(), SetupCheckoutParams{CustomerID: "customer", Currency: "USD", ReservationID: "reservation", IdempotencyKey: "original-key", ResumeOnly: true, ReplayBefore: time.Now().Add(time.Minute)})
			} else {
				_, err = c.CreateCheckoutSession(t.Context(), CheckoutSessionParams{CustomerID: "customer", Currency: "EUR", PriceID: "price", ReservationID: "reservation", IdempotencyKey: "original-key", ResumeOnly: true, ReplayBefore: time.Now().Add(time.Minute)})
			}
			if err != nil || b.checkoutParams == nil || *b.checkoutParams.IdempotencyKey != "original-key" {
				t.Fatalf("unbound attempt was not replayed with original identity: params=%v err=%v", b.checkoutParams, err)
			}
		})
	}
}

func TestRecoveryRefundsCheckoutInvoiceOnly(t *testing.T) {
	b := &checkoutRecoveryBackend{paid: true, includeRenewals: true}
	installBackend(t, b)
	c := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := c.RecoverReservedCheckout(t.Context(), "cus_reserved", "reservation", "cs_reserved"); err != nil {
		t.Fatal(err)
	}
	for _, path := range b.paths {
		if path == "GET /v1/invoice_payments" {
			continue
		}
		if strings.Contains(path, "in_renewal") {
			t.Fatalf("recovery read delivered service: %s", path)
		}
	}
	reads := 0
	for _, path := range b.paths {
		if path == "GET /v1/invoice_payments" {
			reads++
		}
	}
	if reads != 1 {
		t.Fatalf("recovery reached %d invoice payments; only checkout invoice may be refunded", reads)
	}
}
