package mollie

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
	"net/url"
	"strings"
)

// RefundCompletedInvoice uses the original payment and one durable request key.
func (c *Client) RefundCompletedInvoice(ctx context.Context, in billing.CompletedInvoiceRefund) (billing.CompletedInvoiceRefundReceipt, error) {
	var out billing.CompletedInvoiceRefundReceipt
	payment, err := c.GetPayment(ctx, in.ProviderPaymentID)
	if err != nil {
		return out, err
	}
	if payment.Amount == nil {
		return out, fmt.Errorf("mollie payment has no amount")
	}
	amount, err := decimal.NewFromString(payment.Amount.Value)
	if err != nil {
		return out, err
	}
	metadataJSON, err := json.Marshal(payment.Metadata)
	if err != nil {
		return out, err
	}
	var metadata map[string]any
	if json.Unmarshal(metadataJSON, &metadata) != nil {
		return out, fmt.Errorf("mollie payment metadata is unavailable")
	}
	if string(payment.Status) != "paid" || amount.Shift(2).IntPart() != in.AmountMinor || !strings.EqualFold(payment.Amount.Currency, in.Currency) || metadata["tenant_id"] != in.TenantID || metadata["invoice_id"] != in.InvoiceID || in.ProviderCustomerID != "" && payment.CustomerID != in.ProviderCustomerID {
		return out, fmt.Errorf("settled Mollie payment does not match the reviewed invoice")
	}
	if payment.AmountChargedBack != nil {
		v, e := decimal.NewFromString(payment.AmountChargedBack.Value)
		if e != nil || v.IsPositive() {
			return out, fmt.Errorf("mollie payment has a chargeback")
		}
	}
	type receipt struct {
		ID, Status, PaymentID string
		Amount                *struct{ Currency, Value string }
		Metadata              map[string]any
	}
	var found *receipt
	base, err := c.mollieURL("v2/payments/" + url.PathEscape(in.ProviderPaymentID) + "/refunds")
	if err != nil {
		return out, err
	}
	if in.RefundID != "" {
		u := *base
		u.Path += "/" + url.PathEscape(in.RefundID)
		raw, e := c.getJSON(ctx, u.String())
		if e != nil {
			return out, e
		}
		var value receipt
		if err = json.Unmarshal(raw, &value); err != nil {
			return out, err
		}
		found = &value
	} else {
		// A full single-payment refund admits no other refund, including a pending one.
		u := *base
		q := u.Query()
		q.Set("limit", "250")
		u.RawQuery = q.Encode()
		raw, e := c.getJSON(ctx, u.String())
		if e != nil {
			return out, e
		}
		var page struct {
			Embedded struct {
				Refunds []receipt `json:"refunds"`
			} `json:"_embedded"`
			Links struct {
				Next *struct{ Href string } `json:"next"`
			} `json:"_links"`
		}
		if err = json.Unmarshal(raw, &page); err != nil {
			return out, err
		}
		if page.Links.Next != nil && page.Links.Next.Href != "" {
			return out, fmt.Errorf("mollie refund history requires manual reconciliation")
		}
		for _, candidate := range page.Embedded.Refunds {
			if candidate.Metadata["frameworks_operator_refund"] != in.RequestID {
				return out, fmt.Errorf("mollie payment has another refund")
			}
			value := candidate
			found = &value
		}
		if found == nil {
			body, marshalErr := json.Marshal(map[string]any{"amount": Amount(decimal.NewFromInt(in.AmountMinor).Shift(-2).StringFixed(2), in.Currency), "description": "FrameWorks invoice refund", "metadata": map[string]string{"frameworks_operator_refund": in.RequestID, "tenant_id": in.TenantID, "invoice_id": in.InvoiceID}})
			if marshalErr != nil {
				return out, marshalErr
			}
			raw, e = c.postJSON(ctx, base.String(), body, "operator-invoice-refund:"+in.RequestID)
			if e != nil {
				return out, e
			}
			var value receipt
			if err = json.Unmarshal(raw, &value); err != nil {
				return out, err
			}
			found = &value
		}
	}
	if found.Amount == nil {
		return out, fmt.Errorf("mollie refund has no amount")
	}
	returned, e := decimal.NewFromString(found.Amount.Value)
	if e != nil {
		return out, e
	}
	if found.ID == "" || found.PaymentID != in.ProviderPaymentID || returned.Shift(2).IntPart() != in.AmountMinor || !strings.EqualFold(found.Amount.Currency, in.Currency) || found.Metadata["frameworks_operator_refund"] != in.RequestID {
		return out, fmt.Errorf("mollie refund does not match the approved request")
	}
	state := "submitted"
	switch found.Status {
	case "refunded":
		state = "succeeded"
	case "failed", "canceled":
		state = "needs_review"
	}
	return billing.CompletedInvoiceRefundReceipt{ID: found.ID, Status: state, AmountMinor: in.AmountMinor, Currency: in.Currency}, nil
}
