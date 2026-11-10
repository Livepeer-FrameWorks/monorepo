package handlers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

// A billing relationship without tax_assessed_since keeps the untaxed treatment
// it was sold under: its provider customers and subscriptions are not
// synchronized to assessed tax terms, and its recurring Purser invoices carry no
// VAT and no tax facts. The tenant moves to assessed tax handling when it saves a
// complete billing profile, starts a checkout, or changes plan; each of those
// paths requires the complete saved profile that an assessment needs.

func keepsUnassessedTax(ctx context.Context, db purserdb.DBTX, tenant string) (bool, error) {
	keeps, err := purserdb.New(db).TenantKeepsUnassessedTax(ctx, tenant)
	if err != nil {
		return false, fmt.Errorf("read tenant tax handling: %w", err)
	}
	return keeps, nil
}

// StartAssessedTaxHandling moves the tenant's billing relationship to assessed
// tax handling. It is idempotent.
func StartAssessedTaxHandling(ctx context.Context, db purserdb.DBTX, tenant string) error {
	if _, err := purserdb.New(db).StartAssessedTaxHandling(ctx, tenant); err != nil {
		return fmt.Errorf("start assessed tax handling: %w", err)
	}
	return nil
}

// StartAssessedTaxHandlingForSavedProfile starts assessed tax handling when the
// billing profile being saved is complete. An incomplete save changes nothing.
func StartAssessedTaxHandlingForSavedProfile(ctx context.Context, db purserdb.DBTX, tenant string, saved BillingProfile) error {
	if !saved.Complete() {
		return nil
	}
	return StartAssessedTaxHandling(ctx, db, tenant)
}

// unassessedInvoiceTax is the tax position of a recurring invoice under the
// untaxed treatment: the net amount is the amount due and no tax facts are frozen.
func unassessedInvoiceTax(net int64, at time.Time) invoiceTaxAssessment {
	return invoiceTaxAssessment{Net: net, Gross: net, TaxPoint: at, Supplier: ConfiguredDocumentSupplier()}
}
