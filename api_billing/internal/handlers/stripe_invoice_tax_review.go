package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func (jm *JobManager) recordStripeInvoiceTaxReview(ctx context.Context, tenant string, review billingstripe.TaxInvoiceReview) error {
	evidence, err := json.Marshal(struct {
		Code             string          `json:"code"`
		InputFingerprint string          `json:"input_fingerprint"`
		Facts            json.RawMessage `json:"facts"`
	}{review.Code, review.InputFingerprint, review.Evidence})
	if err != nil {
		return err
	}
	q := purserdb.New(jm.db)
	if err = q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_invoice_tax_review", Network: "stripe", ReferenceType: "stripe_invoice", ReferenceID: review.InvoiceID, Detail: review.Message, EvidenceJson: evidence}); err != nil {
		return err
	}
	retained, err := q.HasBillingTaxReviewReceipt(ctx, purserdb.HasBillingTaxReviewReceiptParams{TenantID: tenant, Kind: "provider_invoice_tax_review", ReferenceType: "stripe_invoice", ReferenceID: review.InvoiceID, EvidenceJson: evidence})
	if err != nil {
		return err
	}
	if !retained {
		return fmt.Errorf("invoice %s tax review has no tenant-scoped durable receipt", review.InvoiceID)
	}
	return nil
}
