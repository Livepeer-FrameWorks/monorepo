package stripe

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/stripe/stripe-go/v85"
)

// PaymentMethodTypesForCurrency pins the Checkout payment-method allowlist in
// code so settlement is deterministic. The invariant: SEPA/iDEAL/Bancontact are
// EUR-only at Stripe, so offering them for any other currency would let Stripe
// reject the session. Card is always offered.
func TestPaymentMethodTypesForCurrency(t *testing.T) {
	deref := func(in []*string) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			if s != nil {
				out = append(out, *s)
			}
		}
		return out
	}
	tests := []struct {
		name     string
		currency string
		want     []string
	}{
		{"EUR gets the full EU set", "EUR", []string{"card", "sepa_debit", "ideal", "bancontact"}},
		{"lowercase eur is matched case-insensitively", "eur", []string{"card", "sepa_debit", "ideal", "bancontact"}},
		{"USD is card-only", "USD", []string{"card"}},
		{"GBP is card-only", "GBP", []string{"card"}},
		{"empty currency is card-only", "", []string{"card"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deref(PaymentMethodTypesForCurrency(tt.currency))
			if len(got) != len(tt.want) {
				t.Fatalf("methods = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("methods = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestExtractSubscriptionInfo(t *testing.T) {
	c := &Client{logger: logging.NewLogger()}

	t.Run("full subscription", func(t *testing.T) {
		start := int64(1_700_000_000)
		end := int64(1_702_592_000)
		sub := &stripe.Subscription{
			ID:                "sub_123",
			Customer:          &stripe.Customer{ID: "cus_123"},
			Status:            stripe.SubscriptionStatusActive,
			CancelAtPeriodEnd: true,
			Items: &stripe.SubscriptionItemList{
				Data: []*stripe.SubscriptionItem{
					{CurrentPeriodStart: start, CurrentPeriodEnd: end},
				},
			},
			Metadata: map[string]string{"tenant_id": "tenant-1", "tier_id": "pro"},
		}
		got := c.ExtractSubscriptionInfo(sub)
		if got.StripeSubscriptionID != "sub_123" || got.StripeCustomerID != "cus_123" {
			t.Errorf("ids: %+v", got)
		}
		if got.Status != "active" || !got.CancelAtPeriodEnd {
			t.Errorf("status/cancel: %+v", got)
		}
		if got.TenantID != "tenant-1" || got.TierID != "pro" {
			t.Errorf("metadata: %+v", got)
		}
		if !got.CurrentPeriodStart.Equal(time.Unix(start, 0)) || !got.CurrentPeriodEnd.Equal(time.Unix(end, 0)) {
			t.Errorf("periods: %+v", got)
		}
	})

	t.Run("empty items leaves zero period times and no panic", func(t *testing.T) {
		sub := &stripe.Subscription{
			ID:       "sub_456",
			Customer: &stripe.Customer{ID: "cus_456"},
			Status:   stripe.SubscriptionStatusTrialing,
			Items:    &stripe.SubscriptionItemList{Data: nil},
		}
		got := c.ExtractSubscriptionInfo(sub)
		if !got.CurrentPeriodStart.IsZero() || !got.CurrentPeriodEnd.IsZero() {
			t.Errorf("expected zero period times, got %+v", got)
		}
		if got.TenantID != "" || got.TierID != "" {
			t.Errorf("expected empty metadata, got %+v", got)
		}
	})
}

func TestExtractStripePaymentIntentID(t *testing.T) {
	if got := extractStripePaymentIntentID(nil); got != "" {
		t.Errorf("nil error: got %q", got)
	}
	if got := extractStripePaymentIntentID(&stripe.Error{}); got != "" {
		t.Errorf("nil payment intent: got %q", got)
	}
	withPI := &stripe.Error{PaymentIntent: &stripe.PaymentIntent{ID: "pi_abc"}}
	if got := extractStripePaymentIntentID(withPI); got != "pi_abc" {
		t.Errorf("got %q, want pi_abc", got)
	}
}

// fakeBackend implements stripe.Backend so ChargeOffSession's error
// classification can be exercised without a network round-trip. err is
// returned from Call; otherwise pi is copied into the result resource.
type fakeBackend struct {
	err   error
	pi    *stripe.PaymentIntent
	calls int
}

func (f *fakeBackend) Call(method, path, key string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	if out, ok := v.(*stripe.PaymentIntent); ok && f.pi != nil {
		*out = *f.pi
	}
	return nil
}

func (f *fakeBackend) CallStreaming(method, path, key string, params stripe.ParamsContainer, v stripe.StreamingLastResponseSetter) error {
	return nil
}
func (f *fakeBackend) CallRaw(method, path, key string, body []byte, params *stripe.Params, v stripe.LastResponseSetter) error {
	return nil
}
func (f *fakeBackend) CallMultipart(method, path, key, boundary string, body *bytes.Buffer, params *stripe.Params, v stripe.LastResponseSetter) error {
	return nil
}
func (f *fakeBackend) SetMaxNetworkRetries(maxNetworkRetries int64) {}

func installBackend(t *testing.T, b stripe.Backend) {
	t.Helper()
	prev := stripe.GetBackend(stripe.APIBackend)
	stripe.SetBackend(stripe.APIBackend, b)
	t.Cleanup(func() { stripe.SetBackend(stripe.APIBackend, prev) })
}

func validChargeParams() OffSessionChargeParams {
	return OffSessionChargeParams{
		CustomerID:     "cus_1",
		TenantID:       "tenant-1",
		InvoiceID:      "inv-1",
		AmountCents:    500,
		Currency:       "EUR",
		IdempotencyKey: "charge:inv-1:1",
	}
}

func TestChargeOffSession_InputGuards(t *testing.T) {
	// A bad request must be rejected before any backend call is made.
	be := &fakeBackend{}
	installBackend(t, be)
	c := NewClient(Config{SecretKey: "sk_test_x", Logger: logging.NewLogger()})

	tests := []struct {
		name   string
		mutate func(*OffSessionChargeParams)
	}{
		{"missing idempotency key", func(p *OffSessionChargeParams) { p.IdempotencyKey = "" }},
		{"missing customer id", func(p *OffSessionChargeParams) { p.CustomerID = "" }},
		{"non-positive amount", func(p *OffSessionChargeParams) { p.AmountCents = 0 }},
		{"missing currency", func(p *OffSessionChargeParams) { p.Currency = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validChargeParams()
			tt.mutate(&p)
			if _, err := c.ChargeOffSession(context.Background(), p); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
	if be.calls != 0 {
		t.Errorf("expected no backend calls for invalid input, got %d", be.calls)
	}
}

func TestChargeOffSession_SCARequiredFromAPIError(t *testing.T) {
	// authentication_required is the SCA outcome: the attempt must NOT be
	// marked failed, and the partially-created intent id is recovered from
	// the error so the customer can resume against the same intent.
	installBackend(t, &fakeBackend{err: &stripe.Error{
		Code:          stripe.ErrorCodeAuthenticationRequired,
		Msg:           "authentication required",
		PaymentIntent: &stripe.PaymentIntent{ID: "pi_sca"},
	}})
	c := NewClient(Config{SecretKey: "sk_test_x", Logger: logging.NewLogger()})

	res, err := c.ChargeOffSession(context.Background(), validChargeParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.SCARequired {
		t.Error("expected SCARequired=true")
	}
	if res.Status != "requires_action" {
		t.Errorf("status = %q, want requires_action", res.Status)
	}
	if res.PaymentIntentID != "pi_sca" {
		t.Errorf("payment intent id = %q, want pi_sca", res.PaymentIntentID)
	}
	if res.FailureCode != string(stripe.ErrorCodeAuthenticationRequired) {
		t.Errorf("failure code = %q", res.FailureCode)
	}
}

func TestChargeOffSession_DeclinedIsFailedNotSCA(t *testing.T) {
	installBackend(t, &fakeBackend{err: &stripe.Error{
		Code: stripe.ErrorCodeCardDeclined,
		Msg:  "card declined",
	}})
	c := NewClient(Config{SecretKey: "sk_test_x", Logger: logging.NewLogger()})

	res, err := c.ChargeOffSession(context.Background(), validChargeParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SCARequired {
		t.Error("a decline must not be classified as SCA")
	}
	if res.Status != "failed" {
		t.Errorf("status = %q, want failed", res.Status)
	}
	if res.FailureCode != string(stripe.ErrorCodeCardDeclined) || res.FailureMessage != "card declined" {
		t.Errorf("failure surfaced incorrectly: %+v", res)
	}
}

func TestChargeOffSession_NonStripeErrorPropagates(t *testing.T) {
	// A non-Stripe error (network, etc.) is not a classified outcome — it
	// must bubble up as an error so the caller retries rather than recording
	// a definitive attempt result.
	installBackend(t, &fakeBackend{err: errors.New("connection reset")})
	c := NewClient(Config{SecretKey: "sk_test_x", Logger: logging.NewLogger()})

	res, err := c.ChargeOffSession(context.Background(), validChargeParams())
	if err == nil {
		t.Fatal("expected the raw error to propagate")
	}
	if res != nil {
		t.Errorf("expected nil result on hard error, got %+v", res)
	}
}

func TestChargeOffSession_SuccessRequiresActionSurfacesRedirectURL(t *testing.T) {
	// A synchronous success that still needs action carries the redirect URL
	// the caller hands to the customer.
	installBackend(t, &fakeBackend{pi: &stripe.PaymentIntent{
		ID:             "pi_ok",
		Status:         stripe.PaymentIntentStatusRequiresAction,
		AmountReceived: 0,
		NextAction: &stripe.PaymentIntentNextAction{
			RedirectToURL: &stripe.PaymentIntentNextActionRedirectToURL{URL: "https://stripe.test/auth"},
		},
	}})
	c := NewClient(Config{SecretKey: "sk_test_x", Logger: logging.NewLogger()})

	res, err := c.ChargeOffSession(context.Background(), validChargeParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.SCARequired {
		t.Error("requires_action status must set SCARequired")
	}
	if res.NextActionURL != "https://stripe.test/auth" {
		t.Errorf("next action url = %q", res.NextActionURL)
	}
	if res.PaymentIntentID != "pi_ok" {
		t.Errorf("payment intent id = %q, want pi_ok", res.PaymentIntentID)
	}
}
