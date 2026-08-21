package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_billing/internal/database/purserdb"
)

const defaultMarketplaceFeeBPS = 2000

// ReconcileDefaultPlatformFeePolicy ensures third-party marketplace credits do
// not default to a zero platform fee on a fresh install. Existing policy rows
// are left alone so ops can set the actual commercial rate without bootstrap
// fighting them on every run.
func ReconcileDefaultPlatformFeePolicy(ctx context.Context, exec DBTX) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileDefaultPlatformFeePolicy: nil executor")
	}

	queries := purserdb.New(exec)
	_, err := queries.GetDefaultMarketplaceFeeBPS(ctx)
	if err == nil {
		return Result{Noop: []string{"third_party_marketplace:global"}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("probe platform_fee_policy: %w", err)
	}

	if err := queries.InsertDefaultMarketplaceFeePolicy(ctx, defaultMarketplaceFeeBPS); err != nil {
		return Result{}, fmt.Errorf("insert platform_fee_policy: %w", err)
	}
	return Result{Created: []string{"third_party_marketplace:global"}}, nil
}
