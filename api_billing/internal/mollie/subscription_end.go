package mollie

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// EndSubscriptionForFallback stops recurrence without refunding an activated purchase.
// It checks payments again after cancellation so a concurrent settlement retains its paid term.
func (c *Client) EndSubscriptionForFallback(ctx context.Context, request billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result billing.SubscriptionEndResult
	sub, err := c.fallbackSubscription(ctx, request.CustomerID, request.SubscriptionID)
	if mollieObjectMissing(err) {
		sub = &mollie.Subscription{ID: request.SubscriptionID, Status: "canceled"}
	} else if err != nil {
		return result, err
	}
	if sub.ID != request.SubscriptionID || sub.Status == "" {
		return result, errors.New("mollie fallback subscription has an unexpected identity or status")
	}
	result, err = c.fallbackPaidTerm(ctx, request, sub)
	if err != nil {
		return result, err
	}
	if request.AllowRecovery && sub.Status == "active" && result.PaidUntil.After(time.Now()) && !result.Pending && sub.MandateID != "" {
		mandate, readErr := c.GetMandate(ctx, request.CustomerID, sub.MandateID)
		if readErr != nil {
			return result, readErr
		}
		if mandate.Status == "valid" {
			result.Recovered = true
			return result, nil
		}
	}
	if sub.Status != "canceled" && sub.Status != "completed" {
		if err = c.CancelSubscription(ctx, request.CustomerID, request.SubscriptionID); err != nil {
			return result, err
		}
		confirmed, readErr := c.fallbackSubscription(ctx, request.CustomerID, request.SubscriptionID)
		if readErr != nil && !mollieObjectMissing(readErr) {
			return result, readErr
		}
		if readErr == nil && confirmed.Status != "canceled" && confirmed.Status != "completed" {
			return result, fmt.Errorf("mollie has not confirmed cancellation: %s", confirmed.Status)
		}
	}
	return c.fallbackPaidTerm(ctx, request, sub)
}

func mollieObjectMissing(err error) bool {
	var providerErr *APIError
	return errors.As(err, &providerErr) && providerErr.StatusCode == 404
}

// fallbackPaidTerm reads only payments belonging to this native subscription.
// An initial payment must match the stored checkout receipt exactly. Its first
// scheduled recurrence ends that paid term; later payments use the calendar anchor.
func (c *Client) fallbackPaidTerm(ctx context.Context, request billing.SubscriptionEndRequest, sub *mollie.Subscription) (billing.SubscriptionEndResult, error) {
	var result billing.SubscriptionEndResult
	from := ""
	seen := map[string]bool{}
	for {
		u, err := c.mollieURL("v2/customers/" + url.PathEscape(request.CustomerID) + "/payments")
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
		if mollieObjectMissing(err) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		var page struct {
			Embedded struct {
				Payments []*mollie.Payment `json:"payments"`
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
		for _, payment := range page.Embedded.Payments {
			initial := request.InitialPaymentID != "" && payment.ID == request.InitialPaymentID && payment.SubscriptionID == ""
			if payment.SubscriptionID != request.SubscriptionID && !initial {
				continue
			}
			switch payment.Status {
			case "open", "pending", "authorized":
				result.Pending = true
			case "paid":
				retained, amountErr := retainedFallbackPayment(payment)
				if amountErr != nil {
					return result, amountErr
				}
				if !retained {
					continue
				}
				if payment.CreatedAt == nil {
					return result, errors.New("paid Mollie installment has no creation time")
				}
				anchor := request.PeriodEnd
				if sub.StartDate != nil {
					anchor = sub.StartDate.Time
				}
				if initial && sub.StartDate == nil {
					if sub.CreatedAT == nil {
						return result, errors.New("initial Mollie payment has no first subscription term")
					}
					anchor = billing.NextMonthlyDateBoundary(*sub.CreatedAT)
				}
				until, termErr := fallbackMollieTermEnd(anchor, *payment.CreatedAt)
				if termErr != nil {
					return result, termErr
				}
				if initial {
					until = anchor
				}
				if until.After(result.PaidUntil) {
					result.PaidUntil = until
				}
			}
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			return result, nil
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return result, err
		}
		from = next.Query().Get("from")
		if from == "" || seen[from] {
			return result, errors.New("mollie fallback payment pagination did not advance")
		}
		seen[from] = true
	}
}

func retainedFallbackPayment(payment *mollie.Payment) (bool, error) {
	if payment.Amount == nil {
		return false, errors.New("paid Mollie installment has no amount")
	}
	amount, err := decimal.NewFromString(payment.Amount.Value)
	if err != nil || amount.IsNegative() {
		return false, errors.New("paid Mollie installment has an invalid amount")
	}
	for _, reversed := range []*mollie.Amount{payment.AmountRefunded, payment.AmountChargedBack} {
		if reversed == nil {
			continue
		}
		value, parseErr := decimal.NewFromString(reversed.Value)
		if parseErr != nil || reversed.Currency != payment.Amount.Currency || value.IsNegative() {
			return false, errors.New("mollie installment has an invalid reversed amount")
		}
		amount = amount.Sub(value)
	}
	return amount.IsPositive(), nil
}

// Native subscription dates anchor installments, including capped month ends.
// Payment retries within a cycle do not buy a new month from their retry date.
func fallbackMollieTermEnd(anchor, created time.Time) (time.Time, error) {
	if anchor.IsZero() {
		return time.Time{}, errors.New("mollie installment has no subscription calendar anchor")
	}
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
	created = created.UTC()
	months := (created.Year()-anchor.Year())*12 + int(created.Month()-anchor.Month())
	// The provider's date-only charge can arrive on either side of UTC midnight.
	if billing.AddCalendarMonths(anchor, months).After(created.Add(14 * time.Hour)) {
		months--
	}
	return billing.AddCalendarMonths(anchor, months+1), nil
}

// The HTTP status distinguishes a deleted native object from a retryable outage.
func (c *Client) fallbackSubscription(ctx context.Context, customerID, subscriptionID string) (*mollie.Subscription, error) {
	u, err := c.mollieURL("v2/customers/" + url.PathEscape(customerID) + "/subscriptions/" + url.PathEscape(subscriptionID))
	if err != nil {
		return nil, err
	}
	raw, err := c.getJSON(ctx, u.String())
	if err != nil {
		return nil, err
	}
	var sub mollie.Subscription
	if err = json.Unmarshal(raw, &sub); err != nil {
		return nil, err
	}
	return &sub, nil
}
