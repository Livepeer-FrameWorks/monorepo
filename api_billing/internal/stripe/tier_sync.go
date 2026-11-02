package stripe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/stripe/stripe-go/v85"
	stripeprice "github.com/stripe/stripe-go/v85/price"
	"github.com/stripe/stripe-go/v85/subscription"
)

// SyncSubscriptionTier makes the next renewal charge the request's price. It
// never invoices or credits the paid term. The read decides, so a lost response
// retries as a no-op and needs no idempotency key; a fixed key would replay a
// stale result when a tier is re-pointed back within Stripe's key window.
func (c *Client) SyncSubscriptionTier(ctx context.Context, request billing.ProviderTierRequest) (billing.ProviderTierReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	get := &stripe.SubscriptionParams{}
	get.Context = ctx
	sub, err := subscription.Get(request.SubscriptionID, get)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if sub.Customer == nil || sub.Customer.ID != request.CustomerID || sub.Metadata["tenant_id"] != request.TenantID || sub.Items == nil || len(sub.Items.Data) != 1 {
		return billing.ProviderTierReceipt{}, errors.New("stripe subscription does not match its tenant, customer and single base-fee item")
	}
	item := sub.Items.Data[0]
	receipt := billing.ProviderTierReceipt{PaidThrough: time.Unix(item.CurrentPeriodEnd, 0).UTC()}
	if sub.Status == stripe.SubscriptionStatusCanceled || sub.Status == stripe.SubscriptionStatusIncompleteExpired {
		return billing.ProviderTierReceipt{}, nil
	}
	if item.Price != nil && item.Price.ID == request.PriceID && sub.Metadata["tier_id"] == request.TierID {
		return receipt, nil
	}
	priceParams := &stripe.PriceParams{}
	priceParams.Context = ctx
	target, err := stripeprice.Get(request.PriceID, priceParams)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if !target.Active || target.UnitAmount != request.BaseCents || target.Currency != stripe.CurrencyEUR || target.Recurring == nil ||
		target.Recurring.Interval != stripe.PriceRecurringIntervalMonth || target.Recurring.IntervalCount != 1 {
		return billing.ProviderTierReceipt{}, fmt.Errorf("%w: Stripe monthly price differs from the tier's EUR fee", billing.ErrProviderTierRefused)
	}
	params := &stripe.SubscriptionParams{
		Items:             []*stripe.SubscriptionItemsParams{{ID: stripe.String(item.ID), Price: stripe.String(request.PriceID)}},
		ProrationBehavior: stripe.String("none"),
	}
	params.Context = ctx
	params.AddMetadata("tenant_id", request.TenantID)
	params.AddMetadata("tier_id", request.TierID)
	updated, err := subscription.Update(request.SubscriptionID, params)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if updated.Items == nil || len(updated.Items.Data) != 1 || updated.Items.Data[0].Price == nil || updated.Items.Data[0].Price.ID != request.PriceID {
		return billing.ProviderTierReceipt{}, errors.New("stripe did not confirm the renewal price")
	}
	return receipt, nil
}
