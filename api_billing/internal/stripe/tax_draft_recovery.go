package stripe

import (
	"context"
	"strconv"

	billingtax "frameworks/api_billing/internal/tax"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/invoice"
)

// A subscription's captured hold metadata identifies drafts created by our
// tax guard. Operator drafts and invoices with unsupported adjustments require
// review instead of having their original money silently replaced.
func recoverStripeTaxInvoices(ctx context.Context, sub *api.Subscription, profile TaxProfile, rateID string) error {
	for _, status := range []string{"draft", "open"} {
		p := &api.InvoiceListParams{Subscription: api.String(sub.ID), Status: api.String(status)}
		p.Context = ctx
		p.Limit = api.Int64(100)
		iter := invoice.List(p)
		for iter.Next() {
			inv := iter.Invoice()
			if inv.Status != api.InvoiceStatusDraft && inv.Status != api.InvoiceStatusOpen {
				continue
			}
			if err := recoverStripeTaxInvoice(ctx, sub, inv, profile, rateID); err != nil {
				return err
			}
		}
		if err := iter.Err(); err != nil {
			return err
		}
	}
	return nil
}

func recoverStripeTaxInvoice(ctx context.Context, sub *api.Subscription, inv *api.Invoice, profile TaxProfile, rateID string) error {
	review := func() error {
		return billingtax.Review("stripe_held_invoice_review", "A held Stripe invoice requires billing review before collection resumes")
	}
	var meta map[string]string
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil {
		meta = inv.Parent.SubscriptionDetails.Metadata
	}
	started, parseErr := strconv.ParseInt(meta["frameworks_tax_hold_started"], 10, 64)
	owned := parseErr == nil && started > 0 && inv.Created >= started && meta["frameworks_tax_hold_started"] == sub.Metadata["frameworks_tax_hold_started"] && meta["frameworks_tax_review"] == "true" && meta["frameworks_tax_pause_owned"] == "true" && meta["tenant_id"] == sub.Metadata["tenant_id"]
	if !owned {
		if sub.Metadata["frameworks_tax_review"] == "true" {
			return review()
		}
		return nil
	}
	if inv.Customer == nil || sub.Customer == nil || inv.Customer.ID != sub.Customer.ID || inv.BillingReason != api.InvoiceBillingReasonSubscriptionCycle || inv.AutoAdvance || (sub.PauseCollection != nil && (sub.Metadata["frameworks_tax_pause_owned"] != "true" || sub.PauseCollection.Behavior != api.SubscriptionPauseCollectionBehaviorKeepAsDraft || sub.PauseCollection.ResumesAt != 0)) {
		return review()
	}
	if inv.Status == api.InvoiceStatusDraft {
		lines := inv.Lines
		if lines == nil || lines.HasMore {
			p := &api.InvoiceListLinesParams{Invoice: api.String(inv.ID)}
			p.Context = ctx
			all := invoice.ListLines(p)
			lines = &api.InvoiceLineItemList{}
			for all.Next() {
				lines.Data = append(lines.Data, all.InvoiceLineItem())
			}
			if err := all.Err(); err != nil {
				return err
			}
		}
		if len(lines.Data) == 0 || len(lines.Data) > 250 || len(inv.Discounts) > 0 {
			return review()
		}
		updates := []*api.InvoiceUpdateLinesLineParams{}
		for _, line := range lines.Data {
			if line.ID == "" || line.Amount < 0 || line.Currency != inv.Currency || line.Parent == nil || line.Parent.SubscriptionItemDetails == nil || line.Parent.SubscriptionItemDetails.Subscription != sub.ID || line.Parent.SubscriptionItemDetails.Proration || len(line.DiscountAmounts) > 0 || len(line.PretaxCreditAmounts) > 0 {
				return review()
			}
			updates = append(updates, &api.InvoiceUpdateLinesLineParams{ID: api.String(line.ID), TaxRates: []*string{api.String(rateID)}})
		}
		update := &api.InvoiceParams{AutoAdvance: api.Bool(false), AutomaticTax: &api.InvoiceAutomaticTaxParams{Enabled: api.Bool(false)}, DefaultTaxRates: []*string{api.String(rateID)}, Footer: api.String(profile.SupplierIdentity + "\n" + profile.Wording), Metadata: map[string]string{"tenant_id": sub.Metadata["tenant_id"], "frameworks_tax_profile": profile.Fingerprint(), "frameworks_tax_recovered_hold": "true"}}
		update.Context = ctx
		if _, err := invoice.Update(inv.ID, update); err != nil {
			return err
		}
		updateLines := &api.InvoiceUpdateLinesParams{Lines: updates}
		updateLines.Context = ctx
		if _, err := invoice.UpdateLines(inv.ID, updateLines); err != nil {
			return err
		}
		finalize := &api.InvoiceFinalizeInvoiceParams{AutoAdvance: api.Bool(false)}
		finalize.Context = ctx
		finalize.SetIdempotencyKey("tax-hold-finalize:" + inv.ID)
		var err error
		if inv, err = invoice.FinalizeInvoice(inv.ID, finalize); err != nil {
			return err
		}
	}
	if inv.Status == api.InvoiceStatusOpen {
		if inv.Metadata["frameworks_tax_recovered_hold"] != "true" || inv.Metadata["frameworks_tax_profile"] != profile.Fingerprint() {
			return review()
		}
		pay := &api.InvoicePayParams{}
		pay.Context = ctx
		pay.SetIdempotencyKey("tax-hold-pay:" + inv.ID)
		paid, err := invoice.Pay(inv.ID, pay)
		if err != nil {
			return err
		}
		if paid.Status != api.InvoiceStatusPaid {
			return review()
		}
	}
	return nil
}
