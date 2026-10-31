package stripe

import (
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
)

type tierUpgradeBackend struct {
	subBackend
	current                    *api.Subscription
	updates                    int
	params                     *api.SubscriptionParams
	loseResponse               bool
	originalStart, originalEnd int64
	feeRefunded                int64
	feeDisputed                bool
}

func (b *tierUpgradeBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if ch, ok := out.(*api.Charge); ok {
		*ch = api.Charge{ID: "old-charge", Amount: 2000, AmountRefunded: b.feeRefunded, Disputed: b.feeDisputed}
		return nil
	}
	if price, ok := out.(*api.Price); ok {
		*price = api.Price{ID: "new-price", Active: true, UnitAmount: 5000, Currency: api.CurrencyEUR, Recurring: &api.PriceRecurring{Interval: api.PriceRecurringIntervalMonth, IntervalCount: 1}}
		return nil
	}
	if method == "POST" {
		b.updates++
		b.params = params.(*api.SubscriptionParams)
		b.current.Metadata = b.params.Metadata
		b.current.Items.Data[0].Price = &api.Price{ID: *b.params.Items[0].Price}
		b.current.Items.Data[0].CurrentPeriodStart = time.Now().Unix()
		b.current.Items.Data[0].CurrentPeriodEnd = time.Now().AddDate(0, 1, 0).Unix()
		b.originalStart = b.current.Items.Data[0].CurrentPeriodStart
		b.originalEnd = b.current.Items.Data[0].CurrentPeriodEnd
		b.current.LatestInvoice = &api.Invoice{ID: "upgrade-invoice", Status: api.InvoiceStatusPaid}
		if b.loseResponse {
			return errors.New("provider committed but response was lost")
		}
	}
	*out.(*api.Subscription) = *b.current
	return nil
}

func (b *tierUpgradeBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	switch v := out.(type) {
	case *api.InvoiceList:
		v.Data = []*api.Invoice{
			{ID: "renewal", BillingReason: api.InvoiceBillingReasonSubscriptionCycle},
			{ID: "upgrade-invoice", BillingReason: api.InvoiceBillingReasonSubscriptionUpdate, Parent: &api.InvoiceParent{SubscriptionDetails: &api.InvoiceParentSubscriptionDetails{Metadata: b.current.Metadata}}, Lines: &api.InvoiceLineItemList{Data: []*api.InvoiceLineItem{{Amount: 5000, Pricing: &api.InvoiceLineItemPricing{PriceDetails: &api.InvoiceLineItemPricingPriceDetails{Price: &api.Price{ID: "new-price"}}}, Period: &api.Period{Start: b.originalStart, End: b.originalEnd}}}}},
		}
	case *api.InvoicePaymentList:
		v.Data = []*api.InvoicePayment{{ID: "old-paid", Payment: &api.InvoicePaymentPayment{Charge: &api.Charge{ID: "old-charge"}}}}
	default:
		return b.subBackend.CallRaw(method, path, key, body, p, out)
	}
	return nil
}

