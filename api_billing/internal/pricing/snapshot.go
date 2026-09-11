package pricing

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

// Matches the placement authority/discovery cluster bound; pricing must not
// silently narrow the entitled census to a smaller set of commercial candidates.
const maxPlacementTariffClusters = 4096

// ClusterOwnershipSnapshot is a detached, validated Quartermaster observation.
// It conveys ownership, not entitlement, consent or a lease on cluster identity.
// Callers must bound its lifetime and revalidate access before publishing quotes.
type ClusterOwnershipSnapshot struct {
	clusterID string
	owner     ownership
}

// CaptureClusterOwnership performs the service call before a pricing transaction
// is opened. The snapshot contains no client capable of doing network work later.
func CaptureClusterOwnership(ctx context.Context, qm QuartermasterClient, clusterID string) (*ClusterOwnershipSnapshot, error) {
	if qm == nil || !placementPriceIdentifier(clusterID, 100) {
		return nil, fmt.Errorf("pricing ownership requires client and cluster")
	}
	owner, err := loadOwnership(ctx, qm, clusterID)
	if err != nil {
		return nil, err
	}
	return &ClusterOwnershipSnapshot{clusterID: clusterID, owner: owner}, nil
}

// PlacementTariffSnapshot is pricing evidence, not a placement authorization or
// a quote. It contains no assumed allowance consumption or access permission.
type PlacementTariffSnapshot struct {
	TenantID          string
	AsOf              time.Time
	Tier              *billing.EffectiveTier
	Clusters          map[string]*ClusterPricing
	Subscription      PlacementSubscriptionContext
	NextPricingChange map[string]time.Time
	AllowanceUsage    PlacementAllowanceUsage
}

type PlacementSubscriptionContext struct {
	PeriodStart        *time.Time
	PeriodEnd          *time.Time
	PendingTierID      *uuid.UUID
	PendingEffectiveAt *time.Time
}

