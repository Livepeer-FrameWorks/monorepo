package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
)

// importStripeTaxDocument keeps the provider's issued facts and original PDF.
// It never creates a Purser supply for the provider-managed subscription fee.
func (s *Service) importStripeTaxDocument(ctx context.Context, kind string, raw json.RawMessage) error {
	if s.stripeClient == nil {
		return nil
	}
	var identity struct {
		Number string `json:"number"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &identity); err != nil {
		return err
	}
	if kind == "provider_invoice" && (identity.Status == "draft" || identity.Number == "") {
		return nil
	}
	doc, err := billingstripe.ParseProviderTaxDocument(kind, raw)
	if err != nil {
		return err
	}
	if doc.CustomerID == "" && doc.SourceInvoiceID != "" {
		parent, fetchErr := s.stripeClient.ProviderDocumentFacts(ctx, "provider_invoice", doc.SourceInvoiceID)
		if fetchErr != nil {
			return fetchErr
		}
		parsed, parseErr := billingstripe.ParseProviderTaxDocument("provider_invoice", parent)
		if parseErr != nil {
			return parseErr
		}
		doc.CustomerID = parsed.CustomerID
	}
	queries := purserdb.New(s.db)
	owners, err := queries.ResolveStripeTaxDocumentOwners(ctx, doc.CustomerID)
	if err != nil {
		return err
	}
	if len(owners) == 0 {
		return nil
	}
	if len(owners) != 1 {
		return fmt.Errorf("ambiguous provider customer owner")
	}
	tenant := owners[0]
	existing, lookupErr := queries.FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: doc.ID, DocumentKind: kind})
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	assessment := json.RawMessage(`{}`)
	if lookupErr == nil {
		assessment = existing.TaxAssessment
	}
	if lookupErr != nil && doc.ProfileSHA != "" {
		assessment, err = queries.GetProviderTaxProfile(ctx, purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: doc.ProfileSHA})
		if err != nil {
			return fmt.Errorf("load retained Stripe tax assessment: %w", err)
		}
	}
	if kind == "provider_credit_note" && doc.SourceInvoiceID != "" {
		parent, parentErr := queries.FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: doc.SourceInvoiceID, DocumentKind: "provider_invoice"})
		if errors.Is(parentErr, sql.ErrNoRows) {
			facts, fetchErr := s.stripeClient.ProviderDocumentFacts(ctx, "provider_invoice", doc.SourceInvoiceID)
			if fetchErr != nil {
				return fetchErr
			}
			if err = s.importStripeTaxDocument(ctx, "provider_invoice", facts); err != nil {
				return err
			}
			parent, parentErr = queries.FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: doc.SourceInvoiceID, DocumentKind: "provider_invoice"})
		}
		if parentErr != nil {
			return parentErr
		}
		doc.Supplier, doc.Customer, assessment = parent.SupplierSnapshot, parent.CustomerSnapshot, parent.TaxAssessment
	}
	var pdf []byte
	var sha string
	var pdfErr error
	if lookupErr != nil || !existing.HasPdf {
		if doc.PDFURL == "" {
			pdfErr = fmt.Errorf("original Stripe PDF not yet available")
		} else {
			pdf, sha, pdfErr = s.stripeClient.DownloadOriginalPDF(ctx, doc.PDFURL)
		}
	}
	account, err := s.stripeClient.ProviderAccountID(ctx)
	if err != nil {
		return err
	}
	taxStatus := "legacy_unknown"
	var net, vat sql.NullInt64
	if doc.Net != nil && doc.VAT != nil {
		net = sql.NullInt64{Int64: *doc.Net, Valid: true}
		vat = sql.NullInt64{Int64: *doc.VAT, Valid: true}
		taxStatus = "ready"
	}
	_, err = queries.StoreProviderBillingDocument(ctx, purserdb.StoreProviderBillingDocumentParams{
		TenantID: tenant, ProviderAccountID: account, Livemode: doc.Livemode, DocumentKind: kind, ProviderDocumentID: doc.ID, OriginalNumber: doc.Number,
		SourceInvoiceID: sql.NullString{String: doc.SourceInvoiceID, Valid: doc.SourceInvoiceID != ""}, Status: doc.Status, Currency: doc.Currency,
		NetMinor: net, VatMinor: vat, GrossMinor: doc.Gross, TaxStatus: taxStatus, TaxGroups: doc.TaxGroups, SupplierSnapshot: doc.Supplier, CustomerSnapshot: doc.Customer, RawFacts: doc.Raw, TaxAssessment: assessment,
		OriginalPdfUrl: sql.NullString{String: doc.PDFURL, Valid: doc.PDFURL != ""}, OriginalPdf: pdf, PdfSha256: sql.NullString{String: sha, Valid: sha != ""}, IssuedAt: doc.IssuedAt,
	})
	if err != nil {
		return err
	}
	return pdfErr
}
