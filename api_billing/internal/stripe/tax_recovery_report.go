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
	RecoveryPending bool               `json:"recovery_pending"`
	RetryPending    bool               `json:"retry_pending"`
	Reviews         []TaxInvoiceReview `json:"reviews"`
}

// Only inputs used to classify the invoice enter the fingerprint. Transport
// headers, validation timestamps, expanded mutable objects and our terminal
// markers cannot turn an unchanged operator review into a fresh attempt.
func taxRecoveryInvoiceInputs(inv *api.Invoice, profileSHA string) (json.RawMessage, string, error) {
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
	facts := map[string]any{"invoice_id": inv.ID, "subscription_id": subscriptionID, "customer_id": customerID, "profile_sha": profileSHA, "status": inv.Status, "billing_reason": inv.BillingReason, "currency": inv.Currency, "created": inv.Created, "auto_advance": inv.AutoAdvance, "net": inv.TotalExcludingTax, "total": inv.Total, "captured_hold": parent, "recovered_hold": inv.Metadata["frameworks_tax_recovered_hold"], "recovered_profile": inv.Metadata["frameworks_tax_profile"], "discounts": discounts, "discount_count": len(inv.Discounts), "lines": lines, "lines_has_more": inv.Lines != nil && inv.Lines.HasMore}
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
	var failures error
	for _, review := range report.Reviews {
		if review.InvoiceID == "" || review.SubscriptionID != report.SubscriptionID || review.CustomerID != report.CustomerID || review.InputFingerprint == "" || review.Code == "" {
			failures = errors.Join(failures, errors.New("tax invoice review owner or input identity missing"))
			continue
		}
		get := &api.InvoiceParams{}
		get.Context = ctx
		current, err := invoice.Get(review.InvoiceID, get)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if current.Customer == nil || current.Customer.ID != report.CustomerID || current.Parent == nil || current.Parent.SubscriptionDetails == nil || current.Parent.SubscriptionDetails.Subscription == nil || current.Parent.SubscriptionDetails.Subscription.ID != report.SubscriptionID || current.Parent.SubscriptionDetails.Metadata["tenant_id"] != report.TenantID {
			failures = errors.Join(failures, errors.New("tax invoice review owner changed before acknowledgement"))
			continue
		}
		if err = completeTaxRecoveryInvoiceLines(ctx, current); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		_, fingerprint, err := taxRecoveryInvoiceInputs(current, report.ProfileSHA)
		if err != nil {
			failures = errors.Join(failures, err)
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
	get := &api.SubscriptionParams{}
	get.Context = ctx
	current, err := subscription.Get(report.SubscriptionID, get)
	if err != nil {
		return err
	}
	if current.Customer == nil || current.Customer.ID != report.CustomerID || current.Metadata["tenant_id"] != report.TenantID {
		return errors.New("tax recovery acknowledgement subscription owner mismatch")
	}
	if current.Metadata["frameworks_tax_review"] == "true" || current.Metadata["frameworks_tax_hold_started"] != "" {
		return errors.New("a newer active tax hold must be reconciled before clearing pending recovery")
	}
	if current.Metadata["frameworks_tax_profile"] != report.ProfileSHA {
		return errors.New("subscription tax profile changed before recovery acknowledgement")
	}
	if current.Metadata["frameworks_tax_recovery_pending"] != "true" {
		return nil
	}
	clear := &api.SubscriptionParams{Metadata: map[string]string{"frameworks_tax_recovery_pending": ""}, ProrationBehavior: api.String("none")}
	clear.Context = ctx
	_, err = subscription.Update(report.SubscriptionID, clear)
	return err
}
