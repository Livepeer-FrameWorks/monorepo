package tieraccess

import (
	"context"
	"fmt"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

const sweepPageSize = 200

// SweepDeploymentTiers converges quartermaster.tenants.deployment_tier with
// each tenant's effective billing tier (tenant_subscriptions.tier_id →
// billing_tiers.tier_name). It is the durable backstop behind Reconcile's
// in-band stamp: a crash between a tier flip and the stamp, a Quartermaster
// outage, or rows that predate Purser owning the column (e.g. ” from old
// signups, 'global' from old bootstrap runs) all converge here. Staged
// pending_tier_id values are intentionally ignored — they are not effective
// until the downgrade applier flips them. Tenants without a subscription row
// are skipped: Purser never invents a tier. Returns the number of tenants
// repaired; per-tenant failures are logged and skipped so one bad tenant
// cannot wedge the sweep.
func (r *Reconciler) SweepDeploymentTiers(ctx context.Context) (int, error) {
	if r.qm == nil {
		return 0, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT ts.tenant_id::text, bt.tier_name
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON bt.id = ts.tier_id
	`)
	if err != nil {
		return 0, fmt.Errorf("query subscription tiers: %w", err)
	}
	defer rows.Close()
	tierByTenant := make(map[string]string)
	for rows.Next() {
		var tenantID, tierName string
		if scanErr := rows.Scan(&tenantID, &tierName); scanErr != nil {
			return 0, fmt.Errorf("scan subscription tier: %w", scanErr)
		}
		tierByTenant[tenantID] = tierName
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return 0, fmt.Errorf("iterate subscription tiers: %w", rowsErr)
	}

	repaired := 0
	var after *string
	for {
		page := &commonpb.CursorPaginationRequest{First: sweepPageSize}
		if after != nil {
			page.After = after
		}
		resp, listErr := r.qm.ListTenants(ctx, page)
		if listErr != nil {
			return repaired, fmt.Errorf("list tenants: %w", listErr)
		}
		for _, tenant := range resp.GetTenants() {
			tierName, hasSub := tierByTenant[tenant.GetId()]
			if !hasSub {
				r.logger.WithField("tenant_id", tenant.GetId()).Debug("deployment-tier sweep: no subscription, skipping")
				continue
			}
			if tenant.GetDeploymentTier() == tierName {
				continue
			}
			if _, updErr := r.qm.UpdateTenant(ctx, &quartermasterpb.UpdateTenantRequest{
				TenantId:       tenant.GetId(),
				DeploymentTier: &tierName,
			}); updErr != nil {
				r.logger.WithError(updErr).WithField("tenant_id", tenant.GetId()).Warn("deployment-tier sweep: stamp failed")
				continue
			}
			repaired++
		}
		pagination := resp.GetPagination()
		if pagination == nil || !pagination.GetHasNextPage() || pagination.GetEndCursor() == "" {
			break
		}
		cursor := pagination.GetEndCursor()
		after = &cursor
	}
	return repaired, nil
}
