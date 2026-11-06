package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	billingtax "frameworks/api_billing/internal/tax"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

type billingPhaseTaxReviewError struct {
	*BillingBoundaryCondition
	db         *sql.DB
	tenant     string
	start, end time.Time
	net        int64
	review     billingtax.ReviewError
}

func (e *billingPhaseTaxReviewError) Unwrap() error { return e.BillingBoundaryCondition }

func billingPhaseTaxReviewReference(tenant string, start time.Time) string {
	return tenant + ":" + start.UTC().Format(time.RFC3339Nano)
}

// AfterRollback retains the refusal independently of the arrangement change it
// blocked. The period, rather than the rolled-back invoice ID, identifies retries.
func (e *billingPhaseTaxReviewError) AfterRollback(ctx context.Context) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	evidence, err := json.Marshal(map[string]any{"review_required": true, "code": e.review.Code, "message": e.review.Message, "period_start": e.start, "period_end": e.end})
	if err != nil {
		return err
	}
	err = database.RetryPostgres(cleanup, 0, 0, func() error {
		return purserdb.New(e.db).RecordCryptoAccountingAnomaly(cleanup, purserdb.RecordCryptoAccountingAnomalyParams{TenantID: e.tenant, Kind: "invoice_tax_review", Network: "tax", ReferenceType: "billing_phase", ReferenceID: billingPhaseTaxReviewReference(e.tenant, e.start), AmountCents: e.net, Detail: e.review.Error(), EvidenceJson: evidence})
	})
	if err != nil {
		return fmt.Errorf("retain rejected billing phase tax review: %w", err)
	}
	return nil
}
