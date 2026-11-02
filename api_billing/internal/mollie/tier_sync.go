package mollie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// A scheduled downgrade's first write keeps the subscriber's own amount in the
// subscription metadata, in the same PATCH, for a later cancellation. The tier
// key scopes it: an amount recorded from another tier is stale.
const (
	restoreTierKey   = "billing_restore_tier_id"
	restoreAmountKey = "billing_restore_amount"
)

// SyncSubscriptionTier changes the amount of later recurring payments and leaves
// the paid schedule alone. The read decides, so a lost response retries as a
// no-op; a fixed idempotency key could replay a stale target after a re-point.
// Only canceled and completed subscriptions are terminal: a suspended one renews
// again once its mandate recovers, so it takes the new amount too.
//
// With no downgrade pending, a subscription already on the tier is left alone,
// so a subscriber on an amount the catalog replaced keeps it. Cancelling a
// confirmed downgrade restores the recorded amount, not the catalog's.
func (c *Client) SyncSubscriptionTier(ctx context.Context, request billing.ProviderTierRequest) (billing.ProviderTierReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	sub, err := c.GetSubscription(ctx, request.CustomerID, request.SubscriptionID)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	metadata := map[string]any{}
	raw, err := json.Marshal(sub.Metadata)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if string(raw) != "null" {
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return billing.ProviderTierReceipt{}, err
		}
	}
	if metadata["tenant_id"] != request.TenantID {
		return billing.ProviderTierReceipt{}, errors.New("mollie subscription does not belong to the tenant")
	}
	if sub.Status == "canceled" || sub.Status == "completed" {
		return billing.ProviderTierReceipt{}, nil
	}
	receipt := mollieTierReceipt(sub)
	providerTier := metadata["tier_id"]
	if !request.Downgrade && (providerTier == nil || providerTier == request.TierID) {
		return receipt, nil
	}
	amount := decimal.New(request.BaseCents, -2)
	if request.Downgrade && mollieChargesAmount(sub, amount) && providerTier == request.TierID {
		return receipt, nil
	}
	recorded, hasRecorded := metadata[restoreAmountKey].(string)
	if !request.Downgrade && metadata[restoreTierKey] == request.TierID && hasRecorded {
		if amount, err = decimal.NewFromString(recorded); err != nil {
			return billing.ProviderTierReceipt{}, fmt.Errorf("mollie recorded restore amount: %w", err)
		}
		delete(metadata, restoreTierKey)
		delete(metadata, restoreAmountKey)
	} else {
		if request.BaseCents <= 0 {
			return billing.ProviderTierReceipt{}, fmt.Errorf("%w: the tier has no monthly fee and the subscription recorded none to restore", billing.ErrProviderTierRefused)
		}
		// While the provider is on the local tier its amount is the one to keep,
		// replacing a record an earlier applied downgrade left behind.
		if request.Downgrade && (providerTier == request.CurrentTierID || metadata[restoreTierKey] != request.CurrentTierID) && sub.Amount != nil {
			metadata[restoreTierKey], metadata[restoreAmountKey] = request.CurrentTierID, sub.Amount.Value
		}
	}
	metadata["tier_id"] = request.TierID
	body, err := json.Marshal(map[string]any{
		"amount":   map[string]string{"currency": "EUR", "value": amount.StringFixed(2)},
		"metadata": metadata,
	})
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	u, err := c.mollieURL("v2/customers/" + url.PathEscape(request.CustomerID) + "/subscriptions/" + url.PathEscape(request.SubscriptionID))
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u.String(), bytes.NewReader(body))
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return billing.ProviderTierReceipt{}, &APIError{StatusCode: response.StatusCode, Message: string(result)}
	}
	var updated mollie.Subscription
	if err = json.Unmarshal(result, &updated); err != nil {
		return billing.ProviderTierReceipt{}, err
	}
	if !mollieChargesAmount(&updated, amount) {
		return billing.ProviderTierReceipt{}, fmt.Errorf("mollie did not confirm the %s renewal amount", amount.StringFixed(2))
	}
	return mollieTierReceipt(&updated), nil
}

func mollieChargesAmount(sub *mollie.Subscription, amount decimal.Decimal) bool {
	if sub.Amount == nil || sub.Amount.Currency != "EUR" {
		return false
	}
	current, err := decimal.NewFromString(sub.Amount.Value)
	return err == nil && current.Equal(amount)
}

func mollieTierReceipt(sub *mollie.Subscription) billing.ProviderTierReceipt {
	if sub.NextPaymentDate == nil {
		return billing.ProviderTierReceipt{}
	}
	return billing.ProviderTierReceipt{PaidThrough: sub.NextPaymentDate.UTC()}
}
