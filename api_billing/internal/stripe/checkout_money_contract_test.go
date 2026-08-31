package stripe

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	stripeapi "github.com/stripe/stripe-go/v85"
)

type stripeMoneyBackend struct {
	err             error
	calls           int
	checkoutParams  *stripeapi.CheckoutSessionParams
	subscription    *stripeapi.Subscription
	customer        *stripeapi.Customer
	checkoutSession *stripeapi.CheckoutSession
	price           *stripeapi.Price
}

func (b *stripeMoneyBackend) Call(_ string, _ string, _ string, params stripeapi.ParamsContainer, result stripeapi.LastResponseSetter) error {
	b.calls++
	if b.err != nil {
		return b.err
	}
	if checkoutParams, ok := params.(*stripeapi.CheckoutSessionParams); ok {
		b.checkoutParams = checkoutParams
	}
	switch output := result.(type) {
	case *stripeapi.CheckoutSession:
		if b.checkoutSession != nil {
			*output = *b.checkoutSession
		}
	case *stripeapi.Subscription:
		if b.subscription != nil {
			*output = *b.subscription
		}
	case *stripeapi.Customer:
		if b.customer != nil {
			*output = *b.customer
		}
	case *stripeapi.Price:
		if b.price != nil {
			*output = *b.price
		}
	}
	return nil
}

func (*stripeMoneyBackend) CallStreaming(string, string, string, stripeapi.ParamsContainer, stripeapi.StreamingLastResponseSetter) error {
	return nil
}
func (*stripeMoneyBackend) CallRaw(string, string, string, []byte, *stripeapi.Params, stripeapi.LastResponseSetter) error {
	return nil
}
func (*stripeMoneyBackend) CallMultipart(string, string, string, string, *bytes.Buffer, *stripeapi.Params, stripeapi.LastResponseSetter) error {
	return nil
}
func (*stripeMoneyBackend) SetMaxNetworkRetries(int64) {}

func TestCreateStripeCheckoutRequiresIdempotencyBeforeIO(t *testing.T) {
	backend := &stripeMoneyBackend{}
	installBackend(t, backend)
	client := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})
	if _, err := client.CreateCheckoutSession(context.Background(), CheckoutSessionParams{}); err == nil {
		t.Fatal("missing idempotency key accepted")
	}
	if backend.calls != 0 {
		t.Fatalf("invalid request made %d provider calls", backend.calls)
	}
}

func TestCreateStripeCheckoutBindsCommercialIdentityToSessionAndSubscription(t *testing.T) {
	backend := &stripeMoneyBackend{checkoutSession: &stripeapi.CheckoutSession{ID: "cs_1", URL: "https://checkout.test/cs_1"}}
	installBackend(t, backend)
	client := NewClient(Config{SecretKey: "sk_test", Logger: logging.NewLogger()})

	session, err := client.CreateCheckoutSession(context.Background(), CheckoutSessionParams{
		CustomerID: "cus_1", TenantID: "tenant-1", TierID: "pro", Purpose: "cluster_subscription",
		ReferenceID: "cluster-1", ClusterID: "cluster-1", PriceID: "price_1", Currency: "EUR",
		SuccessURL: "https://app/success", CancelURL: "https://app/cancel", TrialDays: 14,
		IdempotencyKey: "checkout:tenant-1:cluster-1",
	})
	if err != nil || session.ID != "cs_1" {
		t.Fatalf("session = %+v, err=%v", session, err)
	}
	params := backend.checkoutParams
	if params == nil {
		t.Fatal("checkout params not captured")
	}
	if params.IdempotencyKey == nil || *params.IdempotencyKey != "checkout:tenant-1:cluster-1" || params.Customer == nil || *params.Customer != "cus_1" || params.Mode == nil || *params.Mode != "subscription" {
		t.Fatalf("identity params = %+v", params)
	}
	wantMetadata := map[string]string{"tenant_id": "tenant-1", "tier_id": "pro", "purpose": "cluster_subscription", "reference_id": "cluster-1", "cluster_id": "cluster-1"}
	for key, want := range wantMetadata {
		if params.Metadata[key] != want || params.SubscriptionData.Metadata[key] != want {
			t.Fatalf("metadata %q session/subscription = %q/%q, want %q", key, params.Metadata[key], params.SubscriptionData.Metadata[key], want)
		}
	}
	if params.SubscriptionData.TrialPeriodDays == nil || *params.SubscriptionData.TrialPeriodDays != 14 {
		t.Fatalf("trial days = %+v", params.SubscriptionData.TrialPeriodDays)
	}
	if len(params.LineItems) != 1 || params.LineItems[0].Price == nil || *params.LineItems[0].Price != "price_1" || params.LineItems[0].Quantity == nil || *params.LineItems[0].Quantity != 1 {
		t.Fatalf("line items = %+v", params.LineItems)
	}
	if len(params.PaymentMethodTypes) != 4 {
		t.Fatalf("EUR payment methods = %d", len(params.PaymentMethodTypes))
	}
}

