package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
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
	taxReady := false
	var reviewReasons []string
	if lookupErr == nil {
		assessment = existing.TaxAssessment
		retained, retainedErr := queries.GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: existing.ID, DocumentKind: kind})
		if retainedErr != nil {
			return retainedErr
		}
		taxReady = retained.TaxStatus == "ready"
	} else if kind == "provider_invoice" {
		assessment, reviewReasons, err = s.assessImportedStripeInvoice(ctx, tenant, doc)
		if err != nil {
			return err
		}
		taxReady = len(reviewReasons) == 0
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
		retained, retainedErr := queries.GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: parent.ID, DocumentKind: "provider_invoice"})
		if retainedErr != nil {
			return retainedErr
		}
		taxReady = retained.TaxStatus == "ready"
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
		if taxReady {
			taxStatus = "ready"
		}
	}
	store := purserdb.StoreProviderBillingDocumentParams{
		TenantID: tenant, ProviderAccountID: account, Livemode: doc.Livemode, DocumentKind: kind, ProviderDocumentID: doc.ID, OriginalNumber: doc.Number,
		SourceInvoiceID: sql.NullString{String: doc.SourceInvoiceID, Valid: doc.SourceInvoiceID != ""}, Status: doc.Status, Currency: doc.Currency,
		NetMinor: net, VatMinor: vat, GrossMinor: doc.Gross, TaxStatus: taxStatus, TaxGroups: doc.TaxGroups, SupplierSnapshot: doc.Supplier, CustomerSnapshot: doc.Customer, RawFacts: doc.Raw, TaxAssessment: assessment,
		OriginalPdfUrl: sql.NullString{String: doc.PDFURL, Valid: doc.PDFURL != ""}, OriginalPdf: pdf, PdfSha256: sql.NullString{String: sha, Valid: sha != ""}, IssuedAt: doc.IssuedAt,
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		stored, writeErr := q.StoreProviderBillingDocument(ctx, store)
		if writeErr != nil {
			return writeErr
		}
		if len(reviewReasons) == 0 {
			return nil
		}
		retained, readErr := q.GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: stored.ID, DocumentKind: kind})
		if readErr != nil || retained.TaxStatus == "ready" {
			return readErr
		}
		return q.RecordCryptoAccountingAnomaly(ctx, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: tenant, Kind: "provider_tax_document_review", Network: "stripe", ReferenceType: kind, ReferenceID: stored.ID, AmountCents: 0, Detail: "Original Stripe invoice has unverified tax setup; review the retained document and billing profile", EvidenceJson: retained.TaxAssessment})
	})
	if err != nil {
		return err
	}
	return pdfErr
}

func (s *Service) assessImportedStripeInvoice(ctx context.Context, tenant string, doc billingstripe.ProviderTaxDocument) (json.RawMessage, []string, error) {
	retained := json.RawMessage(`{}`)
	var reasons []string
	if doc.ProfileSHA == "" {
		reasons = append(reasons, "provider_tax_sync_missing")
	} else {
		var err error
		retained, err = purserdb.New(s.db).GetProviderTaxProfile(ctx, purserdb.GetProviderTaxProfileParams{TenantID: tenant, ProfileSha256: doc.ProfileSHA})
		if errors.Is(err, sql.ErrNoRows) {
			retained = json.RawMessage(`{}`)
			reasons = append(reasons, "provider_tax_profile_missing")
		} else if err != nil {
			return nil, nil, err
		}
	}
	var profile billingstripe.TaxProfile
	if len(reasons) == 0 {
		if err := json.Unmarshal(retained, &profile); err != nil {
			reasons = append(reasons, "provider_tax_profile_invalid")
		} else if err = billingstripe.ValidateProviderTaxTerms(doc, profile); err != nil {
			reasons = append(reasons, "provider_tax_terms_mismatch")
		}
	}
	if len(reasons) == 0 {
		fresh, err := AssessStripeTaxProfile(ctx, s.db, tenant, "")
		if err != nil {
			var review *billingtax.ReviewError
			if !errors.As(err, &review) || review.Code == "vat_validation_unavailable" {
				return nil, nil, fmt.Errorf("defer Stripe invoice tax verification: %w", err)
			}
			reasons = append(reasons, "current_tax_assessment_required")
		} else if fresh.Fingerprint() != profile.Fingerprint() {
			reasons = append(reasons, "provider_tax_profile_changed")
		} else {
			fresh.ProfileSHA = doc.ProfileSHA
			retained, err = json.Marshal(fresh)
			if err != nil {
				return nil, nil, err
			}
		}
	}
	if len(reasons) != 0 {
		var review map[string]json.RawMessage
		if err := json.Unmarshal(retained, &review); err != nil {
			review = map[string]json.RawMessage{"retained_profile": retained}
		}
		if review == nil {
			review = make(map[string]json.RawMessage)
		}
		review["review_required"] = json.RawMessage(`true`)
		encoded, err := json.Marshal(reasons)
		if err != nil {
			return nil, nil, err
		}
		review["review_reasons"] = encoded
		retained, err = json.Marshal(review)
		if err != nil {
			return nil, nil, err
		}
	}
	return retained, reasons, nil
}
