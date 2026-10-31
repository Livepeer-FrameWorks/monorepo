package stripe

import (
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
)

type fallbackEndBackend struct {
	refunded bool
	credit   bool
	fakeBackend
	status        api.SubscriptionStatus
	customer      string
	invoiceStatus api.InvoiceStatus
	intentStatus  api.PaymentIntentStatus
	paidOnCancel  bool
	cancelErr     error
	cancels       int
	end           time.Time
}

func (b *fallbackEndBackend) Call(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
	switch v := out.(type) {
	case *api.Subscription:
		if method == "DELETE" {
			b.cancels++
			params := p.(*api.SubscriptionCancelParams)
			if api.BoolValue(params.Prorate) || api.BoolValue(params.InvoiceNow) {
				return errors.New("cancellation tried to invoice or prorate")
			}
			if b.cancelErr != nil {
				return b.cancelErr
			}
			b.status = api.SubscriptionStatusCanceled
			if b.paidOnCancel {
				b.invoiceStatus = api.InvoiceStatusPaid
			}
		}
		*v = api.Subscription{ID: "sub", Customer: &api.Customer{ID: b.customer}, Status: b.status, LatestInvoice: &api.Invoice{ID: "invoice", Status: b.invoiceStatus}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{Price: &api.Price{ID: "price"}}}}}
	case *api.Invoice:
		*v = api.Invoice{ID: "invoice", Status: b.invoiceStatus, PeriodEnd: b.end.AddDate(0, -1, 0).Unix(), Lines: &api.InvoiceLineItemList{Data: []*api.InvoiceLineItem{
			{Amount: 2000, Period: &api.Period{End: b.end.Unix()}, Pricing: &api.InvoiceLineItemPricing{PriceDetails: &api.InvoiceLineItemPricingPriceDetails{Price: &api.Price{ID: "price"}}}},
			{Amount: 1000, Period: &api.Period{End: b.end.AddDate(0, 2, 0).Unix()}, Pricing: &api.InvoiceLineItemPricing{PriceDetails: &api.InvoiceLineItemPricingPriceDetails{Price: &api.Price{ID: "unrelated"}}}},
		}}}
		if b.invoiceStatus == api.InvoiceStatusPaid {
			v.AmountPaid = 2000
		}
		if b.credit {
			v.AmountPaid = 0
			v.AmountDue = 0
			v.Total = 2000
			v.StartingBalance = -2000
			v.EndingBalance = 0
		}
	case *api.PaymentIntent:
		*v = api.PaymentIntent{ID: "intent", Status: b.intentStatus, LatestCharge: &api.Charge{ID: "charge", Amount: 2000, Refunded: b.refunded}}
	default:
		return errors.New("fallback attempted an unrelated mutation or refund")
	}
	return nil
}
func (b *fallbackEndBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	if v, ok := out.(*api.InvoicePaymentList); ok {
		v.Data = []*api.InvoicePayment{{Payment: &api.InvoicePaymentPayment{PaymentIntent: &api.PaymentIntent{ID: "intent"}}}}
		return nil
	}
	return errors.New("unexpected list during fallback")
}

func TestStripeFallbackEndsOnlyFutureCollection(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second).AddDate(0, 1, 0)
	for _, tc := range []struct {
		name                             string
		status                           api.SubscriptionStatus
		invoice                          api.InvoiceStatus
		intent                           api.PaymentIntentStatus
		recovered, pending, paidOnCancel bool
		cancels                          int
		fail                             bool
		customer                         string
	}{
		{name: "unpaid", status: api.SubscriptionStatusUnpaid, invoice: api.InvoiceStatusOpen, intent: api.PaymentIntentStatusRequiresPaymentMethod, cancels: 1},
		{name: "payment recovers", status: api.SubscriptionStatusActive, invoice: api.InvoiceStatusPaid, recovered: true},
		{name: "paid during cancellation", status: api.SubscriptionStatusUnpaid, invoice: api.InvoiceStatusOpen, paidOnCancel: true, cancels: 1},
		{name: "bank settlement", status: api.SubscriptionStatusUnpaid, invoice: api.InvoiceStatusOpen, intent: api.PaymentIntentStatusProcessing, pending: true, cancels: 1},
		{name: "already cancelled paid term", status: api.SubscriptionStatusCanceled, invoice: api.InvoiceStatusPaid},
		{name: "cancel failure", status: api.SubscriptionStatusUnpaid, invoice: api.InvoiceStatusOpen, cancels: 1, fail: true},
		{name: "different customer", status: api.SubscriptionStatusUnpaid, invoice: api.InvoiceStatusOpen, customer: "other", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fallbackEndBackend{status: tc.status, customer: "customer", invoiceStatus: tc.invoice, intentStatus: tc.intent, paidOnCancel: tc.paidOnCancel, end: end}
			if tc.customer != "" {
				b.customer = tc.customer
			}
			if tc.fail && tc.customer == "" {
				b.cancelErr = errors.New("provider unavailable")
			}
			installBackend(t, b)
			got, err := newStripeClient().EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{AllowRecovery: true, CustomerID: "customer", SubscriptionID: "sub"})
			if (err != nil) != tc.fail || b.cancels != tc.cancels {
				t.Fatalf("result=%+v cancels=%d err=%v", got, b.cancels, err)
			}
			if tc.fail {
				return
			}
			if got.Recovered != tc.recovered || got.Pending != tc.pending {
				t.Fatalf("outcome=%+v", got)
			}
			wantPaid := !tc.recovered && (tc.paidOnCancel || tc.invoice == api.InvoiceStatusPaid)
			if wantPaid && !got.PaidUntil.Equal(end) || !wantPaid && !got.PaidUntil.IsZero() {
				t.Fatalf("paid term=%s, want retained=%v through %s", got.PaidUntil, wantPaid, end)
			}
		})
	}
}

func TestStripeFallbackCannotRecoverOrExtendRefundedFee(t *testing.T) {
	for _, state := range []api.SubscriptionStatus{api.SubscriptionStatusActive, api.SubscriptionStatusCanceled} {
		t.Run(string(state), func(t *testing.T) {
			b := &fallbackEndBackend{status: state, customer: "customer", invoiceStatus: api.InvoiceStatusPaid, refunded: true, end: time.Now().AddDate(0, 1, 0)}
			installBackend(t, b)
			got, err := newStripeClient().EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{AllowRecovery: true, CustomerID: "customer", SubscriptionID: "sub"})
			if err != nil || got.Recovered || !got.PaidUntil.IsZero() {
				t.Fatalf("refunded invoice extended service: %+v %v", got, err)
			}
		})
	}
}

func TestStripeFallbackKeepsCreditFundedPaidTerm(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second).AddDate(0, 1, 0)
	b := &fallbackEndBackend{status: api.SubscriptionStatusCanceled, customer: "customer", invoiceStatus: api.InvoiceStatusPaid, credit: true, end: end}
	installBackend(t, b)
	got, err := newStripeClient().EndSubscriptionForFallback(t.Context(), billing.SubscriptionEndRequest{CustomerID: "customer", SubscriptionID: "sub"})
	if err != nil || !got.PaidUntil.Equal(end) {
		t.Fatalf("credit funded month lost: %+v %v", got, err)
	}
}
