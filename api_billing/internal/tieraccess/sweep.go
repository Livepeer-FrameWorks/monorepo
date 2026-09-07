package tieraccess

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	sweepPageSize              = 200
	handoffRetryAttempts       = 3
	handoffRetryInitialBackoff = 100 * time.Millisecond
)

// SweepDeploymentTiers converges quartermaster.tenants.deployment_tier with
// each tenant's effective billing tier (tenant_subscriptions.tier_id →
// billing_tiers.tier_name). It is the durable backstop behind Reconcile's
// in-band stamp: a crash between a tier flip and the stamp, a Quartermaster
// outage, or rows that predate Purser owning the column (e.g. ” from old
// signups, 'global' from old bootstrap runs) all converge here. Staged
// pending_tier_id values are intentionally ignored — they are not effective
// until the downgrade applier flips them. A tenant without a subscription is
// an authoritative fail-closed state: unverified, abandoned, system, and
// deactivated tenants must not retain legacy DNS grants. Returns the number of
// tenants repaired. Per-tenant failures do not stop later tenants from
// converging, but any failed or surplus subscription withholds the handoff.
func (r *Reconciler) SweepDeploymentTiers(ctx context.Context) (repaired int, err error) {
	defer func() {
		if err != nil {
			r.recordFailure("sweep")
		}
	}()
	if r.qm == nil {
		return 0, nil
	}

	observedAt := time.Now().UTC()
	rows, err := purserdb.New(r.db).ListSubscriptionTierNames(ctx)
	if err != nil {
		return 0, fmt.Errorf("query subscription tiers: %w", err)
	}
	tierByTenant := make(map[string]purserdb.ListSubscriptionTierNamesRow, len(rows))
	for _, row := range rows {
		tierByTenant[row.TenantID.String()] = row
	}

	var after *string
	failed := 0
	matchedSubscriptions := int64(0)
	visited := int64(0)
	var firstFailure error
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
			visited++
			desired, hasSub := tierByTenant[tenant.GetId()]
			if hasSub {
				matchedSubscriptions++
			} else {
				desired.TierName = "free"
			}
			if !tenant.GetIsActive() {
				desired.CustomSubdomainEnabled = false
				desired.CustomDomainEnabled = false
			}
			if tenant.GetBillingEntitlementsObserved() &&
				tenant.GetDeploymentTier() == desired.TierName &&
				tenant.GetCustomSubdomainEnabled() == desired.CustomSubdomainEnabled &&
				tenant.GetCustomDomainEnabled() == desired.CustomDomainEnabled {
				continue
			}
			applied, updErr := r.qm.ApplyTenantBillingEntitlements(ctx, &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
				TenantId:               tenant.GetId(),
				DeploymentTier:         desired.TierName,
				CustomSubdomainEnabled: desired.CustomSubdomainEnabled,
				CustomDomainEnabled:    desired.CustomDomainEnabled,
				ObservedAt:             timestamppb.New(observedAt),
			})
			if updErr != nil {
				r.recordFailure("sweep_item")
				r.logger.WithError(updErr).WithField("tenant_id", tenant.GetId()).Warn("deployment-tier sweep: stamp failed")
				failed++
				if firstFailure == nil {
					firstFailure = updErr
				}
				continue
			}
			if !applied.GetApplied() {
				// Quartermaster locks and rechecks this row before applying. A false
				// result therefore means a newer observation won; reloading the same
				// row cannot strengthen that transactional guarantee.
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
	if failed != 0 {
		return repaired, fmt.Errorf("materialize DNS entitlements for %d tenants (first failure: %w)", failed, firstFailure)
	}
	if int64(len(rows)) != matchedSubscriptions {
		return repaired, fmt.Errorf("DNS entitlement handoff census mismatch: subscriptions=%d matched_subscriptions=%d tenants=%d", len(rows), matchedSubscriptions, visited)
	}
	handoff, handoffErr := r.completeTenantDNSEntitlementHandoff(ctx, visited)
	if handoffErr != nil {
		return repaired, fmt.Errorf("complete DNS entitlement handoff: %w", handoffErr)
	}
	if handoff == nil || !handoff.GetRecorded() {
		return repaired, fmt.Errorf("complete DNS entitlement handoff: Quartermaster did not confirm the durable receipt")
	}
	return repaired, nil
}

func (r *Reconciler) completeTenantDNSEntitlementHandoff(ctx context.Context, observedTenantCount int64) (*quartermasterpb.CompleteTenantDNSEntitlementHandoffResponse, error) {
	backoff := handoffRetryInitialBackoff
	for attempt := 1; ; attempt++ {
		handoff, err := r.qm.CompleteTenantDNSEntitlementHandoff(ctx, observedTenantCount)
		if status.Code(err) != codes.Aborted || attempt >= handoffRetryAttempts {
			return handoff, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
	}
}
