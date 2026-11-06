package stripe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/charge"
	"github.com/stripe/stripe-go/v85/invoice"
	"github.com/stripe/stripe-go/v85/invoicepayment"
	"github.com/stripe/stripe-go/v85/paymentintent"
	stripeprice "github.com/stripe/stripe-go/v85/price"
	"github.com/stripe/stripe-go/v85/subscription"
	"github.com/stripe/stripe-go/v85/taxrate"
)

// UpgradeMonthlySubscription replaces the existing price, invoices the new
// month now and credits Stripe's unused paid fee. Metadata makes a committed
// update discoverable even after the request's idempotency window expires.
func (c *Client) UpgradeMonthlySubscription(ctx context.Context, request billing.TierUpgradeRequest) (billing.TierUpgradeReceipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	get := &stripe.SubscriptionParams{}
	get.Context = ctx
	get.AddExpand("latest_invoice")
	sub, err := subscription.Get(request.SubscriptionID, get)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	if sub.Customer == nil || sub.Customer.ID != request.CustomerID || sub.Metadata["tenant_id"] != request.TenantID || sub.Items == nil || len(sub.Items.Data) != 1 {
		return billing.TierUpgradeReceipt{}, errors.New("stripe upgrade subscription does not match its tenant, customer and single base-fee item")
	}
	item := sub.Items.Data[0]
	if sub.Metadata["billing_tier_upgrade_id"] == request.ID {
		if item.Price == nil || item.Price.ID != request.PriceID {
			return billing.TierUpgradeReceipt{}, errors.New("stripe upgrade receipt has a different price")
		}
		return locateStripeUpgradeReceipt(ctx, request)
	}
	if request.LocateOnly {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: no provider update exists for this purchase", billing.ErrTierUpgradeRejected)
	}
	if item.Quantity != 1 || item.Price == nil ||
		item.Price.UnitAmount < 0 || item.Price.UnitAmount > request.BaseCents ||
		item.Price.Currency != stripe.CurrencyEUR || item.Price.Recurring == nil ||
		item.Price.Recurring.Interval != stripe.PriceRecurringIntervalMonth || item.Price.Recurring.IntervalCount != 1 {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: the native Stripe fee must be one monthly EUR unit no higher than the replacement fee", billing.ErrTierUpgradeRejected)
	}
	if sub.Status != stripe.SubscriptionStatusActive && sub.Status != stripe.SubscriptionStatusTrialing {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: Stripe subscription is %s", billing.ErrTierUpgradeRejected, sub.Status)
	}
	if sub.CancelAtPeriodEnd || sub.CancelAt > 0 {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: confirm subscription resumption before upgrading", billing.ErrTierUpgradeRejected)
	}
	if sub.LatestInvoice != nil && sub.LatestInvoice.Status != stripe.InvoiceStatusPaid {
		return billing.TierUpgradeReceipt{}, errors.Join(billing.ErrTierUpgradeRejected, billing.ErrTierUpgradeUnsettled)
	}
	if item.Price.UnitAmount > 0 && (sub.LatestInvoice == nil || sub.LatestInvoice.AmountPaid <= 0) {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: the previous paid fee cannot be established from Stripe's latest invoice; reconcile its payment before upgrading", billing.ErrTierUpgradeRejected)
	}
	paidBase, err := validateStripePaidMonthlyBase(ctx, sub, item)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	if sub.LatestInvoice != nil && sub.LatestInvoice.AmountPaid > 0 {
		if err = retainedUpgradeFee(ctx, sub.LatestInvoice.ID); err != nil {
			return billing.TierUpgradeReceipt{}, err
		}
	}
	priceParams := &stripe.PriceParams{}
	priceParams.Context = ctx
	target, priceErr := stripeprice.Get(request.PriceID, priceParams)
	if priceErr != nil {
		return billing.TierUpgradeReceipt{}, priceErr
	}
	if !target.Active || target.UnitAmount != request.BaseCents || target.Currency != stripe.CurrencyEUR || target.Recurring == nil || target.Recurring.Interval != stripe.PriceRecurringIntervalMonth || target.Recurring.IntervalCount != 1 {
		return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: Stripe monthly price differs from the accepted EUR fee", billing.ErrTierUpgradeRejected)
	}
	credit, err := originalStripeUpgradeCredit(ctx, request, sub, item, paidBase)
	if err != nil {
		return billing.TierUpgradeReceipt{}, err
	}
	params := &stripe.SubscriptionParams{
		Items:                 []*stripe.SubscriptionItemsParams{{ID: stripe.String(item.ID), Price: stripe.String(request.PriceID)}},
		BillingCycleAnchorNow: stripe.Bool(true),
		ProrationBehavior:     stripe.String("none"),
		PaymentBehavior:       stripe.String("error_if_incomplete"),
	}
	if credit != nil {
		params.AddInvoiceItems = []*stripe.SubscriptionAddInvoiceItemParams{credit}
	}
	params.Context = ctx
	params.SetIdempotencyKey("tier-upgrade:" + request.ID)
	params.AddMetadata("tenant_id", request.TenantID)
	params.AddMetadata("tier_id", request.TierID)
	params.AddMetadata("billing_tier_upgrade_id", request.ID)
	updated, err := subscription.Update(request.SubscriptionID, params)
	if err != nil {
		var providerErr *stripe.Error
		if errors.As(err, &providerErr) && providerErr.HTTPStatusCode >= 400 && providerErr.HTTPStatusCode < 500 && providerErr.HTTPStatusCode != 408 && providerErr.HTTPStatusCode != 409 && providerErr.HTTPStatusCode != 429 {
			return billing.TierUpgradeReceipt{}, fmt.Errorf("%w: %w", billing.ErrTierUpgradeRejected, err)
		}
		return billing.TierUpgradeReceipt{}, err
	}
	return stripeUpgradeReceipt(updated)
}

