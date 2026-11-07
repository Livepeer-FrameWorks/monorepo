package stripe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	api "github.com/stripe/stripe-go/v85"
	"github.com/stripe/stripe-go/v85/invoice"
	"github.com/stripe/stripe-go/v85/subscription"
)

const taxTerminalCode = "frameworks_tax_terminal_review"
const taxTerminalInput = "frameworks_tax_terminal_input"
const taxRecoveryContext = "frameworks_tax_recovery_context"

type TaxInvoiceReview struct {
	InvoiceID        string          `json:"invoice_id"`
	SubscriptionID   string          `json:"subscription_id"`
	CustomerID       string          `json:"customer_id"`
	Code             string          `json:"code"`
	Message          string          `json:"message"`
	InputFingerprint string          `json:"input_fingerprint"`
	Evidence         json.RawMessage `json:"evidence"`
}

type TaxRecoveryReport struct {
	TenantID        string             `json:"tenant_id"`
	SubscriptionID  string             `json:"subscription_id"`
	CustomerID      string             `json:"customer_id"`
	ProfileSHA      string             `json:"profile_sha"`
	ContextSHA      string             `json:"context_sha"`
	RecoveryPending bool               `json:"recovery_pending"`
	RetryPending    bool               `json:"retry_pending"`
	Reviews         []TaxInvoiceReview `json:"reviews"`
}

// Only inputs used to classify the invoice enter the fingerprint. Transport
// headers, validation timestamps, expanded mutable objects and our terminal
// markers cannot turn an unchanged operator review into a fresh attempt.
func taxRecoveryInvoiceInputs(inv *api.Invoice, sub *api.Subscription, profileSHA string) (json.RawMessage, string, error) {
	parent := map[string]string{}
	subscriptionID := ""
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil {
		details := inv.Parent.SubscriptionDetails
		if details.Subscription != nil {
			subscriptionID = details.Subscription.ID
		}
		for _, key := range []string{"tenant_id", "frameworks_tax_hold_started", "frameworks_tax_review", "frameworks_tax_pause_owned"} {
			parent[key] = details.Metadata[key]
		}
	}
	customerID := ""
	if inv.Customer != nil {
		customerID = inv.Customer.ID
	}
	lines := []map[string]any{}
	if inv.Lines != nil {
		for _, line := range inv.Lines.Data {
			if line == nil {
				lines = append(lines, nil)
				continue
			}
			itemID, lineSub := "", ""
			proration := false
			if line.Parent != nil && line.Parent.SubscriptionItemDetails != nil {
				itemID = line.Parent.SubscriptionItemDetails.SubscriptionItem
				lineSub = line.Parent.SubscriptionItemDetails.Subscription
				proration = line.Parent.SubscriptionItemDetails.Proration
			}
			var discountAmount, creditAmount int64
			for _, discount := range line.DiscountAmounts {
				if discount != nil {
					discountAmount += discount.Amount
				}
			}
			for _, credit := range line.PretaxCreditAmounts {
				if credit != nil {
					creditAmount += credit.Amount
				}
			}
			taxes := []map[string]any{}
			for _, tax := range line.Taxes {
				if tax != nil {
					rateID := ""
					if tax.TaxRateDetails != nil {
						rateID = tax.TaxRateDetails.TaxRate
					}
					taxes = append(taxes, map[string]any{"rate_id": rateID, "amount": tax.Amount, "taxable_amount": tax.TaxableAmount, "tax_behavior": tax.TaxBehavior})
				}
			}
			lines = append(lines, map[string]any{"id": line.ID, "amount": line.Amount, "currency": line.Currency, "quantity": line.Quantity, "period": line.Period, "subscription_id": lineSub, "item_id": itemID, "proration": proration, "discount_count": len(line.DiscountAmounts), "discount_amount": discountAmount, "pretax_credit_count": len(line.PretaxCreditAmounts), "pretax_credit_amount": creditAmount, "taxes": taxes})
		}
	}
	discounts := []string{}
	for _, discount := range inv.Discounts {
		if discount != nil {
			discounts = append(discounts, discount.ID)
		}
	}
	var context json.RawMessage
	var err error
	if inv.Status == api.InvoiceStatusOpen {
		profileSHA = ""
		context, err = json.Marshal(map[string]string{"subscription_id": sub.ID, "tenant_id": sub.Metadata["tenant_id"], "customer_id": sub.Customer.ID})
	} else {
		context, _, err = taxRecoverySubscriptionInputs(sub, profileSHA)
	}
	if err != nil {
		return nil, "", err
	}
	facts := map[string]any{"invoice_id": inv.ID, "subscription_id": subscriptionID, "customer_id": customerID, "profile_sha": profileSHA, "subscription_context": context, "status": inv.Status, "billing_reason": inv.BillingReason, "currency": inv.Currency, "created": inv.Created, "auto_advance": inv.AutoAdvance, "net": inv.TotalExcludingTax, "total": inv.Total, "captured_hold": parent, "recovered_hold": inv.Metadata["frameworks_tax_recovered_hold"], "recovered_profile": inv.Metadata["frameworks_tax_profile"], "discounts": discounts, "discount_count": len(inv.Discounts), "lines": lines, "lines_has_more": inv.Lines != nil && inv.Lines.HasMore}
	evidence, err := json.Marshal(facts)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(evidence)
	return evidence, hex.EncodeToString(digest[:]), nil
}