// ReadPlacementTariffs resolves one tenant's tier and cluster tariffs in a single
// read-only repeatable-read transaction. Ownership observations must already be
// captured; entitlement and freshness checks surround this read at the caller.
// No partial snapshot is returned when any read or transaction completion fails.
func ReadPlacementTariffs(ctx context.Context, db *sql.DB, tenantID string, owners []*ClusterOwnershipSnapshot) (*PlacementTariffSnapshot, error) {
	tenant, err := uuid.Parse(tenantID)
	if db == nil || err != nil || tenant == uuid.Nil || tenant.String() != tenantID || len(owners) == 0 || len(owners) > maxPlacementTariffClusters {
		return nil, fmt.Errorf("invalid placement tariff snapshot input")
	}
	seen := make(map[string]bool, len(owners))
	clusterIDs := make([]string, 0, len(owners))
	for _, owner := range owners {
		if owner == nil || owner.clusterID == "" || seen[owner.clusterID] {
			return nil, fmt.Errorf("invalid or duplicate placement cluster ownership")
		}
		seen[owner.clusterID] = true
		clusterIDs = append(clusterIDs, owner.clusterID)
	}
	var out *PlacementTariffSnapshot
	err = database.WithRetryablePostgresTx(ctx, db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *sql.Tx) error {
		out = nil
		asOf, clockErr := purserdb.New(tx).ReadPlacementSnapshotTime(ctx)
		if clockErr != nil {
			return clockErr
		}
		tier, readErr := billing.LoadEffectiveTierTx(ctx, tx, tenantID)
		if readErr != nil {
			return readErr
		}
		contextRow, contextErr := purserdb.New(tx).ReadPlacementSubscriptionContext(ctx, purserdb.ReadPlacementSubscriptionContextParams{TenantID: tenantID, SubscriptionID: tier.SubscriptionID})
		if contextErr != nil {
			return contextErr
		}
		snapshot := &PlacementTariffSnapshot{TenantID: tenantID, AsOf: asOf, Tier: tier, Clusters: make(map[string]*ClusterPricing, len(owners)), NextPricingChange: make(map[string]time.Time)}
		if contextRow.PeriodStart.Valid {
			snapshot.Subscription.PeriodStart = &contextRow.PeriodStart.Time
		}
		if contextRow.PeriodEnd.Valid {
			snapshot.Subscription.PeriodEnd = &contextRow.PeriodEnd.Time
		}
		if contextRow.PendingTierID.Valid {
			snapshot.Subscription.PendingTierID = &contextRow.PendingTierID.UUID
		}
		if contextRow.PendingEffectiveAt.Valid {
			snapshot.Subscription.PendingEffectiveAt = &contextRow.PendingEffectiveAt.Time
		}
		for _, owner := range owners {
			resolved, resolveErr := ResolveClusterPricingTx(ctx, tx, ClusterPricingSnapshotInput{
				Ownership: owner, ConsumingTenantID: tenantID, AsOf: asOf, TierRules: tier.Rules, TierCurrency: tier.Currency,
			})
			if resolveErr != nil {
				return resolveErr
			}
			snapshot.Clusters[owner.clusterID] = resolved
		}
		boundaries, boundaryErr := purserdb.New(tx).ListPlacementPricingBoundaries(ctx, purserdb.ListPlacementPricingBoundariesParams{ClusterIds: clusterIDs, ObservedAt: asOf})
		if boundaryErr != nil {
			return boundaryErr
		}
		for _, boundary := range boundaries {
			if !seen[boundary.ClusterID] || !boundary.ValidUntil.After(asOf) {
				return fmt.Errorf("invalid placement pricing boundary")
			}
			if _, duplicate := snapshot.NextPricingChange[boundary.ClusterID]; duplicate {
				return fmt.Errorf("duplicate placement pricing boundary")
			}
			snapshot.NextPricingChange[boundary.ClusterID] = boundary.ValidUntil
		}
		usage, usageErr := readPlacementAllowanceUsage(ctx, tx, snapshot, clusterIDs)
		if usageErr != nil {
			return usageErr
		}
		snapshot.AllowanceUsage = usage
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		out = snapshot
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AllowancePeriod returns only an explicitly observed current billing period.
// Missing, reversed, future or expired dates cannot establish unused allowances.
func (snapshot *PlacementTariffSnapshot) AllowancePeriod() (time.Time, time.Time, bool) {
	if snapshot == nil || snapshot.AsOf.IsZero() {
		return time.Time{}, time.Time{}, false
	}
	period := snapshot.Subscription
	if period.PeriodStart == nil || period.PeriodEnd == nil || period.PeriodStart.IsZero() || period.PeriodStart.After(snapshot.AsOf) || !period.PeriodEnd.After(snapshot.AsOf) {
		return time.Time{}, time.Time{}, false
	}
	return *period.PeriodStart, *period.PeriodEnd, true
}

// QuoteExpiry intersects the short tariff lease with independently validated
// access expiry and observed pricing/reset boundaries. It grants no access and
// cannot make a stale or unpriced snapshot eligible for placement by itself.
func (snapshot *PlacementTariffSnapshot) QuoteExpiry(clusterID string, now, accessUntil time.Time) (time.Time, error) {
	if snapshot == nil || snapshot.AsOf.IsZero() || now.IsZero() || snapshot.Clusters[clusterID] == nil || !accessUntil.After(now) {
		return time.Time{}, ErrPlacementQuoteUnavailable
	}
	expiry := snapshot.AsOf.Add(time.Minute)
	capAt := func(bound time.Time) {
		if bound.Before(expiry) {
			expiry = bound
		}
	}
	capAt(accessUntil)
	if bound, known := snapshot.NextPricingChange[clusterID]; known {
		capAt(bound)
	}
	if _, end, current := snapshot.AllowancePeriod(); current {
		capAt(end)
	}
	pending := snapshot.Subscription
	if (pending.PendingTierID == nil) != (pending.PendingEffectiveAt == nil) {
		return time.Time{}, ErrPlacementQuoteUnavailable
	}
	if pending.PendingTierID != nil {
		if *pending.PendingTierID == uuid.Nil {
			return time.Time{}, ErrPlacementQuoteUnavailable
		}
		capAt(*pending.PendingEffectiveAt)
	}
	if !expiry.After(now) || !expiry.After(snapshot.AsOf) {
		return time.Time{}, ErrPlacementQuoteUnavailable
	}
	return expiry, nil
}

// ClusterPricingSnapshotInput uses the ownership snapshot's exact cluster ID.
// Tier rules and currency must be loaded in the same transaction as resolution.
type ClusterPricingSnapshotInput struct {
	Ownership         *ClusterOwnershipSnapshot
	ConsumingTenantID string
	AsOf              time.Time
	TierRules         []rating.Rule
	TierCurrency      string
}

// ResolveClusterPricingTx reads history only through the supplied transaction.
// The caller owns its isolation, deadline and completion; this function neither
// contacts Quartermaster nor starts a second database transaction.
func ResolveClusterPricingTx(ctx context.Context, tx *sql.Tx, in ClusterPricingSnapshotInput) (*ClusterPricing, error) {
	if tx == nil || in.Ownership == nil || in.Ownership.clusterID == "" || in.ConsumingTenantID == "" || in.AsOf.IsZero() {
		return nil, fmt.Errorf("pricing snapshot requires transaction, ownership, tenant and time")
	}
	return resolveClusterPricing(ctx, tx, ResolveInputs{
		ConsumingTenantID: in.ConsumingTenantID, ClusterID: in.Ownership.clusterID,
		AsOf: in.AsOf, TierRules: in.TierRules, TierCurrency: in.TierCurrency,
	}, in.Ownership.owner)
}
