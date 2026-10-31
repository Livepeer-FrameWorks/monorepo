package mollie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// CancelReservedCheckout reconciles a lost response by its exact reservation metadata.
// Recurring subscriptions are cancelled; first payments are cancelled or refunded.
func (c *Client) CancelReservedCheckout(ctx context.Context, customerID, intentID, paymentID, subscriptionID string) error {
	_, err := c.RecoverReservedCheckout(ctx, customerID, intentID, paymentID, subscriptionID)
	return err
}

// RecoverReservedCheckout reports actions accepted for an exact reservation.
func (c *Client) RecoverReservedCheckout(ctx context.Context, customerID, intentID, paymentID, subscriptionID string) (billing.CheckoutRecoveryResult, error) {
	var result billing.CheckoutRecoveryResult
	if subscriptionID != "" {
		err := c.cancelReservedSubscription(ctx, customerID, subscriptionID, &result)
		return result, err
	}
	if paymentID != "" {
		err := c.cancelReservedPayment(ctx, intentID, paymentID, &result)
		return result, err
	}
	for _, resource := range []string{"subscriptions", "payments"} {
		from := ""
		for {
			u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/" + resource)
			if err != nil {
				return result, err
			}
			query := u.Query()
			query.Set("limit", "250")
			if from != "" {
				query.Set("from", from)
			}
			u.RawQuery = query.Encode()
			raw, err := c.getJSON(ctx, u.String())
			if err != nil {
				return result, err
			}
			var page struct {
				Embedded map[string][]struct {
					ID       string         `json:"id"`
					Metadata map[string]any `json:"metadata"`
				} `json:"_embedded"`
				Links struct {
					Next *struct {
						Href string `json:"href"`
					} `json:"next"`
				} `json:"_links"`
			}
			if err = json.Unmarshal(raw, &page); err != nil {
				return result, err
			}
			for _, object := range page.Embedded[resource] {
				if object.Metadata["checkout_reservation_id"] != intentID {
					continue
				}
				if resource == "payments" {
					err = c.cancelReservedPayment(ctx, intentID, object.ID, &result)
				} else {
					err = c.cancelReservedSubscription(ctx, customerID, object.ID, &result)
				}
				if err != nil {
					return result, err
				}
			}
			if page.Links.Next == nil || page.Links.Next.Href == "" {
				break
			}
			next, err := url.Parse(page.Links.Next.Href)
			if err != nil {
				return result, err
			}
			nextFrom := next.Query().Get("from")
			if nextFrom == "" || nextFrom == from {
				return result, fmt.Errorf("mollie %s pagination did not advance", resource)
			}
			from = nextFrom
		}
	}
	return result, nil
}

func (c *Client) cancelReservedSubscription(ctx context.Context, customerID, id string, result *billing.CheckoutRecoveryResult) error {
	sub, err := c.GetSubscription(ctx, customerID, id)
	if err != nil {
		return err
	}
	if sub.Status != "canceled" && sub.Status != "completed" {
		if err = c.CancelSubscription(ctx, customerID, id); err != nil {
			return err
		}
	}
	result.Cancelled = true
	// The separate first payment funds checkout. Subscription payments fund
	// recurring service and are never part of checkout compensation.
	return nil
}

