package handlers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/shopspring/decimal"
)

// PriorFreeAllowanceUsage retains Free consumption across pricing phases of one
// billing period. Finalized PAYG documents do not consume the Free allowance.
func PriorFreeAllowanceUsage(ctx context.Context, db purserdb.DBTX, tenantID string, phaseStart time.Time, meter string) (decimal.Decimal, error) {
	start, err := splitPeriodStart(ctx, db, tenantID, phaseStart)
	if err != nil {
		return decimal.Zero, err
	}
	return priorFreeAllowanceUsage(ctx, db, tenantID, start, phaseStart, meter)
}

func priorFreeAllowanceUsage(ctx context.Context, db purserdb.DBTX, tenantID string, start, phaseStart time.Time, meter string) (decimal.Decimal, error) {
	if !start.Before(phaseStart) {
		return decimal.Zero, nil
	}
	used, err := purserdb.New(db).SumPriorFreeAllowanceUsage(ctx, purserdb.SumPriorFreeAllowanceUsageParams{TenantID: tenantID, PeriodStart: start, PhaseStart: phaseStart, Meter: meter})
	if err != nil {
		return decimal.Zero, err
	}
	quantity, err := decimal.NewFromString(used)
	if err != nil {
		return decimal.Zero, fmt.Errorf("decode prior Free allowance: %w", err)
	}
	return decimal.Max(quantity, decimal.Zero), nil
}
