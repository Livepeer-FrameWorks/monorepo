package stripe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/charge"
	"github.com/stripe/stripe-go/v85/dispute"
	"github.com/stripe/stripe-go/v85/refund"
)

// InspectRefundSettlement reads bank outcomes and retries a confirmed failed
// refund through the original purchase compensation and its idempotency keys.
func (c *Client) InspectRefundSettlement(ctx context.Context, plan billing.RefundSettlement) ([]billing.SettledRefund, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	params := &api.RefundListParams{Charge: api.String(plan.ChargeID)}
	params.Context = ctx
	iter := refund.List(params)
	var receipts []billing.SettledRefund
	get := &api.ChargeParams{}
	get.Context = ctx
	ch, err := charge.Get(plan.ChargeID, get)
	if err != nil {
		return receipts, err
	}
	if ch.Disputed {
		lost, _, err := lostDisputeReceipts(ctx, plan.ChargeID)
		if err != nil {
			return receipts, err
		}
		for i := range lost {
			lost[i].PaymentID = plan.PaymentID
		}
		receipts = append(receipts, lost...)
	}
	waiting := false
	var total int64
	for _, receipt := range receipts {
		total += receipt.AmountCents
	}
	failed := false
	for iter.Next() {
		r := iter.Refund()
		if !strings.EqualFold(string(r.Currency), plan.Currency) {
			return receipts, fmt.Errorf("refund %s has a different currency from its purchase", r.ID)
		}
		switch r.Status {
		case api.RefundStatusSucceeded:
			if r.Amount > 0 {
				total += r.Amount
				receipts = append(receipts, billing.SettledRefund{ObjectType: "refund", ID: r.ID, PaymentID: plan.PaymentID, ChargeID: plan.ChargeID, AmountCents: r.Amount, Currency: strings.ToUpper(string(r.Currency))})
			}
		case api.RefundStatusFailed, api.RefundStatusCanceled:
			failed = true
		default:
			waiting = true
		}
	}
	if err := iter.Err(); err != nil {
		return receipts, err
	}
	if total >= plan.AmountCents {
		return receipts, nil
	}
	if waiting {
		return receipts, billing.ErrRefundSettling
	}
	if failed {
		key := ""
		switch plan.ReferenceKind {
		case "checkout":
			key = "checkout-expiry-refund:" + plan.ReferenceID
		case "tier_upgrade":
			key = "unapplied-upgrade-refund:" + plan.ReferenceID
		}
		if key == "" || plan.InvoiceID == "" {
			return receipts, fmt.Errorf("failed refund for charge %s has no original purchase invoice", plan.ChargeID)
		}
		var retried billing.CheckoutRecoveryResult
		if err := c.refundInvoicePaymentsForCharge(ctx, plan.InvoiceID, key, &retried, plan.ReferenceKind, plan.ReferenceID, plan.ChargeID); err != nil {
			return receipts, err
		}
		return receipts, billing.ErrRefundSettling
	}

	return receipts, fmt.Errorf("purchase %s has no bank return in progress; inspect the provider and issue its missing refund", plan.ReferenceID)
}

func lostDisputeReceipts(ctx context.Context, chargeID string) ([]billing.SettledRefund, bool, error) {
	p := &api.DisputeListParams{Charge: api.String(chargeID)}
	p.Context = ctx
	iter := dispute.List(p)
	var receipts []billing.SettledRefund
	open := false
	found := false
	for iter.Next() {
		d := iter.Dispute()
		found = true
		switch d.Status {
		case api.DisputeStatusLost:
			receipts = append(receipts, billing.SettledRefund{ObjectType: "dispute", ID: d.ID, ChargeID: chargeID, AmountCents: d.Amount, Currency: strings.ToUpper(string(d.Currency))})
		case api.DisputeStatusWon:
		default:
			open = true
		}
	}
	if err := iter.Err(); err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, fmt.Errorf("disputed charge %s has no dispute evidence", chargeID)
	}
	return receipts, open, nil
}
