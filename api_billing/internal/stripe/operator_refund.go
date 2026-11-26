package stripe

import (
	"context"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/paymentintent"
	"github.com/stripe/stripe-go/v85/refund"
	"strings"
)

// RefundCompletedInvoice verifies the exact captured payment before a full return.
// The stable request key also recovers a lost creation response without another refund.
func (c *Client) RefundCompletedInvoice(ctx context.Context, in billing.CompletedInvoiceRefund) (billing.CompletedInvoiceRefundReceipt, error) {
	var out billing.CompletedInvoiceRefundReceipt
	pp := &api.PaymentIntentParams{}
	pp.Context = ctx
	pp.AddExpand("latest_charge")
	pi, err := paymentintent.Get(in.ProviderPaymentID, pp)
	if err != nil {
		return out, err
	}
	if pi.Status != api.PaymentIntentStatusSucceeded || pi.AmountReceived != in.AmountMinor || !strings.EqualFold(string(pi.Currency), in.Currency) || pi.Metadata["tenant_id"] != in.TenantID || pi.Metadata["invoice_id"] != in.InvoiceID || pi.LatestCharge == nil || pi.LatestCharge.Disputed {
		return out, fmt.Errorf("captured Stripe payment does not match the reviewed invoice")
	}
	if in.ProviderCustomerID != "" && (pi.Customer == nil || pi.Customer.ID != in.ProviderCustomerID) {
		return out, fmt.Errorf("stripe payment customer differs from the original receipt")
	}
	var found *api.Refund
	if in.RefundID != "" {
		rp := &api.RefundParams{}
		rp.Context = ctx
		found, err = refund.Get(in.RefundID, rp)
		if err != nil {
			return out, err
		}
	} else {
		list := &api.RefundListParams{Charge: api.String(pi.LatestCharge.ID)}
		list.Context = ctx
		iter := refund.List(list)
		for iter.Next() {
			candidate := iter.Refund()
			if candidate.Metadata["frameworks_operator_refund"] == in.RequestID {
				found = candidate
				continue
			}
			return out, fmt.Errorf("stripe charge has another refund; reconcile it before continuing")
		}
		if listErr := iter.Err(); listErr != nil {
			return out, listErr
		}
		if found == nil {
			if pi.LatestCharge.AmountRefunded != 0 {
				return out, fmt.Errorf("stripe charge is already partly returned")
			}
			rp := &api.RefundParams{PaymentIntent: api.String(in.ProviderPaymentID), Amount: api.Int64(in.AmountMinor)}
			rp.Context = ctx
			rp.SetIdempotencyKey("operator-invoice-refund:" + in.RequestID)
			rp.Metadata = map[string]string{"frameworks_operator_refund": in.RequestID, "tenant_id": in.TenantID, "invoice_id": in.InvoiceID}
			found, err = refund.New(rp)
			if err != nil {
				return out, err
			}
		}
	}
	if found.Amount != in.AmountMinor || !strings.EqualFold(string(found.Currency), in.Currency) || found.PaymentIntent == nil || found.PaymentIntent.ID != in.ProviderPaymentID || found.Metadata["frameworks_operator_refund"] != in.RequestID {
		return out, fmt.Errorf("stripe refund does not match the approved request")
	}
	state := "submitted"
	switch found.Status {
	case api.RefundStatusSucceeded:
		state = "succeeded"
	case api.RefundStatusFailed, api.RefundStatusCanceled:
		state = "needs_review"
	}
	return billing.CompletedInvoiceRefundReceipt{ID: found.ID, Status: state, ChargeID: pi.LatestCharge.ID, AmountMinor: found.Amount, Currency: strings.ToUpper(string(found.Currency))}, nil
}
