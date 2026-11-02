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

// A scheduled downgrade's first write keeps the subscriber's own price in the
// subscription metadata, in the same atomic update, for a later cancellation.
// The tier key scopes it: a restore price recorded from another tier is stale.
const (
	restoreTierKey  = "billing_restore_tier_id"
	restorePriceKey = "billing_restore_price_id"
)

// SyncSubscriptionTier makes the next renewal charge the request's price. It
// never invoices or credits the paid term. The read decides, so a lost response
// retries as a no-op and needs no idempotency key; a fixed key would replay a
// stale result when a tier is re-pointed back within Stripe's key window.
//
// With no downgrade pending, a subscription already on the tier is left alone,
// so a subscriber on a price the catalog replaced keeps it. Cancelling a confirmed
// downgrade restores the recorded price, not the catalog's.
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
	providerTier := sub.Metadata["tier_id"]
	if !request.Downgrade && (providerTier == "" || providerTier == request.TierID) {
		return receipt, nil
	}
	if request.Downgrade && item.Price != nil && item.Price.ID == request.PriceID && providerTier == request.TierID {
		return receipt, nil
	}
	if item.Quantity != 1 || item.Price == nil || item.Price.Currency != stripe.CurrencyEUR || item.Price.Recurring == nil ||
		item.Price.Recurring.Interval != stripe.PriceRecurringIntervalMonth || item.Price.Recurring.IntervalCount != 1 {
		return billing.ProviderTierReceipt{}, fmt.Errorf("%w: the native Stripe fee must be one monthly EUR unit", billing.ErrProviderTierRefused)
	}
	params := &stripe.SubscriptionParams{ProrationBehavior: stripe.String("none")}
	params.Context = ctx
	params.AddMetadata("tenant_id", request.TenantID)
	params.AddMetadata("tier_id", request.TierID)
	var change *stripe.SubscriptionItemsParams
	confirmPrice := request.PriceID
	if !request.Downgrade && sub.Metadata[restoreTierKey] == request.TierID && sub.Metadata[restorePriceKey] != "" {
		confirmPrice = sub.Metadata[restorePriceKey]
		change, err = restoreItem(ctx, item.ID, confirmPrice)
		if err != nil {
			return billing.ProviderTierReceipt{}, err
		}
		if change.Price == nil {
			confirmPrice = ""
		}
		params.AddMetadata(restoreTierKey, "")
		params.AddMetadata(restorePriceKey, "")
	} else {
		if request.PriceID == "" {
			return billing.ProviderTierReceipt{}, fmt.Errorf("%w: the tier has no monthly Stripe price and the subscription recorded none to restore", billing.ErrProviderTierRefused)
		}
		priceParams := &stripe.PriceParams{}
		priceParams.Context = ctx
		target, readErr := stripeprice.Get(request.PriceID, priceParams)
		if readErr != nil {
			return billing.ProviderTierReceipt{}, readErr
		}
		if !target.Active || target.UnitAmount != request.BaseCents || target.Currency != stripe.CurrencyEUR || target.Recurring == nil ||
			target.Recurring.Interval != stripe.PriceRecurringIntervalMonth || target.Recurring.IntervalCount != 1 {
			return billing.ProviderTierReceipt{}, fmt.Errorf("%w: Stripe monthly price differs from the tier's EUR fee", billing.ErrProviderTierRefused)
		}
		change = &stripe.SubscriptionItemsParams{ID: stripe.String(item.ID), Price: stripe.String(request.PriceID)}
		// While the provider is on the local tier its price is the one to keep,
		// replacing a record an earlier applied downgrade left behind.
		if request.Downgrade && (providerTier == request.CurrentTierID || sub.Metadata[restoreTierKey] != request.CurrentTierID) {
			params.AddMetadata(restoreTierKey, request.CurrentTierID)
			params.AddMetadata(restorePriceKey, item.Price.ID)
		}
	}
	params.Items = []*stripe.SubscriptionItemsParams{change}
	updated, err := subscription.Update(request.SubscriptionID, params)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if updated.Items == nil || len(updated.Items.Data) != 1 || updated.Items.Data[0].Price == nil || confirmPrice != "" && updated.Items.Data[0].Price.ID != confirmPrice {
		return billing.ProviderTierReceipt{}, errors.New("stripe did not confirm the renewal price")
	}
	return receipt, nil
}

// restoreItem returns the subscriber to a recorded price. Stripe refuses an
// archived price on an update, so that one is re-created inline with its product,
// amount and interval, which keeps what the subscriber pays exact.
func restoreItem(ctx context.Context, itemID, priceID string) (*stripe.SubscriptionItemsParams, error) {
	priceParams := &stripe.PriceParams{}
	priceParams.Context = ctx
	original, err := stripeprice.Get(priceID, priceParams)
	if err != nil {
		return nil, err
	}
	if original.Active {
		return &stripe.SubscriptionItemsParams{ID: stripe.String(itemID), Price: stripe.String(priceID)}, nil
	}
	if original.Product == nil || original.Recurring == nil {
		return nil, fmt.Errorf("%w: the archived Stripe price has no product or interval to restore", billing.ErrProviderTierRefused)
	}
	return &stripe.SubscriptionItemsParams{ID: stripe.String(itemID), PriceData: &stripe.SubscriptionItemPriceDataParams{
		Currency: stripe.String(string(original.Currency)), Product: stripe.String(original.Product.ID), UnitAmount: stripe.Int64(original.UnitAmount),
		Recurring: &stripe.SubscriptionItemPriceDataRecurringParams{Interval: stripe.String(string(original.Recurring.Interval)), IntervalCount: stripe.Int64(original.Recurring.IntervalCount)},
	}}, nil
}
