package stripe

import (
	"fmt"
	"testing"

	api "github.com/stripe/stripe-go/v85"
)

type terminalRecoveryBackend struct {
	stripeMoneyBackend
	direct    bool
	forbidden int
}

func (b *terminalRecoveryBackend) Call(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
	if method == "DELETE" || path == "/v1/refunds" {
		b.forbidden++
		return fmt.Errorf("terminal provider object must not be cancelled or refunded again")
	}
	switch v := out.(type) {
	case *api.CheckoutSession:
		*v = api.CheckoutSession{ID: "cs_terminal", Status: api.CheckoutSessionStatusComplete, Subscription: &api.Subscription{ID: "sub_terminal"}}
	case *api.Subscription:
		st := api.SubscriptionStatusIncompleteExpired
		if b.direct {
			st = api.SubscriptionStatusCanceled
		}
		*v = api.Subscription{ID: "sub_terminal", Status: st}
	case *api.Charge:
		*v = api.Charge{ID: "ch_terminal", Amount: 2000, AmountRefunded: 2000, Refunded: true}
	case *api.PaymentIntent:
		*v = api.PaymentIntent{ID: "pi_terminal", LatestCharge: &api.Charge{Amount: 2000, AmountRefunded: 2000}}
	}
	return nil
}

func (b *terminalRecoveryBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	switch v := out.(type) {
	case *api.InvoiceList:
		v.Data = []*api.Invoice{{ID: "invoice", AmountPaid: 2000}}
	case *api.InvoicePaymentList:
		payment := &api.InvoicePaymentPayment{PaymentIntent: &api.PaymentIntent{ID: "pi_terminal"}}
		if b.direct {
			payment = &api.InvoicePaymentPayment{Charge: &api.Charge{ID: "ch_terminal"}}
		}
		v.Data = []*api.InvoicePayment{{ID: "paid", Payment: payment}}
	}
	return nil
}

func TestRecoveryTreatsTerminalSubscriptionAndRefundAsComplete(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			b := &terminalRecoveryBackend{direct: direct}
			installBackend(t, b)
			c := NewClient(Config{SecretKey: "sk_test"})
			if err := c.CancelReservedCheckout(t.Context(), "customer", "intent", "cs_terminal"); err != nil {
				t.Fatal(err)
			}
			if b.forbidden != 0 {
				t.Fatal("repeated terminal financial action")
			}
		})
	}
}