func (c *Client) cancelReservedPayment(ctx context.Context, intentID, id string, result *billing.CheckoutRecoveryResult) error {
	payment, err := c.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if payment.SubscriptionID != "" || payment.SequenceType == mollie.RecurringSequence {
		return nil
	}
	switch payment.Status {
	case "canceled", "expired", "failed":
		return nil
	case "paid":
		if payment.Amount == nil || len(payment.Amount.Currency) != 3 {
			return fmt.Errorf("paid checkout %s has no amount", id)
		}
		remaining, amountErr := decimal.NewFromString(payment.Amount.Value)
		if amountErr != nil {
			return amountErr
		}
		originalCents := remaining.Shift(2).IntPart()
		if originalCents == 0 {
			// A zero-amount card mandate establishes collection permission;
			// it has no bank return or monetary settlement to reconcile.
			return nil
		}
		var chargedBack int64
		if payment.AmountChargedBack != nil {
			amount, parseErr := decimal.NewFromString(payment.AmountChargedBack.Value)
			if parseErr != nil || payment.AmountChargedBack.Currency != payment.Amount.Currency {
				return fmt.Errorf("invalid chargeback on checkout %s", id)
			}
			chargedBack = max(amount.Shift(2).IntPart(), 0)
			result.ReturnedByChargeback = chargedBack > 0
			remaining = remaining.Sub(amount)
			if !remaining.IsPositive() {
				result.RefundSettlements = append(result.RefundSettlements, billing.RefundSettlement{Provider: "mollie", ReferenceKind: "checkout", ReferenceID: intentID, PaymentID: id, AmountCents: originalCents, Currency: payment.Amount.Currency})
				result.ReturnedByChargeback = true
				return nil
			}
		}
		result.RefundSettlements = append(result.RefundSettlements, billing.RefundSettlement{Provider: "mollie", ReferenceKind: "checkout", ReferenceID: intentID, PaymentID: id, AmountCents: originalCents, Currency: payment.Amount.Currency})
		if payment.AmountRefunded != nil {
			refunded, parseErr := decimal.NewFromString(payment.AmountRefunded.Value)
			if parseErr != nil {
				return parseErr
			}
			remaining = remaining.Sub(refunded)
		}
		if payment.AmountRemaining != nil {
			refundable, parseErr := decimal.NewFromString(payment.AmountRemaining.Value)
			if parseErr != nil {
				return parseErr
			}
			remaining = decimal.Min(remaining, refundable)
		}
		if !remaining.IsPositive() {
			result.RefundAlreadyComplete = true
			return nil
		}
		raw, marshalErr := json.Marshal(map[string]any{"amount": Amount(remaining.StringFixed(2), payment.Amount.Currency), "description": "Expired checkout reservation"})
		if marshalErr != nil {
			return marshalErr
		}
		u, urlErr := c.mollieURL("v2/payments/" + url.PathEscape(id) + "/refunds")
		if urlErr != nil {
			return urlErr
		}
		_, err = c.postJSON(ctx, u.String(), raw, "checkout-expiry-refund:"+intentID+":"+id)
		if err == nil {
			result.RefundRequested = true
		}
		return err
	default:
		if payment.IsCancelable {
			_, _, err = c.client.Payments.Cancel(ctx, id)
			if err == nil {
				result.Cancelled = true
			}
			return err
		}
		return fmt.Errorf("checkout %s is still settling (%s): %w", id, payment.Status, ErrPaymentSettling)
	}
}

// ErrPaymentSettling is a normal wait for a non-cancelable provider payment.
var ErrPaymentSettling = errors.New("provider payment is settling")

