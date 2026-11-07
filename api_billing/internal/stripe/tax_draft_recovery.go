package stripe

import (
	"context"
	"errors"
	"strconv"

	billingtax "frameworks/api_billing/internal/tax"
	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/invoice"
)

// A subscription's captured hold metadata identifies drafts created by our
// tax guard. Operator invoices are outside this recovery; owned invoices with
// unsupported adjustments require review before their money can change.
func recoverStripeTaxInvoices(ctx context.Context, sub *api.Subscription, profile TaxProfile, rateID string) (*TaxRecoveryReport, error) {
	report := &TaxRecoveryReport{TenantID: sub.Metadata["tenant_id"], SubscriptionID: sub.ID, ProfileSHA: profile.Fingerprint(), RecoveryPending: true}
	if sub.Customer != nil {
		report.CustomerID = sub.Customer.ID
	}
	_, contextSHA, err := taxRecoverySubscriptionInputs(sub, report.ProfileSHA)
	if err != nil {
		return report, err
	}
	report.ContextSHA = contextSHA
	var failures error
	reported := map[string]bool{}
	for _, status := range []string{"draft", "open"} {
		p := &api.InvoiceListParams{Subscription: api.String(sub.ID), Status: api.String(status)}
		p.Context = ctx
		p.Limit = api.Int64(100)
		iter := invoice.List(p)
		for iter.Next() {
			inv := iter.Invoice()
			if (inv.Status != api.InvoiceStatusDraft && inv.Status != api.InvoiceStatusOpen) || !ownedTaxHeldInvoice(sub, inv) {
				continue
			}
			if inv.Status == api.InvoiceStatusOpen && inv.Metadata["frameworks_tax_recovered_hold"] == "true" && inv.Metadata["frameworks_tax_profile"] != "" {
				continue
			}
			if inv.Status == api.InvoiceStatusDraft {
				if lineErr := completeTaxRecoveryInvoiceLines(ctx, inv); lineErr != nil {
					failures = errors.Join(failures, lineErr)
					report.RetryPending = true
					continue
				}
			}
			evidence, fingerprint, inputErr := taxRecoveryInvoiceInputs(inv, sub, report.ProfileSHA)
			if inputErr != nil {
				failures = errors.Join(failures, inputErr)
				report.RetryPending = true
				continue
			}
			if inv.Metadata[taxTerminalCode] != "" && inv.Metadata[taxTerminalInput] == fingerprint {
				continue
			}
			if err := recoverStripeTaxInvoice(ctx, sub, inv, profile, rateID); err != nil {
				var review *billingtax.ReviewError
				if errors.As(err, &review) && !billingtax.IsTransient(err) {
					if !reported[inv.ID] {
						report.Reviews = append(report.Reviews, TaxInvoiceReview{InvoiceID: inv.ID, SubscriptionID: sub.ID, CustomerID: report.CustomerID, Code: review.Code, Message: review.Message, InputFingerprint: fingerprint, Evidence: evidence})
						reported[inv.ID] = true
					}
				} else {
					failures = errors.Join(failures, err)
					report.RetryPending = true
				}
			}
		}
		if err := iter.Err(); err != nil {
			failures = errors.Join(failures, err)
			report.RetryPending = true
		}
	}
	return report, failures
}

