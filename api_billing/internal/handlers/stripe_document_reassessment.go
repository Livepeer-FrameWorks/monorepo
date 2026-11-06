package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"frameworks/api_billing/internal/database/purserdb"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const stripeTaxReviewBatchSize = 20

// Reassessment refines an internal verification result. Original issued facts,
// captured assessments, money and PDFs remain protected by their existing guard.
func (s *Service) reassessStripeTaxDocuments(ctx context.Context, tenant string) error {
	q := purserdb.New(s.db)
	work, err := q.ListProviderDocumentsForTaxReview(ctx, purserdb.ListProviderDocumentsForTaxReviewParams{TenantID: tenant, PageSize: stripeTaxReviewBatchSize})
	if err != nil {
		return err
	}
	current := sync.OnceValues(func() (*billingstripe.TaxProfile, error) {
		return AssessStripeTaxProfile(ctx, s.db, tenant, "")
	})
	var failures error
	for _, item := range work {
		if err = ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if err = s.reassessStripeTaxDocument(ctx, tenant, item.ID, item.DocumentKind, current); err != nil {
			// Failed attempts also move behind older work, so one unavailable
			// assessment cannot starve other retained documents in this tenant.
			stampErr := q.StampProviderDocumentTaxReviewAttempt(ctx, purserdb.StampProviderDocumentTaxReviewAttemptParams{TenantID: tenant, DocumentID: item.ID})
			failures = errors.Join(failures, fmt.Errorf("reassess provider document %s: %w", item.ID, err), stampErr)
		}
	}
	return failures
}

func (s *Service) reassessStripeTaxDocument(ctx context.Context, tenant, id, kind string, current func() (*billingstripe.TaxProfile, error)) error {
	q := purserdb.New(s.db)
	retained, err := q.GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: id, DocumentKind: kind})
	if err != nil {
		return err
	}
	if retained.TaxStatus == "ready" {
		return nil
	}
	original, err := billingstripe.ParseProviderTaxDocument(kind, retained.RawFacts)
	if err != nil {
		return err
	}
	var assessment json.RawMessage
	var reasons []string
	if kind == "provider_invoice" {
		assessment, reasons, err = s.assessImportedStripeInvoiceWithCurrent(ctx, tenant, original, current)
	} else {
		assessment = json.RawMessage(`{}`)
		parent, parentErr := q.FindProviderBillingDocument(ctx, purserdb.FindProviderBillingDocumentParams{TenantID: tenant, ProviderDocumentID: original.SourceInvoiceID, DocumentKind: "provider_invoice"})
		if errors.Is(parentErr, sql.ErrNoRows) {
			reasons = append(reasons, "source_invoice_missing")
		} else if parentErr != nil {
			return parentErr
		} else {
			source, readErr := q.GetProviderBillingDocument(ctx, purserdb.GetProviderBillingDocumentParams{TenantID: tenant, DocumentID: parent.ID, DocumentKind: "provider_invoice"})
			if readErr != nil {
				return readErr
			}
			assessment = source.TaxAssessment
			if source.TaxStatus != "ready" {
				reasons = append(reasons, "source_invoice_tax_review")
			}
		}
	}
	if err != nil {
		return err
	}
	status := "ready"
	if !retainedProviderTaxFactsMatch(original, retained) {
		reasons = append(reasons, "captured_tax_facts_incomplete_or_mismatched")
	}
	if len(reasons) > 0 {
		status = "legacy_unknown"
		assessment, err = stripeTaxAssessmentReview(assessment, reasons)
		if err != nil {
			return err
		}
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		if _, updateErr := queries.UpdateProviderDocumentTaxReview(ctx, purserdb.UpdateProviderDocumentTaxReviewParams{TenantID: tenant, DocumentID: id, TaxStatus: status, TaxAssessment: assessment}); updateErr != nil {
			return updateErr
		}
		return retainProviderDocumentTaxReview(ctx, queries, tenant, id, kind)
	})
}