// ReservedPayment locates a first payment even when the response binding was lost.
func (c *Client) ReservedPayment(ctx context.Context, customerID, intentID, paymentID string) (*mollie.Payment, error) {
	if paymentID != "" {
		return c.GetPayment(ctx, paymentID)
	}
	from := ""
	for {
		u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/payments")
		if err != nil {
			return nil, err
		}
		query := u.Query()
		query.Set("limit", "250")
		if from != "" {
			query.Set("from", from)
		}
		u.RawQuery = query.Encode()
		raw, err := c.getJSON(ctx, u.String())
		if err != nil {
			return nil, err
		}
		var page struct {
			Embedded struct {
				Payments []mollie.Payment `json:"payments"`
			} `json:"_embedded"`
			Links struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		for _, p := range page.Embedded.Payments {
			metadata, ok := p.Metadata.(map[string]any)
			if !ok {
				continue
			}
			if metadata["checkout_reservation_id"] == intentID {
				return c.GetPayment(ctx, p.ID)
			}
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			return nil, nil
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return nil, err
		}
		n := next.Query().Get("from")
		if n == "" || n == from {
			return nil, errors.New("payment pagination did not advance")
		}
		from = n
	}
}

// ReservedSubscription locates a recurring create whose response was not bound.
func (c *Client) ReservedSubscription(ctx context.Context, customerID, intentID, id string) (*mollie.Subscription, error) {
	if id != "" {
		return c.GetSubscription(ctx, customerID, id)
	}
	from := ""
	var found *mollie.Subscription
	for {
		u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/subscriptions")
		if err != nil {
			return nil, err
		}
		query := u.Query()
		query.Set("limit", "250")
		if from != "" {
			query.Set("from", from)
		}
		u.RawQuery = query.Encode()
		raw, err := c.getJSON(ctx, u.String())
		if err != nil {
			return nil, err
		}
		var page struct {
			Embedded struct {
				Subscriptions []mollie.Subscription `json:"subscriptions"`
			} `json:"_embedded"`
			Links struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if decodeErr := json.Unmarshal(raw, &page); decodeErr != nil {
			return nil, decodeErr
		}
		for _, sub := range page.Embedded.Subscriptions {
			metadata, ok := sub.Metadata.(map[string]any)
			if ok && metadata["checkout_reservation_id"] == intentID {
				if found != nil {
					return nil, fmt.Errorf("multiple subscriptions for reservation %s", intentID)
				}
				copy := sub
				found = &copy
			}
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			return found, nil
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return nil, err
		}
		nextFrom := next.Query().Get("from")
		if nextFrom == "" || nextFrom == from {
			return nil, errors.New("subscription pagination did not advance")
		}
		from = nextFrom
	}
}

// ErrHistoricalFundingAmbiguous requires an operator to decide delivery from external evidence.
var ErrHistoricalFundingAmbiguous = errors.New("historical Mollie delivery cannot be proven")

// InspectFirstPaymentFunding resolves activation history for a funding
// receipt that predates local funding links. Provider creation alone does not
// prove local activation; ambiguous history retains the payment for reconciliation.
func (c *Client) InspectFirstPaymentFunding(
	ctx context.Context, customerID, paymentID, retainedSubscriptionID string,
	locallyActivated func(string) (bool, error),
) (bool, []string, error) {
	payment, err := c.GetPayment(ctx, paymentID)
	if err != nil {
		return false, nil, err
	}
	if customerID == "" {
		customerID = payment.CustomerID
	}
	if customerID == "" || payment.CustomerID != "" && payment.CustomerID != customerID {
		return false, nil, errors.New("first payment has no matching provider customer")
	}
	retained, amountErr := retainedFallbackPayment(payment)
	if amountErr != nil {
		return false, nil, amountErr
	}
	funded := false
	var orphans []string
	ambiguousID := ""
	from := ""
	for {
		u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/subscriptions")
		if err != nil {
			return false, nil, err
		}
		query := u.Query()
		query.Set("limit", "250")
		if from != "" {
			query.Set("from", from)
		}
		u.RawQuery = query.Encode()
		raw, err := c.getJSON(ctx, u.String())
		if err != nil {
			return false, nil, err
		}
		var page struct {
			Embedded struct {
				Subscriptions []*mollie.Subscription `json:"subscriptions"`
			} `json:"_embedded"`
			Links struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return false, nil, err
		}
		for _, subscription := range page.Embedded.Subscriptions {
			if payment.CreatedAt == nil || subscription.CreatedAT == nil {
				return false, nil, errors.New("provider funding history has no creation timestamps")
			}
			if subscription.CreatedAT.Before(*payment.CreatedAt) {
				continue
			}
			if payment.MandateID == "" || subscription.MandateID == "" {
				return false, nil, errors.New("provider funding history cannot identify the initial payment mandate")
			}
			if subscription.MandateID == payment.MandateID {
				activated, proofErr := locallyActivated(subscription.ID)
				if proofErr != nil {
					return false, nil, proofErr
				}
				if activated {
					funded = true
				} else {
					ambiguousID = subscription.ID
				}
				if subscription.ID != retainedSubscriptionID {
					orphans = append(orphans, subscription.ID)
				}
			}
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			if !funded && ambiguousID != "" && retained {
				return false, orphans, fmt.Errorf(
					"%w: subscription %s, initial payment %s; use frameworks admin billing recovery checkout "+
						"--tenant-id TENANT --reservation-id RESERVATION --reason REASON "+
						"--retain-delivered only after confirming delivered service",
					ErrHistoricalFundingAmbiguous, ambiguousID, paymentID,
				)
			}
			return funded, orphans, nil
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return false, nil, err
		}
		cursor := next.Query().Get("from")
		if cursor == "" || cursor == from {
			return false, nil, errors.New("provider subscription history has no advancing cursor")
		}
		from = cursor
	}
}