func TestResolveStripeDefaultPaymentMethodUsesExplicitPrecedence(t *testing.T) {
	t.Run("subscription wins", func(t *testing.T) {
		backend := &stripeMoneyBackend{subscription: &stripeapi.Subscription{DefaultPaymentMethod: &stripeapi.PaymentMethod{ID: "pm_subscription"}}}
		installBackend(t, backend)
		got, err := newStripeClient().ResolveDefaultPaymentMethod(context.Background(), "cus_1", "sub_1")
		if err != nil || got != "pm_subscription" || backend.calls != 1 {
			t.Fatalf("method=%q calls=%d err=%v", got, backend.calls, err)
		}
	})
	t.Run("customer fallback", func(t *testing.T) {
		backend := &stripeMoneyBackend{
			subscription: &stripeapi.Subscription{},
			customer:     &stripeapi.Customer{InvoiceSettings: &stripeapi.CustomerInvoiceSettings{DefaultPaymentMethod: &stripeapi.PaymentMethod{ID: "pm_customer"}}},
		}
		installBackend(t, backend)
		got, err := newStripeClient().ResolveDefaultPaymentMethod(context.Background(), "cus_1", "sub_1")
		if err != nil || got != "pm_customer" || backend.calls != 2 {
			t.Fatalf("method=%q calls=%d err=%v", got, backend.calls, err)
		}
	})
	t.Run("none is explicit", func(t *testing.T) {
		backend := &stripeMoneyBackend{subscription: &stripeapi.Subscription{}, customer: &stripeapi.Customer{}}
		installBackend(t, backend)
		got, err := newStripeClient().ResolveDefaultPaymentMethod(context.Background(), "cus_1", "sub_1")
		if err != nil || got != "" {
			t.Fatalf("method=%q err=%v", got, err)
		}
	})
	t.Run("missing provider ids rejected", func(t *testing.T) {
		backend := &stripeMoneyBackend{}
		installBackend(t, backend)
		for _, ids := range [][2]string{{"", "sub_1"}, {"cus_1", ""}, {"", ""}} {
			if _, err := newStripeClient().ResolveDefaultPaymentMethod(context.Background(), ids[0], ids[1]); err == nil {
				t.Fatalf("ids %q/%q accepted", ids[0], ids[1])
			}
		}
		if backend.calls != 0 {
			t.Fatalf("invalid ids made %d provider calls", backend.calls)
		}
	})
}

func TestStripeProviderCancellationOperationsAreNoOpsWithoutProviderIDs(t *testing.T) {
	backend := &stripeMoneyBackend{}
	installBackend(t, backend)
	client := newStripeClient()
	if err := client.ExpireCheckoutSession(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if err := client.DeactivatePrice(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if backend.calls != 0 {
		t.Fatalf("blank ids made %d provider calls", backend.calls)
	}
}

func TestStripeProviderMutationsWrapTransportErrors(t *testing.T) {
	backend := &stripeMoneyBackend{err: errors.New("provider unavailable")}
	installBackend(t, backend)
	client := newStripeClient()
	if err := client.ExpireCheckoutSession(context.Background(), "cs_1"); err == nil {
		t.Fatal("checkout expiration transport error was swallowed")
	}
	if err := client.DeactivatePrice(context.Background(), "price_1"); err == nil {
		t.Fatal("price deactivation transport error was swallowed")
	}
}
