package stripe

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/invoice"
	"github.com/stripe/stripe-go/v85/invoicepayment"
	"github.com/stripe/stripe-go/v85/paymentintent"
	"github.com/stripe/stripe-go/v85/subscription"
)

// EndSubscriptionForFallback stops future billing while retaining a recovered or paid term.
// An in-flight debit remains fenced until Stripe supplies its final outcome.
func (c *Client) EndSubscriptionForFallback(ctx context.Context, request billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result billing.SubscriptionEndResult
	params := &stripe.SubscriptionParams{}
	params.Context = ctx
	params.AddExpand("latest_invoice")
	sub, err := subscription.Get(request.SubscriptionID, params)
	if stripeObjectMissing(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if sub.ID != request.SubscriptionID || sub.Customer == nil || sub.Customer.ID != request.CustomerID || sub.Status == "" {
		return result, errors.New("stripe fallback subscription does not match its customer or has no status")
	}
	if request.AllowRecovery && !sub.CancelAtPeriodEnd && sub.CancelAt == 0 {
		if sub.Status == stripe.SubscriptionStatusTrialing {
			result.Recovered = true
			return result, nil
		}
		if sub.Status == stripe.SubscriptionStatusActive && sub.LatestInvoice != nil && sub.LatestInvoice.Status == stripe.InvoiceStatusPaid {
			params := &stripe.InvoiceParams{}
			params.Context = ctx
			latest, readErr := invoice.Get(sub.LatestInvoice.ID, params)
			if readErr != nil {
				return result, readErr
			}
			retained, readErr := retainedFallbackInvoiceFee(ctx, latest)
			if readErr != nil {
				return result, readErr
			}
			if retained {
				result.Recovered = true
				return result, nil
			}
		}
	}
	prices := make(map[string]bool)
	if sub.Items != nil {
		for _, item := range sub.Items.Data {
			if item.Price != nil {
				prices[item.Price.ID] = true
			}
		}
	}
	if sub.Status != stripe.SubscriptionStatusCanceled && sub.Status != stripe.SubscriptionStatusIncompleteExpired {
		cancelParams := &stripe.SubscriptionCancelParams{Prorate: stripe.Bool(false), InvoiceNow: stripe.Bool(false)}
		cancelParams.Context = ctx
		cancelParams.AddExpand("latest_invoice")
		sub, err = subscription.Cancel(request.SubscriptionID, cancelParams)
		if err != nil {
			return result, err
		}
		if sub.Status != stripe.SubscriptionStatusCanceled {
			return result, fmt.Errorf("stripe has not confirmed cancellation: %s", sub.Status)
		}
	}
	if sub.LatestInvoice == nil || sub.LatestInvoice.ID == "" {
		return result, nil
	}
	get := &stripe.InvoiceParams{}
	get.Context = ctx
	latest, err := invoice.Get(sub.LatestInvoice.ID, get)
	if err != nil {
		return result, err
	}
	if latest.Status == stripe.InvoiceStatusPaid {
		retained, readErr := retainedFallbackInvoiceFee(ctx, latest)
		if readErr != nil || !retained {
			return result, readErr
		}
		result.PaidUntil, err = paidSubscriptionTermEnd(ctx, latest, prices)
		return result, err
	}
	payments := &stripe.InvoicePaymentListParams{Invoice: stripe.String(latest.ID)}
	payments.Context = ctx
	iter := invoicepayment.List(payments)
	for iter.Next() {
		item := iter.InvoicePayment()
		if item.Payment == nil || item.Payment.PaymentIntent == nil {
			continue
		}
		getIntent := &stripe.PaymentIntentParams{}
		getIntent.Context = ctx
		intent, readErr := paymentintent.Get(item.Payment.PaymentIntent.ID, getIntent)
		if readErr != nil {
			return result, readErr
		}
		if intent.Status == stripe.PaymentIntentStatusProcessing || intent.Status == stripe.PaymentIntentStatusRequiresCapture || intent.Status == stripe.PaymentIntentStatusSucceeded {
			result.Pending = true
		}
	}
	return result, iter.Err()
}

func stripeObjectMissing(err error) bool {
	var providerErr *stripe.Error
	return errors.As(err, &providerErr) && providerErr.HTTPStatusCode == 404
}

// The invoice header describes its invoicing window; the paid base-fee line
// names the service term it bought, including an advance-billed renewal.
func paidSubscriptionTermEnd(ctx context.Context, latest *stripe.Invoice, prices map[string]bool) (time.Time, error) {
	var until time.Time
	lines := latest.Lines
	if lines == nil || lines.HasMore {
		params := &stripe.InvoiceListLinesParams{Invoice: stripe.String(latest.ID)}
		params.Context = ctx
		iter := invoice.ListLines(params)
		lines = &stripe.InvoiceLineItemList{}
		for iter.Next() {
			lines.Data = append(lines.Data, iter.InvoiceLineItem())
		}
		if err := iter.Err(); err != nil {
			return until, err
		}
	}
	for _, line := range lines.Data {
		if line.Amount <= 0 || line.Period == nil || line.Pricing == nil || line.Pricing.PriceDetails == nil || line.Pricing.PriceDetails.Price == nil || !prices[line.Pricing.PriceDetails.Price.ID] {
			continue
		}
		end := time.Unix(line.Period.End, 0).UTC()
		if end.After(until) {
			until = end
		}
	}
	if until.IsZero() {
		return until, errors.New("paid Stripe invoice has no matching subscription service term")
	}
	return until, nil
}

// Invoice paid status survives a refund or chargeback. Only retained funding
// may recover collection or extend a cancelled subscription's paid access.
func retainedFallbackInvoiceFee(ctx context.Context, invoice *stripe.Invoice) (bool, error) {
	if invoice.Status != stripe.InvoiceStatusPaid {
		return false, nil
	}
	if invoice.AmountPaid == 0 {
		// Discounts, native proration and retained customer credit can fund a
		// whole term without a new charge. The service line supplies its term.
		credit := invoice.StartingBalance < 0 && invoice.EndingBalance-invoice.StartingBalance >= invoice.Total
		return invoice.AmountDue == 0 && (invoice.Total <= 0 || credit), nil
	}
	err := retainedUpgradeFee(ctx, invoice.ID)
	if errors.Is(err, billing.ErrTierUpgradeRejected) {
		return false, nil
	}
	return err == nil, err
}
