package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func retainNativeMollieTaxReviewTx(ctx context.Context, tx *sql.Tx, tenant, invoice, payment, sha string, profile billingstripe.TaxProfile, proof CryptoTaxAssessment, frozen json.RawMessage, netInput string, net sql.NullInt64, gross int64, acceptedGross sql.NullInt64, at time.Time, reason string) error {
	var expected any
	if acceptedGross.Valid {
		expected = acceptedGross.Int64
	}
	var acceptedNet any
	if net.Valid {
		acceptedNet = net.Int64
	}
	decision, err := json.Marshal(map[string]any{"review_required": true, "code": reason, "review_reasons": []string{reason}, "accepted_profile_sha256": sha, "accepted_provider_profile": frozen, "provider_tax_assessment": profile.Evidence, "accepted_net_input": netInput, "accepted_net_minor": acceptedNet, "expected_gross_minor": expected, "confirmed_payment": map[string]any{"id": payment, "gross_minor": gross, "currency": "EUR", "paid_at": at}})
	if err != nil {
		return err
	}
	supplier, err := json.Marshal(proof.Supplier)
	if err != nil {
		return err
	}
	address, err := json.Marshal(map[string]string{"street": profile.Street, "city": profile.City, "postal_code": profile.PostalCode, "country": profile.Country})
	if err != nil {
		return err
	}
	customer, err := json.Marshal(map[string]any{"name": profile.Name, "company": profile.Company, "email": profile.Email, "address": string(address), "vat_number": profile.VATNumber, "customer_type": proof.CustomerType})
	if err != nil {
		return err
	}
	q := purserdb.New(tx)
	// Cash receipt is known even when the accepted evidence cannot establish a
	// lawful tax split. Partial original parties stay partial; no current seller
	// configuration or guessed VAT is substituted into this reviewed record.
	rows, err := q.FreezeInvoiceTax(ctx, purserdb.FreezeInvoiceTaxParams{TenantID: tenant, InvoiceID: invoice, TaxStatus: "review", DocumentGrossMinor: sql.NullInt64{Int64: gross, Valid: true}, TaxPointAt: sql.NullTime{Time: at, Valid: true}, TaxDecision: decision, TaxSummary: json.RawMessage(`[]`), CustomerSnapshot: customer, SupplierSnapshot: supplier})
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("retain Mollie tax review: invoice missing or tax facts already finalized")
	}
	return q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: tenant, Kind: "provider_tax_document_review", Network: "mollie", ReferenceType: "invoice", ReferenceID: invoice, AmountCents: gross, Detail: "Confirmed Mollie payment requires review of its retained tax evidence", EvidenceJson: decision})
}