func recoverStripeTaxInvoice(ctx context.Context, sub *api.Subscription, inv *api.Invoice, profile TaxProfile, rateID string) error {
	review := func(code, message string) error {
		return billingtax.Review(code, message)
	}
	if !ownedTaxHeldInvoice(sub, inv) {
		return nil
	}
	if inv.Status == api.InvoiceStatusOpen {
		if inv.Metadata["frameworks_tax_recovered_hold"] != "true" || inv.Metadata["frameworks_tax_profile"] == "" {
			return review("stripe_held_invoice_missing_frozen_tax", "Finalized held invoice lacks frozen tax proof; preserve its original money for review")
		}
		return nil
	}
	if inv.BillingReason != api.InvoiceBillingReasonSubscriptionCycle {
		return review("stripe_held_invoice_nonrenewal", "Owned held invoice is not a regular renewal; review its original supply before correction")
	}
	if sub.PauseCollection != nil && (sub.Metadata["frameworks_tax_pause_owned"] != "true" || sub.PauseCollection.Behavior != api.SubscriptionPauseCollectionBehaviorKeepAsDraft || sub.PauseCollection.ResumesAt != 0) {
		return review("stripe_held_invoice_operator_pause", "An operator changed collection policy for this held invoice")
	}
	if inv.Status == api.InvoiceStatusDraft {
		if inv.AutoAdvance {
			if inv.Metadata["frameworks_tax_recovered_hold"] == "true" && inv.Metadata["frameworks_tax_profile"] != "" {
				return nil
			}
			return review("stripe_held_invoice_unproven_collection", "Held draft has automatic collection enabled without retained tax proof")
		}
		if err := completeTaxRecoveryInvoiceLines(ctx, inv); err != nil {
			return err
		}
		lines := inv.Lines
		if lines.HasMore || len(lines.Data) == 0 || len(lines.Data) > 250 || len(inv.Discounts) > 0 {
			return review("stripe_held_invoice_unsupported_lines", "Held invoice has unsupported discounts or an incomplete line set")
		}
		updates := []*api.InvoiceUpdateLinesLineParams{}
		for _, line := range lines.Data {
			if line == nil || line.ID == "" || line.Amount < 0 || line.Currency != inv.Currency || line.Parent == nil || line.Parent.SubscriptionItemDetails == nil || line.Parent.SubscriptionItemDetails.Subscription != sub.ID || line.Parent.SubscriptionItemDetails.Proration || len(line.DiscountAmounts) > 0 || len(line.PretaxCreditAmounts) > 0 {
				return review("stripe_held_invoice_unsupported_line", "Held invoice contains adjustments requiring original-source tax review")
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
		// Stripe's automatic advancement owns collection, dunning and SCA
		// reminders. A declined or processing payment does not invalidate VAT.
		// https://docs.stripe.com/invoicing/integration/automatic-advancement-collection
		finalize := &api.InvoiceFinalizeInvoiceParams{AutoAdvance: api.Bool(true)}
		finalize.Context = ctx
		// The SDK reuses its request key for transport retries. A later sweep
		// rereads this fixed invoice's state and needs a fresh key so an
		// executed 500 cannot pin an unchanged draft to a cached failure.
		var err error
		if inv, err = invoice.FinalizeInvoice(inv.ID, finalize); err != nil {
			return err
		}
	}
	if inv.Status == api.InvoiceStatusOpen {
		if inv.Metadata["frameworks_tax_recovered_hold"] != "true" || inv.Metadata["frameworks_tax_profile"] == "" {
			return review("stripe_held_invoice_missing_frozen_tax", "Finalized held invoice lacks frozen tax proof; preserve its original money for review")
		}
	}
	return nil
}

func ownedTaxHeldInvoice(sub *api.Subscription, inv *api.Invoice) bool {
	if sub == nil || sub.Customer == nil || inv == nil || inv.Customer == nil || inv.Customer.ID != sub.Customer.ID || inv.Parent == nil || inv.Parent.SubscriptionDetails == nil {
		return false
	}
	details := inv.Parent.SubscriptionDetails
	meta := details.Metadata
	started, err := strconv.ParseInt(meta["frameworks_tax_hold_started"], 10, 64)
	return err == nil && started > 0 && inv.Created >= started && meta["frameworks_tax_review"] == "true" && meta["frameworks_tax_pause_owned"] == "true" && meta["tenant_id"] == sub.Metadata["tenant_id"] && details.Subscription != nil && details.Subscription.ID == sub.ID
}

func completeTaxRecoveryInvoiceLines(ctx context.Context, inv *api.Invoice) error {
	if inv.Lines != nil && !inv.Lines.HasMore {
		return nil
	}
	p := &api.InvoiceListLinesParams{Invoice: api.String(inv.ID)}
	p.Context = ctx
	p.Limit = api.Int64(100)
	all := invoice.ListLines(p)
	inv.Lines = &api.InvoiceLineItemList{}
	for all.Next() {
		inv.Lines.Data = append(inv.Lines.Data, all.InvoiceLineItem())
		if len(inv.Lines.Data) > 250 {
			inv.Lines.HasMore = true
			return nil
		}
	}
	return all.Err()
}
