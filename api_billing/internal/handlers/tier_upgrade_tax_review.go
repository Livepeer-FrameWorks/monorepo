package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
)

type tierUpgradeTaxReviewError struct {
	upgrade purserdb.GetTierUpgradeRow
	review  *billingtax.ReviewError
}

func (e *tierUpgradeTaxReviewError) Error() string { return e.review.Error() }
func (e *tierUpgradeTaxReviewError) Unwrap() error { return e.review }

func permanentInvoiceTaxReview(err error) *billingtax.ReviewError {
	var review *billingtax.ReviewError
	if !billingtax.IsTransient(err) && errors.As(err, &review) {
		return review
	}
	return nil
}

func recordTierUpgradeTaxReview(ctx context.Context, q *purserdb.Queries, row purserdb.GetTierUpgradeRow, review *billingtax.ReviewError) error {
	evidence, err := json.Marshal(map[string]any{"review_required": true, "code": review.Code, "message": review.Message, "upgrade_id": row.ID, "requested_at": row.RequestedAt, "old_tier_id": row.OldTierID, "tier_id": row.TierID})
	if err != nil {
		return err
	}
	if err = q.RecordBillingTaxReview(ctx, purserdb.RecordBillingTaxReviewParams{TenantID: row.TenantID, Kind: "invoice_tax_review", Network: "tax", ReferenceType: "tier_upgrade", ReferenceID: row.ID, AmountCents: row.BaseCents, Detail: review.Error(), EvidenceJson: evidence}); err != nil {
		return fmt.Errorf("retain upgrade tax review: %w", err)
	}
	return nil
}