func taxRecoverySubscriptionInputs(sub *api.Subscription, profileSHA string) (json.RawMessage, string, error) {
	if sub == nil || sub.Customer == nil {
		return nil, "", errors.New("tax recovery subscription customer required")
	}
	pause := map[string]any{}
	if sub.PauseCollection != nil {
		pause["behavior"], pause["resumes_at"] = sub.PauseCollection.Behavior, sub.PauseCollection.ResumesAt
	}
	facts := map[string]any{"subscription_id": sub.ID, "customer_id": sub.Customer.ID, "tenant_id": sub.Metadata["tenant_id"], "profile_sha": profileSHA, "pause_collection": pause, "pause_owned": sub.Metadata["frameworks_tax_pause_owned"] == "true"}
	evidence, err := json.Marshal(facts)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(evidence)
	return evidence, hex.EncodeToString(digest[:]), nil
}

// AckTaxRecoveryReport is called only after its reviews have been durably
// recorded by the tenant's billing ledger. Provider markers follow that write,
// so a crash cannot hide an invoice from the operator before it is recorded.
func (c *Client) AckTaxRecoveryReport(ctx context.Context, report *TaxRecoveryReport) error {
	if report == nil || !report.RecoveryPending {
		return nil
	}
	if report.TenantID == "" || report.CustomerID == "" || report.SubscriptionID == "" || report.ProfileSHA == "" {
		return errors.New("complete tax recovery report ownership required")
	}
	getSub := &api.SubscriptionParams{}
	getSub.Context = ctx
	sub, err := subscription.Get(report.SubscriptionID, getSub)
	if err != nil {
		return err
	}
	if err = validateTaxRecoveryReportSubscription(report, sub); err != nil {
		return err
	}
	var failures error
	for _, review := range report.Reviews {
		if review.InvoiceID == "" || review.SubscriptionID != report.SubscriptionID || review.CustomerID != report.CustomerID || review.InputFingerprint == "" || review.Code == "" {
			failures = errors.Join(failures, errors.New("tax invoice review owner or input identity missing"))
			continue
		}
		get := &api.InvoiceParams{}
		get.Context = ctx
		current, invoiceErr := invoice.Get(review.InvoiceID, get)
		if invoiceErr != nil {
			failures = errors.Join(failures, invoiceErr)
			continue
		}
		if current.Customer == nil || current.Customer.ID != report.CustomerID || current.Parent == nil || current.Parent.SubscriptionDetails == nil || current.Parent.SubscriptionDetails.Subscription == nil || current.Parent.SubscriptionDetails.Subscription.ID != report.SubscriptionID || current.Parent.SubscriptionDetails.Metadata["tenant_id"] != report.TenantID {
			failures = errors.Join(failures, errors.New("tax invoice review owner changed before acknowledgement"))
			continue
		}
		if current.Status != api.InvoiceStatusDraft && (current.Status != api.InvoiceStatusOpen || review.Code != "stripe_held_invoice_missing_frozen_tax") {
			continue
		}
		if current.Status == api.InvoiceStatusDraft {
			if lineErr := completeTaxRecoveryInvoiceLines(ctx, current); lineErr != nil {
				failures = errors.Join(failures, lineErr)
				continue
			}
		}
		_, fingerprint, inputErr := taxRecoveryInvoiceInputs(current, sub, report.ProfileSHA)
		if inputErr != nil {
			failures = errors.Join(failures, inputErr)
			continue
		}
		if fingerprint != review.InputFingerprint {
			failures = errors.Join(failures, fmt.Errorf("tax invoice %s inputs changed before review acknowledgement", review.InvoiceID))
			continue
		}
		if current.Metadata[taxTerminalCode] == review.Code && current.Metadata[taxTerminalInput] == fingerprint {
			continue
		}
		update := &api.InvoiceParams{Metadata: map[string]string{taxTerminalCode: review.Code, taxTerminalInput: review.InputFingerprint}}
		update.Context = ctx
		_, err = invoice.Update(review.InvoiceID, update)
		failures = errors.Join(failures, err)
	}
	if failures != nil || report.RetryPending {
		return failures
	}
	current := sub
	if len(report.Reviews) > 0 {
		// Invoice acknowledgement has no compare-and-swap with subscription
		// metadata. Recheck after those writes so newer collection work stays
		// pending; the normal no-review path already has a fresh owner check.
		get := &api.SubscriptionParams{}
		get.Context = ctx
		current, err = subscription.Get(report.SubscriptionID, get)
		if err != nil {
			return err
		}
		if err = validateTaxRecoveryReportSubscription(report, current); err != nil {
			return err
		}
	}
	if current.Metadata["frameworks_tax_recovery_pending"] != "true" {
		return nil
	}
	clear := &api.SubscriptionParams{Metadata: map[string]string{"frameworks_tax_recovery_pending": "", taxRecoveryContext: report.ContextSHA}, ProrationBehavior: api.String("none")}
	clear.Context = ctx
	_, err = subscription.Update(report.SubscriptionID, clear)
	return err
}

func validateTaxRecoveryReportSubscription(report *TaxRecoveryReport, sub *api.Subscription) error {
	if sub.Customer == nil || sub.Customer.ID != report.CustomerID || sub.Metadata["tenant_id"] != report.TenantID {
		return errors.New("tax recovery acknowledgement subscription owner mismatch")
	}
	if sub.Metadata["frameworks_tax_review"] == "true" || sub.Metadata["frameworks_tax_hold_started"] != "" {
		return errors.New("a newer active tax hold must be reconciled before clearing pending recovery")
	}
	if sub.Metadata["frameworks_tax_profile"] != report.ProfileSHA {
		return errors.New("subscription tax profile changed before recovery acknowledgement")
	}
	_, contextSHA, err := taxRecoverySubscriptionInputs(sub, report.ProfileSHA)
	if err != nil {
		return err
	}
	if contextSHA != report.ContextSHA {
		return errors.New("subscription collection context changed before recovery acknowledgement")
	}
	return nil
}
