package mollie

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/VictorAvelar/mollie-api-go/v4/mollie"
	"github.com/shopspring/decimal"
)

// InspectRefundSettlement reads all refund outcomes for the exact first payment.
func (c *Client) InspectRefundSettlement(ctx context.Context, plan billing.RefundSettlement) ([]billing.SettledRefund, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var receipts []billing.SettledRefund

	var total int64
	failed := false
	waiting := false
	from := ""
	for {
		u, err := c.mollieURL("v2/payments/" + url.PathEscape(plan.PaymentID) + "/refunds")
		if err != nil {
			return receipts, err
		}
		query := u.Query()
		query.Set("limit", "250")
		if from != "" {
			query.Set("from", from)
		}
		u.RawQuery = query.Encode()
		raw, err := c.getJSON(ctx, u.String())
		if err != nil {
			return receipts, err
		}
		var page struct {
			Embedded struct {
				Refunds []mollie.Refund `json:"refunds"`
			} `json:"_embedded"`
			Links struct {
				Next *struct {
					Href string `json:"href"`
				} `json:"next"`
			} `json:"_links"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return receipts, err
		}
		for _, r := range page.Embedded.Refunds {
			if r.Amount == nil || !strings.EqualFold(r.Amount.Currency, plan.Currency) {
				return receipts, fmt.Errorf("refund %s has a different currency from its purchase", r.ID)
			}
			switch string(r.Status) {
			case "refunded":
				amount, amountErr := decimal.NewFromString(r.Amount.Value)
				if amountErr != nil {
					return receipts, amountErr
				}
				cents := amount.Shift(2).IntPart()
				if cents > 0 {
					total += cents
					receipts = append(receipts, billing.SettledRefund{ObjectType: "refund", ID: r.ID, PaymentID: plan.PaymentID, AmountCents: cents, Currency: strings.ToUpper(r.Amount.Currency)})
				}
			case "failed", "canceled":
				failed = true
			default:
				waiting = true
			}
		}
		if page.Links.Next == nil || page.Links.Next.Href == "" {
			break
		}
		next, err := url.Parse(page.Links.Next.Href)
		if err != nil {
			return receipts, err
		}
		cursor := next.Query().Get("from")
		if cursor == "" || cursor == from {
			return receipts, fmt.Errorf("refund pagination did not advance")
		}
		from = cursor
	}
	payment, err := c.GetPayment(ctx, plan.PaymentID)
	if err != nil {
		return receipts, err
	}
	if payment.AmountChargedBack != nil {
		back, err := decimal.NewFromString(payment.AmountChargedBack.Value)
		if err != nil {
			return receipts, err
		}
		if back.IsPositive() {
			from = ""
			for {
				u, err := c.mollieURL("v2/payments/" + url.PathEscape(plan.PaymentID) + "/chargebacks")
				if err != nil {
					return receipts, err
				}
				query := u.Query()
				query.Set("limit", "250")
				if from != "" {
					query.Set("from", from)
				}
				u.RawQuery = query.Encode()
				raw, err := c.getJSON(ctx, u.String())
				if err != nil {
					return receipts, err
				}
				var page struct {
					Embedded struct {
						Chargebacks []mollie.Chargeback `json:"chargebacks"`
					} `json:"_embedded"`
					Links struct {
						Next *struct {
							Href string `json:"href"`
						} `json:"next"`
					} `json:"_links"`
				}
				if err = json.Unmarshal(raw, &page); err != nil {
					return receipts, err
				}
				for _, b := range page.Embedded.Chargebacks {
					if b.ReversedAt != nil {
						continue
					}
					if b.Amount == nil || b.Amount.Currency != plan.Currency {
						return receipts, fmt.Errorf("chargeback currency does not match its purchase")
					}
					amount, amountErr := decimal.NewFromString(b.Amount.Value)
					if amountErr != nil {
						return receipts, amountErr
					}
					cents := amount.Shift(2).IntPart()
					if cents > 0 {
						total += cents
						receipts = append(receipts, billing.SettledRefund{ObjectType: "chargeback", ID: b.ID, PaymentID: plan.PaymentID, AmountCents: cents, Currency: plan.Currency})
					}
				}
				if page.Links.Next == nil || page.Links.Next.Href == "" {
					break
				}
				next, err := url.Parse(page.Links.Next.Href)
				if err != nil {
					return receipts, err
				}
				cursor := next.Query().Get("from")
				if cursor == "" || cursor == from {
					return receipts, fmt.Errorf("chargeback pagination did not advance")
				}
				from = cursor
			}
		}
	}
	if total >= plan.AmountCents {
		return receipts, nil
	}
	if failed {
		return receipts, fmt.Errorf("refund for payment %s failed before the purchase was returned; inspect the provider and issue the missing refund", plan.PaymentID)
	}
	if waiting {
		return receipts, billing.ErrRefundSettling
	}
	return receipts, fmt.Errorf("purchase %s has no bank return in progress; inspect the provider and issue its missing refund", plan.ReferenceID)
}