func TestTierUpgradeStripeStartsMonthOnce(t *testing.T) {
	b := &tierUpgradeBackend{current: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Status: api.SubscriptionStatusActive,
		Metadata: map[string]string{"tenant_id": "tenant"}, LatestInvoice: &api.Invoice{Status: api.InvoiceStatusPaid},
		Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", Price: &api.Price{ID: "old"}}}}}, loseResponse: true}
	installBackend(t, b)
	request := billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", TierID: "new-tier", CustomerID: "customer", SubscriptionID: "sub", PriceID: "new-price", BaseCents: 5000}
	c := newStripeClient()
	if _, err := c.UpgradeMonthlySubscription(t.Context(), request); err == nil {
		t.Fatal("lost response did not fail")
	}
	receipt, err := c.UpgradeMonthlySubscription(t.Context(), request)
	if err != nil || !receipt.Applied || !receipt.PeriodEnd.After(receipt.PeriodStart) || b.updates != 1 {
		t.Fatalf("receipt=%+v updates=%d err=%v", receipt, b.updates, err)
	}
	p := b.params
	if p.BillingCycleAnchorNow == nil || !*p.BillingCycleAnchorNow || api.StringValue(p.ProrationBehavior) != "always_invoice" || api.StringValue(p.PaymentBehavior) != "error_if_incomplete" || api.StringValue(p.Items[0].ID) != "item" || api.StringValue(p.IdempotencyKey) != "tier-upgrade:upgrade" {
		t.Fatalf("upgrade did not reset and invoice the existing subscription safely: %+v", p)
	}
}

func TestTierUpgradeStripeDoesNotCreditUnpaidMonth(t *testing.T) {
	b := &tierUpgradeBackend{current: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Status: api.SubscriptionStatusActive,
		Metadata: map[string]string{"tenant_id": "tenant"}, LatestInvoice: &api.Invoice{Status: api.InvoiceStatusOpen},
		Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", Price: &api.Price{ID: "old"}}}}}}
	installBackend(t, b)
	_, err := newStripeClient().UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", PriceID: "new"})
	if !errors.Is(err, billing.ErrTierUpgradeUnsettled) || b.updates != 0 {
		t.Fatalf("unpaid month was credited: updates=%d err=%v", b.updates, err)
	}
}

func TestTierUpgradeStripeRefusesWrongPrice(t *testing.T) {
	b := &tierUpgradeBackend{current: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Status: api.SubscriptionStatusActive,
		Metadata: map[string]string{"tenant_id": "tenant"}, LatestInvoice: &api.Invoice{Status: api.InvoiceStatusPaid},
		Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", Price: &api.Price{ID: "old"}}}}}}
	installBackend(t, b)
	_, err := newStripeClient().UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", PriceID: "new-price", BaseCents: 6000})
	if !errors.Is(err, billing.ErrTierUpgradeRejected) || b.updates != 0 {
		t.Fatalf("wrong provider amount was charged: updates=%d err=%v", b.updates, err)
	}
}

func TestTierUpgradeStripeRefusesRefundedOrDisputedFeeAndPendingCancellation(t *testing.T) {
	for _, mode := range []string{"refunded", "disputed", "pending cancellation"} {
		t.Run(mode, func(t *testing.T) {
			b := &tierUpgradeBackend{current: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Status: api.SubscriptionStatusActive, Metadata: map[string]string{"tenant_id": "tenant"}, LatestInvoice: &api.Invoice{ID: "old", Status: api.InvoiceStatusPaid, AmountPaid: 2000}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", Price: &api.Price{ID: "old"}}}}}}
			if mode == "refunded" {
				b.feeRefunded = 100
			}
			if mode == "disputed" {
				b.feeDisputed = true
			}
			if mode == "pending cancellation" {
				b.current.CancelAtPeriodEnd = true
			}
			installBackend(t, b)
			_, err := newStripeClient().UpgradeMonthlySubscription(t.Context(), billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", PriceID: "new-price", BaseCents: 5000})
			if !errors.Is(err, billing.ErrTierUpgradeRejected) || b.updates != 0 {
				t.Fatalf("disqualified fee or cancellation changed pricing: updates=%d err=%v", b.updates, err)
			}
		})
	}
}

func TestTierUpgradeStripeReplayUsesOriginalInvoiceAfterRenewalAndDunning(t *testing.T) {
	b := &tierUpgradeBackend{current: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Status: api.SubscriptionStatusActive, Metadata: map[string]string{"tenant_id": "tenant"}, LatestInvoice: &api.Invoice{Status: api.InvoiceStatusPaid}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item", Price: &api.Price{ID: "old"}}}}}, loseResponse: true}
	installBackend(t, b)
	request := billing.TierUpgradeRequest{ID: "upgrade", TenantID: "tenant", CustomerID: "customer", SubscriptionID: "sub", PriceID: "new-price", BaseCents: 5000, RequestedAt: time.Now()}
	if _, err := newStripeClient().UpgradeMonthlySubscription(t.Context(), request); err == nil {
		t.Fatal("lost response did not fail")
	}
	original := b.originalStart
	b.current.Status = api.SubscriptionStatusPastDue
	b.current.Items.Data[0].CurrentPeriodStart = time.Now().AddDate(0, 2, 0).Unix()
	b.current.Items.Data[0].CurrentPeriodEnd = time.Now().AddDate(0, 3, 0).Unix()
	b.current.LatestInvoice = &api.Invoice{ID: "renewal", Status: api.InvoiceStatusPaid}
	receipt, err := newStripeClient().UpgradeMonthlySubscription(t.Context(), request)
	if err != nil || receipt.PeriodStart.Unix() != original || receipt.PaidReceiptID != "upgrade-invoice" || b.updates != 1 {
		t.Fatalf("replay substituted renewal for purchase: receipt=%+v updates=%d err=%v", receipt, b.updates, err)
	}
}