// Native proration uses the subscriber's actual price, which may predate the
// current catalog. Bind it to the paid invoice's full monthly base line rather
// than allowing an unpaid price edit or an unrelated usage payment as evidence.
func validateStripePaidMonthlyBase(
	ctx context.Context, sub *stripe.Subscription, item *stripe.SubscriptionItem,
) (*stripe.InvoiceLineItem, error) {
	reject := func() error {
		return fmt.Errorf("%w: the native monthly fee has no matching undiscounted paid base line; reconcile its invoice before upgrading", billing.ErrTierUpgradeRejected)
	}
	if len(sub.Discounts) > 0 || len(item.Discounts) > 0 {
		return nil, reject()
	}
	if item.Price.UnitAmount == 0 {
		return nil, nil
	}
	if sub.LatestInvoice == nil || sub.LatestInvoice.ID == "" {
		return nil, reject()
	}
	lines := sub.LatestInvoice.Lines
	if lines == nil || lines.HasMore {
		params := &stripe.InvoiceListLinesParams{Invoice: stripe.String(sub.LatestInvoice.ID)}
		params.Context = ctx
		iter := invoice.ListLines(params)
		lines = &stripe.InvoiceLineItemList{}
		for iter.Next() {
			lines.Data = append(lines.Data, iter.InvoiceLineItem())
		}
		if err := iter.Err(); err != nil {
			return nil, err
		}
	}
	matches := 0
	var matched *stripe.InvoiceLineItem
	for _, line := range lines.Data {
		// The item identifies the line: a restored price keeps its fee under a
		// new price ID, so the amount below is what binds the two.
		if line.Pricing == nil || line.Pricing.PriceDetails == nil || line.Pricing.PriceDetails.Price == nil ||
			line.Parent == nil || line.Parent.SubscriptionItemDetails == nil {
			continue
		}
		parent := line.Parent.SubscriptionItemDetails
		if parent.Proration || parent.Subscription != sub.ID || parent.SubscriptionItem != item.ID ||
			line.Period == nil || line.Period.Start != item.CurrentPeriodStart || line.Period.End != item.CurrentPeriodEnd ||
			line.Period.End <= line.Period.Start {
			continue
		}
		if line.Quantity != 1 || line.Currency != stripe.CurrencyEUR || line.Amount != item.Price.UnitAmount ||
			len(line.DiscountAmounts) > 0 || len(line.PretaxCreditAmounts) > 0 {
			return nil, reject()
		}
		matches++
		matched = line
	}
	if matches != 1 {
		return nil, reject()
	}
	return matched, nil
}

