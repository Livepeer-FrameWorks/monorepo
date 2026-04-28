package bootstrap

import (
	"context"
	"errors"
	"fmt"
)

// Sections aggregates per-phase reconcile outcomes for the cobra dispatcher.
type Sections struct {
	BillingTierCatalog Result
	ClusterPricing     Result
	CustomerBilling    Result
	// PostCommit is the cross-service work the dispatcher must run after the
	// outer reconcile transaction commits — and skip when --dry-run rolls back.
	// Today this is QM SubscribeToCluster + UpdateTenant(primary_cluster) for
	// every customer in customer_billing, mirroring runtime
	// ensureTierClusterAccess. The dispatcher dispatches per Op.Kind.
	PostCommit []PostCommitOp
}

// Reconcile applies the entire PurserSection in dependency order against the
// caller-supplied executor. The cobra dispatcher opens the outer transaction
// and decides commit (apply) vs rollback (--dry-run); no reconciler in this
// package opens its own tx.
//
// Order:
//
//  1. billing_tier_catalog (embedded ⊕ overlay) — establishes tier rows that
//     customer_billing references.
//  2. cluster_pricing.
//  3. customer_billing — resolves tenant alias → UUID via the supplied
//     QMBootstrapClient (bootstrap calls Quartermaster's ResolveTenantAliases gRPC).
//     Emits PostCommitOps for cross-service entitlement work.
func Reconcile(ctx context.Context, exec DBTX, desired PurserSection, embedded []CatalogTier, resolver QMBootstrapClient) (*Sections, error) {
	if exec == nil {
		return nil, errors.New("Reconcile: nil executor")
	}
	out := &Sections{}

	tiers, err := MergeBillingTierOverlay(embedded, desired.BillingTiers)
	if err != nil {
		return out, fmt.Errorf("billing_tier_catalog overlay: %w", err)
	}
	tierRes, err := ReconcileBillingTierCatalog(ctx, exec, tiers)
	out.BillingTierCatalog = tierRes
	if err != nil {
		return out, fmt.Errorf("billing_tier_catalog: %w", err)
	}

	pricingRes, err := ReconcileClusterPricing(ctx, exec, desired.ClusterPricing)
	out.ClusterPricing = pricingRes
	if err != nil {
		return out, fmt.Errorf("cluster_pricing: %w", err)
	}

	if len(desired.CustomerBilling) > 0 {
		if resolver == nil {
			return out, errors.New("customer_billing: declared but no QMBootstrapClient supplied")
		}
		cbRes, post, err := ReconcileCustomerBilling(ctx, exec, desired.CustomerBilling, resolver)
		out.CustomerBilling = cbRes
		out.PostCommit = post
		if err != nil {
			return out, fmt.Errorf("customer_billing: %w", err)
		}
	}
	return out, nil
}
