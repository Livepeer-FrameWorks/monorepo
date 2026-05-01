package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	var existingBPS int
	err := exec.QueryRowContext(ctx, `
		SELECT fee_basis_points
		FROM purser.platform_fee_policy
		WHERE cluster_kind = 'third_party_marketplace'
		  AND cluster_owner_tenant_id IS NULL
		  AND pricing_source IS NULL
		  AND effective_to IS NULL
		ORDER BY effective_from DESC
		LIMIT 1
	`).Scan(&existingBPS)
	if err == nil {
		return Result{Noop: []string{"third_party_marketplace:global"}}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, fmt.Errorf("probe platform_fee_policy: %w", err)
	}

	_, err = exec.ExecContext(ctx, `
		INSERT INTO purser.platform_fee_policy (
			cluster_kind, cluster_owner_tenant_id, pricing_source,
			fee_basis_points, notes
		) VALUES (
			'third_party_marketplace', NULL, NULL, $1,
			'default marketplace revenue-share policy'
		)
	`, defaultMarketplaceFeeBPS)
	if err != nil {
		return Result{}, fmt.Errorf("insert platform_fee_policy: %w", err)
	}
	return Result{Created: []string{"third_party_marketplace:global"}}, nil
}