func originalStripeUpgradeCredit(ctx context.Context, request billing.TierUpgradeRequest, sub *stripe.Subscription, item *stripe.SubscriptionItem, line *stripe.InvoiceLineItem) (*stripe.SubscriptionAddInvoiceItemParams, error) {
	if line == nil || line.Amount == 0 {
		return nil, nil
	}
	reject := func(reason string) (*stripe.SubscriptionAddInvoiceItemParams, error) {
		return nil, fmt.Errorf("%w: original paid tax credit requires review: %s", billing.ErrTierUpgradeRejected, reason)
	}
	if sub.LatestInvoice.PostPaymentCreditNotesAmount > 0 || sub.LatestInvoice.PrePaymentCreditNotesAmount > 0 {
		return reject("source invoice already has credit notes")
	}
	if !request.OldPeriodEnd.IsZero() && request.OldPeriodEnd.Unix() != line.Period.End {
		return reject("provider paid period advanced beyond the accepted boundary")
	}
	if item.Price.Product == nil || item.Price.Product.ID == "" {
		return reject("source product identity missing")
	}
	if request.RequestedAt.IsZero() {
		return reject("accepted upgrade time missing")
	}
	at := request.RequestedAt.UTC().Unix()
	if at < line.Period.Start || at >= line.Period.End {
		return reject("accepted upgrade time is outside its paid source period")
	}
	whole := line.Period.End - line.Period.Start
	remaining := min(max(line.Period.End-at, 0), whole)
	if whole <= 0 || remaining <= 0 {
		return reject("source paid month no longer overlaps the upgrade")
	}
	minor := decimal.NewFromInt(line.Amount).Mul(decimal.NewFromInt(remaining)).Div(decimal.NewFromInt(whole)).Round(0).IntPart()
	if minor == 0 {
		return nil, nil
	}
	var originalVAT int64
	for _, tax := range line.Taxes {
		if tax != nil {
			originalVAT += tax.Amount
		}
	}
	if originalVAT < 0 {
		return reject("source tax amount is negative")
	}
	rates := []*string{}
	rateEvidence := []providerCreditRate{}
	if originalVAT > 0 {
		for _, tax := range line.Taxes {
			if tax == nil || tax.Amount == 0 {
				continue
			}
			if tax.Amount < 0 || tax.TaxBehavior != stripe.InvoiceLineItemTaxTaxBehaviorExclusive || tax.TaxRateDetails == nil || tax.TaxRateDetails.TaxRate == "" {
				return reject("original exclusive tax rate is not established")
			}
			p := &stripe.TaxRateParams{}
			p.Context = ctx
			rate, err := taxrate.Get(tax.TaxRateDetails.TaxRate, p)
			if err != nil {
				return nil, err
			}
			calculated := decimal.NewFromInt(line.Amount).Mul(decimal.NewFromFloat(rate.Percentage)).Div(decimal.NewFromInt(100)).Round(0).IntPart()
			if rate.Inclusive || calculated != tax.Amount {
				return reject("original tax amount differs from its retained rate")
			}
			bps := decimal.NewFromFloat(rate.Percentage).Mul(decimal.NewFromInt(100))
			if !bps.IsInteger() {
				return reject("original VAT precision is unsupported")
			}
			rateEvidence = append(rateEvidence, providerCreditRate{ID: rate.ID, RateBPS: bps.IntPart()})
			rates = append(rates, stripe.String(rate.ID))
		}
	} else {
		list := &stripe.TaxRateListParams{Active: stripe.Bool(true)}
		list.Context = ctx
		list.Limit = stripe.Int64(100)
		iter := taxrate.List(list)
		for iter.Next() {
			rate := iter.TaxRate()
			if !rate.Inclusive && rate.Percentage == 0 && rate.Metadata["frameworks_original_untaxed"] == "true" {
				rates = []*string{stripe.String(rate.ID)}
				break
			}
		}
		if err := iter.Err(); err != nil {
			return nil, err
		}
		if len(rates) == 0 {
			p := &stripe.TaxRateParams{DisplayName: stripe.String("Original period VAT"), Inclusive: stripe.Bool(false), Percentage: stripe.Float64(0), TaxType: stripe.String("vat"), Metadata: map[string]string{"frameworks_original_untaxed": "true"}}
			p.Context = ctx
			p.SetIdempotencyKey("frameworks-original-untaxed-v1")
			rate, err := taxrate.New(p)
			if err != nil {
				return nil, err
			}
			if rate.ID == "" || rate.Inclusive || rate.Percentage != 0 {
				return reject("provider did not confirm the original zero-tax rate")
			}
			rates = []*string{stripe.String(rate.ID)}
		}
	}
	if originalVAT == 0 {
		rateEvidence = []providerCreditRate{{ID: stripe.StringValue(rates[0]), RateBPS: 0}}
	}
	evidence, err := json.Marshal(rateEvidence)
	if err != nil {
		return nil, err
	}
	if len(evidence) > 500 {
		return reject("original tax evidence exceeds provider metadata limits")
	}
	return &stripe.SubscriptionAddInvoiceItemParams{Quantity: stripe.Int64(1), Discountable: stripe.Bool(false), PriceData: &stripe.InvoiceItemPriceDataParams{Currency: stripe.String("eur"), Product: stripe.String(item.Price.Product.ID), TaxBehavior: stripe.String("exclusive"), UnitAmount: stripe.Int64(-minor)}, TaxRates: rates, Period: &stripe.SubscriptionAddInvoiceItemPeriodParams{Start: &stripe.SubscriptionAddInvoiceItemPeriodStartParams{Type: stripe.String("timestamp"), Timestamp: stripe.Int64(at)}, End: &stripe.SubscriptionAddInvoiceItemPeriodEndParams{Type: stripe.String("timestamp"), Timestamp: stripe.Int64(line.Period.End)}}, Metadata: map[string]string{"frameworks_credit_invoice": sub.LatestInvoice.ID, "frameworks_credit_line": line.ID, "frameworks_credit_original_net": strconv.FormatInt(line.Amount, 10), "frameworks_credit_original_vat": strconv.FormatInt(originalVAT, 10), "frameworks_credit_rates": string(evidence)}}, nil
}

// Renewal changes latest_invoice and the subscription item's current period.
// The finalized upgrade invoice retains the purchase metadata and its original
// replacement period, so replay cannot substitute a later renewal receipt.
func locateStripeUpgradeReceipt(ctx context.Context, request billing.TierUpgradeRequest) (billing.TierUpgradeReceipt, error) {
	params := &stripe.InvoiceListParams{Subscription: stripe.String(request.SubscriptionID), CreatedRange: &stripe.RangeQueryParams{GreaterThanOrEqual: request.RequestedAt.Unix()}}
	params.Context = ctx
	invoices := invoice.List(params)
	var receipt billing.TierUpgradeReceipt
	for invoices.Next() {
		inv := invoices.Invoice()
		if inv.BillingReason != stripe.InvoiceBillingReasonSubscriptionUpdate || inv.Parent == nil || inv.Parent.SubscriptionDetails == nil {
			continue
		}
		meta := inv.Parent.SubscriptionDetails.Metadata
		if meta["billing_tier_upgrade_id"] != request.ID || meta["tenant_id"] != request.TenantID {
			continue
		}
		if receipt.Applied {
			return receipt, errors.New("multiple provider invoices match the accepted upgrade")
		}
		lines := inv.Lines
		if lines == nil || lines.HasMore {
			p := &stripe.InvoiceListLinesParams{Invoice: stripe.String(inv.ID)}
			p.Context = ctx
			iter := invoice.ListLines(p)
			lines = &stripe.InvoiceLineItemList{}
			for iter.Next() {
				lines.Data = append(lines.Data, iter.InvoiceLineItem())
			}
			if err := iter.Err(); err != nil {
				return receipt, err
			}
		}
		for _, line := range lines.Data {
			if line.Amount <= 0 || line.Pricing == nil || line.Pricing.PriceDetails == nil || line.Pricing.PriceDetails.Price == nil || line.Pricing.PriceDetails.Price.ID != request.PriceID || line.Period == nil || line.Period.End <= line.Period.Start {
				continue
			}
			receipt = billing.TierUpgradeReceipt{Applied: true, PeriodStart: time.Unix(line.Period.Start, 0).UTC(), PeriodEnd: time.Unix(line.Period.End, 0).UTC(), PaidReceiptID: inv.ID}
		}
	}
	if err := invoices.Err(); err != nil {
		return receipt, err
	}
	if !receipt.Applied {
		return receipt, errors.New("accepted upgrade invoice has not been located; retain the pricing fence")
	}
	return receipt, nil
}

// Stripe's paid invoice status survives refunds and disputes. Native proration
// must not credit that fee unless its payment is still retained.
func retainedUpgradeFee(ctx context.Context, invoiceID string) error {
	params := &stripe.InvoicePaymentListParams{Invoice: stripe.String(invoiceID), Status: stripe.String("paid")}
	params.Context = ctx
	payments := invoicepayment.List(params)
	found := false
	for payments.Next() {
		payment := payments.InvoicePayment()
		if payment.Payment == nil {
			return errors.New("paid upgrade basis has no payment reference")
		}
		var ch *stripe.Charge
		var err error
		switch {
		case payment.Payment.Charge != nil:
			p := &stripe.ChargeParams{}
			p.Context = ctx
			ch, err = charge.Get(payment.Payment.Charge.ID, p)
		case payment.Payment.PaymentIntent != nil:
			p := &stripe.PaymentIntentParams{}
			p.Context = ctx
			p.AddExpand("latest_charge")
			var pi *stripe.PaymentIntent
			pi, err = paymentintent.Get(payment.Payment.PaymentIntent.ID, p)
			if err == nil {
				ch = pi.LatestCharge
			}
		}
		if err != nil {
			return err
		}
		if ch != nil && ch.Disputed {
			lost, open, disputeErr := lostDisputeReceipts(ctx, ch.ID)
			if disputeErr != nil {
				return errors.Join(billing.ErrTierUpgradeRejected, disputeErr)
			}
			if len(lost) > 0 || open {
				return fmt.Errorf("%w: the previous fee has an unresolved or lost dispute", billing.ErrTierUpgradeRejected)
			}
		}
		if ch == nil || ch.Refunded || ch.AmountRefunded > 0 {
			return fmt.Errorf("%w: the previous fee has a refund, dispute or missing charge; reconcile it before upgrading", billing.ErrTierUpgradeRejected)
		}
		found = true
	}
	if err := payments.Err(); err != nil {
		return err
	}
	if !found {
		return errors.New("paid upgrade basis has no settled invoice payment")
	}
	return nil
}

func stripeUpgradeReceipt(sub *stripe.Subscription) (billing.TierUpgradeReceipt, error) {
	if sub.Items == nil || len(sub.Items.Data) != 1 || sub.Items.Data[0].CurrentPeriodStart <= 0 || sub.Items.Data[0].CurrentPeriodEnd <= sub.Items.Data[0].CurrentPeriodStart {
		return billing.TierUpgradeReceipt{}, errors.New("stripe upgrade has no authoritative billing period")
	}
	return billing.TierUpgradeReceipt{Applied: true, PaidReceiptID: func() string {
		if sub.LatestInvoice != nil {
			return sub.LatestInvoice.ID
		}
		return ""
	}(), PeriodStart: time.Unix(sub.Items.Data[0].CurrentPeriodStart, 0).UTC(), PeriodEnd: time.Unix(sub.Items.Data[0].CurrentPeriodEnd, 0).UTC()}, nil
}
