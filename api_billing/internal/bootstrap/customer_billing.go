package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"
	"frameworks/api_billing/internal/tieraccess"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"

	"github.com/google/uuid"
)

// ReconcileCustomerBilling reconciles every CustomerBilling row into Purser's
// per-tenant billing state, mirroring what
// (*PurserServer).InitializePrepaidAccount and InitializePostpaidAccount do at
// runtime — that is the canonical onboarding path and the one a bootstrapped
// customer must end up in too. The local-DB writes happen inside the caller-
// supplied transaction; the cross-service entitlement work (subscribing the
// tenant to eligible clusters, setting the primary cluster) returns as a list
// of post-commit ops the cobra layer applies after the tx commits, so dry-run
// rolls back cleanly.
//
// Per row this reconciler:
//
//  1. resolves the tenant alias → UUID via Quartermaster gRPC (no schema reads);
//  2. resolves the tier slug → tier UUID + tier_level from purser.billing_tiers;
//  3. upserts purser.tenant_subscriptions (one row per tenant, UNIQUE on
//     tenant_id);
//  4. for `model: prepaid`, ensures a purser.prepaid_balances row exists at
//     balance=0 with the tier currency and a low-balance threshold matching
//     runtime defaults (idempotent via ON CONFLICT DO NOTHING);
//  5. computes eligible clusters as the intersection of (a) Quartermaster's
//     platform-official set, (b) priced clusters in purser.cluster_pricing,
//     (c) tier-qualified by required_tier_level. Emits PostCommitOps the
//     dispatcher uses to call QM BootstrapClusterAccess +
//     UpdateTenant(primary_cluster).
//
// The platform-official boundary is critical: cluster_pricing alone covers
// private customer clusters too (per
// docs/architecture/bootstrap-desired-state.md), so a tenant with
// cluster_access: derived must NOT be auto-subscribed to those.
//
// Stable key: tenant alias. Idempotent: same (model, tier) on a known tenant ⇒
// noop subscription, balance untouched, eligible clusters re-emitted (QM's
// BootstrapClusterAccess handler is itself idempotent).
//
// `cluster_access` on CustomerBilling controls whether step 5 emits ops:
//   - "" or "derived" — emit ops for every eligible cluster (the
//     operator-friendly default; matches runtime behavior).
//   - "none"          — local DB writes only; no QM calls.
//
// Anything else is a configuration error.
func ReconcileCustomerBilling(ctx context.Context, exec DBTX, entries []CustomerBilling, qm QMBootstrapClient, prepared map[string]*PreparedCustomerGrant) (Result, []PostCommitOp, error) {
	if exec == nil {
		return Result{}, nil, errors.New("ReconcileCustomerBilling: nil executor")
	}
	if qm == nil {
		return Result{}, nil, errors.New("ReconcileCustomerBilling: nil QM bootstrap client")
	}
	if len(entries) == 0 {
		return Result{}, nil, nil
	}

	if db, ok := exec.(*sql.DB); ok {
		var result Result
		var post []PostCommitOp
		err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
			var err error
			result, post, err = ReconcileCustomerBilling(ctx, tx, entries, qm, prepared)
			return err
		})
		err = handlers.RecordBillingPhaseTaxReview(ctx, err)
		return result, post, err
	}
	tx, ok := exec.(*sql.Tx)
	if !ok {
		return Result{}, nil, errors.New("customer billing requires a transaction")
	}

	// Cache the platform-official set once per reconcile run. Bounded (a
	// handful of clusters) and the same set applies to every entry.
	var officialIDs map[string]bool
	loadOfficial := func() (map[string]bool, error) {
		if officialIDs != nil {
			return officialIDs, nil
		}
		ids, err := qm.PlatformOfficialClusters(ctx)
		if err != nil {
			return nil, err
		}
		if ids == nil {
			ids = map[string]bool{}
		}
		officialIDs = ids
		return officialIDs, nil
	}

	res := Result{}
	var post []PostCommitOp
	for _, e := range entries {
		if err := validateCustomerBilling(e); err != nil {
			return Result{}, nil, err
		}
		alias, err := aliasFromRef(e.Tenant.Ref)
		if err != nil {
			return Result{}, nil, fmt.Errorf("customer_billing: %w", err)
		}
		boundary := prepared[alias]
		if boundary != nil {
			boundary.applied = false
		}
		if boundary != nil && boundary.deferred != nil {
			res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: boundary.deferred.Error()})
			continue
		}
		tenantID := ""
		if boundary != nil {
			tenantID = boundary.tenantID
		}
		if tenantID == "" {
			tenantID, err = qm.Resolve(ctx, alias)
			if err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
		}
		locked, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if lockErr != nil && !errors.Is(lockErr, sql.ErrNoRows) {
			return Result{}, nil, lockErr
		}
		preserved := lockErr == nil && locked.HasGrant && (boundary == nil || !boundary.checkGrant) && locked.Current.Status != "cancelled"
		if preserved {
			current, currentErr := purserdb.New(tx).GetSubscriptionTierForGrant(ctx, tenantID)
			if currentErr != nil {
				return Result{}, nil, currentErr
			}
			e.Tier, e.Model = current.TierName, current.BillingModel
		}
		tierID, tierLevel, currency, err := resolveTier(ctx, exec, e.Tier)
		if err != nil {
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
		}
		if lockErr == nil {
			var businessErr error
			if locked.AccountClosed {
				businessErr = subscriptions.ErrTenantBillingClosed
			} else if locked.Current.FreeFallbackProviderStopping {
				businessErr = subscriptions.ErrPendingFreeFallback
			} else if locked.Current.PendingReason.String == "tier_upgrade" {
				businessErr = subscriptions.ErrPendingTierUpgrade
			} else if pendingErr := subscriptions.CheckPendingTierUpgrade(ctx, tx, tenantID); pendingErr != nil {
				if !errors.Is(pendingErr, subscriptions.ErrPendingTierUpgrade) {
					return Result{}, nil, pendingErr
				}
				businessErr = pendingErr
			} else if locked.Current.Status == "cancelled" {
				businessErr = fmt.Errorf("tenant %s subscription is cancelled: revoke its outstanding grant before starting provider checkout", tenantID)
			} else if locked.Current.BillingModel != e.Model {
				businessErr = fmt.Errorf("tenant %s runs %s billing; desired state declares %s: use frameworks admin billing set-tier to close its current period first", tenantID, locked.Current.BillingModel, e.Model)
			}
			if businessErr != nil {
				res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: businessErr.Error()})
				continue
			}
		}
		if boundary != nil && boundary.checkGrant && e.OperatorGrant != nil {
			if checkoutErr := subscriptions.CheckGrantCheckout(ctx, tx, tenantID); checkoutErr != nil {
				if !errors.Is(checkoutErr, subscriptions.ErrPendingCheckout) && !subscriptions.IsPricingRefusal(checkoutErr) {
					return Result{}, nil, checkoutErr
				}
				res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: checkoutErr.Error()})
				continue
			}
		}

		if boundary != nil {
			if boundaryErr := boundary.CheckUnchanged(ctx, exec, tenantID); boundaryErr != nil {
				if !errors.Is(boundaryErr, errCustomerGrantChanged) {
					return Result{}, nil, boundaryErr
				}
				res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: boundaryErr.Error()})
				continue
			}
			if phaseErr := commitCustomerGrantPhases(ctx, exec, boundary); phaseErr != nil {
				if !errors.Is(phaseErr, handlers.ErrPrepaidPhaseChanged) && !errors.Is(phaseErr, handlers.ErrPostpaidPhaseChanged) {
					return Result{}, nil, phaseErr
				}
				res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: phaseErr.Error()})
				continue
			}
		}
		grantChanged := false
		grantBeforeTier := boundary != nil && (boundary.close != nil || boundary.prepaid != nil)
		if grantBeforeTier {
			// The history trigger reads ts.tier_id, so archive the grant before
			// changing the subscription to the newly declared tier.
			grantChanged, err = reconcileCustomerGrant(ctx, exec, tenantID, tierID, e, boundary)
			if err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
		}
		subAction, err := upsertTenantSubscription(ctx, exec, tenantID, tierID, e)
		if err != nil {
			var deferred *customerDeclarationDeferred
			if !errors.As(err, &deferred) {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
			res.Deferred = append(res.Deferred, DeferredCustomer{Alias: alias, Reason: err.Error()})
			continue
		}
		if !grantBeforeTier && !preserved {
			grantChanged, err = reconcileCustomerGrant(ctx, exec, tenantID, tierID, e, boundary)
			if err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
		}
		if grantChanged && subAction == "noop" {
			subAction = "updated"
		}
		if err := reconcileEntitlementOverrides(ctx, exec, tenantID, e.EntitlementOverrides); err != nil {
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
		}
		switch subAction {
		case "created":
			res.Created = append(res.Created, alias)
		case "updated":
			res.Updated = append(res.Updated, alias)
		case "noop":
			res.Noop = append(res.Noop, alias)
		}

		if e.Model == "prepaid" {
			if err := ensurePrepaidBalance(ctx, exec, tenantID, currency); err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
		}

		// Stamp the billing tier into QM's tenants.deployment_tier — Purser is
		// that column's authority. Emitted regardless of cluster_access: tier
		// stamping is not cluster entitlement, and `none` entries still carry a
		// billing tier the alias/custom-domain gates must see.
		post = append(post, PostCommitOp{
			Kind: PostCommitSetDeploymentTier, TenantID: tenantID, Tier: e.Tier, Alias: alias,
		})

		switch e.ClusterAccess {
		case "", "derived":
			official, err := loadOfficial()
			if err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
			eligible, err := eligibleClusters(ctx, exec, tierLevel, official)
			if err != nil {
				return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
			for _, c := range eligible {
				post = append(post, PostCommitOp{
					Kind: PostCommitGrantClusterAccess, TenantID: tenantID, ClusterID: c.ClusterID, Alias: alias,
				})
			}
			if candidates := tieraccess.PrimaryCandidates(eligible, official); len(candidates) > 0 {
				post = append(post, PostCommitOp{
					Kind: PostCommitSetPrimaryCluster, TenantID: tenantID, ClusterID: candidates[0],
					PrimaryCandidates: candidates, Alias: alias,
				})
			}
		case "none":
			// operator-asserted opt-out; no QM calls.
		default:
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: cluster_access %q invalid (expected \"\"|\"derived\"|\"none\")", alias, e.ClusterAccess)
		}
		// Commodore resolves the invalidation fanout from the tenant's
		// Quartermaster routing, which a fresh tenant only has once the
		// primary-cluster and deployment-tier ops above have run.
		if grantChanged {
			post = append(post, PostCommitOp{Kind: PostCommitInvalidateTenantCache, TenantID: tenantID, Alias: alias})
		}
		if boundary != nil {
			boundary.applied = true
		}
	}

	return res, post, nil
}

// commitCustomerGrantPhases isolates a tenant whose rated closing document changed.
// Storage errors must escape to the outer transaction retry, especially 40001.
func commitCustomerGrantPhases(ctx context.Context, exec DBTX, boundary *PreparedCustomerGrant) error {
	if boundary.prepaid == nil && boundary.close == nil {
		return nil
	}
	tx, ok := exec.(*sql.Tx)
	if !ok {
		return errors.New("prepared grants require a transaction")
	}
	if err := purserdb.New(tx).BeginBootstrapGrantPhase(ctx); err != nil {
		return err
	}
	var err error
	if boundary.prepaid != nil {
		_, err = boundary.prepaid.CommitTx(ctx, tx)
	}
	if err == nil && boundary.close != nil {
		_, err = boundary.close.CommitTx(ctx, tx)
	}
	if err != nil {
		if !errors.Is(err, handlers.ErrPrepaidPhaseChanged) && !errors.Is(err, handlers.ErrPostpaidPhaseChanged) {
			return err
		}
		if rollbackErr := purserdb.New(tx).RollbackBootstrapGrantPhase(ctx); rollbackErr != nil {
			return rollbackErr
		}
	}
	if releaseErr := purserdb.New(tx).ReleaseBootstrapGrantPhase(ctx); releaseErr != nil {
		return releaseErr
	}
	return err
}

// PostCommitOp is cross-service work the dispatcher executes after the local
// reconcile transaction commits — and that --dry-run skips. The opaque kind
// keeps this type free of any gRPC types so the bootstrap pkg stays
// stand-alone; the dispatcher provides a Quartermaster client and dispatches
// per Kind.
type PostCommitOp struct {
	Kind      PostCommitKind
	TenantID  string
	ClusterID string
	Tier      string // billing tier_name, for set_deployment_tier
	Alias     string // for human-readable reporting
	// PrimaryCandidates, for set_primary_cluster, are the clusters the
	// primary may be: the dispatcher keeps a current primary among them and
	// otherwise sets ClusterID, the first candidate.
	PrimaryCandidates []string
}

type PostCommitKind string

const (
	// PostCommitInvalidateTenantCache refreshes Commodore and Gateway billing caches.
	PostCommitInvalidateTenantCache PostCommitKind = "invalidate_tenant_cache"
	// PostCommitGrantClusterAccess invokes Quartermaster's
	// BootstrapClusterAccess RPC — the service-token-gated entitlement entry
	// point a bootstrap caller (no tenant session) is allowed to use.
	PostCommitGrantClusterAccess PostCommitKind = "grant_cluster_access"
	// PostCommitSetPrimaryCluster invokes UpdateTenantCluster with
	// primary_cluster_id unless the tenant's current primary is already one
	// of the op's PrimaryCandidates.
	PostCommitSetPrimaryCluster PostCommitKind = "set_primary_cluster"
	// PostCommitSetDeploymentTier invokes UpdateTenant with deployment_tier =
	// the entry's billing tier_name. Purser owns that QM column; bootstrap
	// stamps it so desired-state customers converge immediately instead of
	// waiting for the hourly deployment-tier sweep.
	PostCommitSetDeploymentTier PostCommitKind = "set_deployment_tier"
)

// QMBootstrapClient is the cross-service surface ReconcileCustomerBilling
// needs from Quartermaster. The cobra dispatcher wires it to the QM gRPC
// client; tests inject a fake. Keeping it narrow keeps the bootstrap pkg
// free of any gRPC dependency.
type QMBootstrapClient interface {
	// Resolve maps a bootstrap tenant alias to the tenant UUID. Backed by
	// ResolveTenantAliases at runtime.
	Resolve(ctx context.Context, alias string) (string, error)
	// PlatformOfficialClusters returns the QM-projected set of clusters
	// flagged is_platform_official; each value reports whether that cluster
	// can be a tenant's primary (models.ClusterTypeCanBePreferred). Bootstrap
	// intersects the set with priced clusters to compute customer
	// entitlement; private clusters with pricing rows must NOT auto-grant.
	PlatformOfficialClusters(ctx context.Context) (map[string]bool, error)
}

func validateCustomerBilling(e CustomerBilling) error {
	if e.OperatorGrant != nil {
		if err := e.OperatorGrant.Validate(); err != nil {
			return err
		}
	}
	if e.Tenant.Ref == "" {
		return errors.New("tenant.ref required")
	}
	switch e.Model {
	case "prepaid", "postpaid":
	default:
		return fmt.Errorf("model must be \"prepaid\" or \"postpaid\" (got %q)", e.Model)
	}
	if e.Tier == "" {
		return errors.New("tier required")
	}
	return nil
}

// aliasFromRef parses a TenantRef.Ref into the alias. Mirrors the QM-side
// AliasFromRef so Purser doesn't pull api_tenants as a dependency.
func aliasFromRef(ref string) (string, error) {
	if ref == "quartermaster.system_tenant" {
		return "frameworks", nil
	}
	const prefix = "quartermaster.tenants["
	if strings.HasPrefix(ref, prefix) && strings.HasSuffix(ref, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(ref, prefix), "]"), nil
	}
	return "", fmt.Errorf("malformed tenant ref %q", ref)
}

func resolveTier(ctx context.Context, exec DBTX, slug string) (tierID string, tierLevel int32, currency string, err error) {
	tier, queryErr := purserdb.New(exec).GetBootstrapBillingTier(ctx, slug)
	if queryErr != nil {
		if errors.Is(queryErr, sql.ErrNoRows) {
			return "", 0, "", fmt.Errorf("tier slug %q not in purser.billing_tiers (run `purser bootstrap` so the embedded catalog is reconciled first)", slug)
		}
		return "", 0, "", fmt.Errorf("resolve tier: %w", queryErr)
	}
	tierID, tierLevel, currency = tier.ID.String(), tier.TierLevel, tier.Currency
	if currency == "" {
		currency = billing.LedgerCurrency
	}
	return tierID, tierLevel, currency, nil
}

func upsertTenantSubscription(ctx context.Context, exec DBTX, tenantID, tierID string, e CustomerBilling) (string, error) {
	queries := purserdb.New(exec)
	tierUUID, err := uuid.Parse(tierID)
	if err != nil {
		return "", fmt.Errorf("parse billing tier id: %w", err)
	}
	current, err := queries.GetBootstrapTenantSubscription(ctx, tenantID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Desired state carries no billing country, so a bootstrapped tenant
		// starts in the presentment currency of an unknown country until its
		// billing details set one.
		if insertErr := queries.InsertBootstrapTenantSubscription(ctx, purserdb.InsertBootstrapTenantSubscriptionParams{
			ID: uuid.New(), TenantID: tenantID, TierID: tierUUID, BillingModel: e.Model,
			PresentmentCurrency: billing.PresentmentCurrencyForCountry(""),
		}); insertErr != nil {
			return "", fmt.Errorf("insert tenant_subscriptions: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe tenant_subscriptions: %w", err)
	}
	if current.TierID == tierUUID && current.BillingModel == e.Model {
		return "noop", nil
	}
	if current.BillingModel != e.Model {
		return "", &customerDeclarationDeferred{fmt.Errorf("tenant %s runs %s billing; desired state declares %s, and a switch between billing models closes the current period with a billing document, so make it with `frameworks admin billing set-tier` and then update the desired state", tenantID, current.BillingModel, e.Model)}
	}
	affected, err := queries.UpdateBootstrapTenantSubscription(ctx, purserdb.UpdateBootstrapTenantSubscriptionParams{
		TierID: tierUUID, BillingModel: e.Model, TenantID: tenantID,
	})
	if err != nil {
		return "", fmt.Errorf("update tenant_subscriptions: %w", err)
	}
	if affected != 1 {
		return "", errors.New("update tenant_subscriptions: probed row disappeared")
	}
	return "updated", nil
}

func reconcileEntitlementOverrides(ctx context.Context, exec DBTX, tenantID string, desired map[string]any) error {
	if desired == nil {
		return nil
	}
	queries := purserdb.New(exec)
	subscriptionID, err := queries.GetBootstrapSubscriptionID(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("lookup tenant subscription for entitlement overrides: %w", err)
	}
	if len(desired) == 0 {
		err := queries.DeleteBootstrapEntitlementOverrides(ctx, subscriptionID)
		if err != nil {
			return fmt.Errorf("clear subscription entitlement overrides: %w", err)
		}
		return nil
	}
	if err := queries.DeleteBootstrapEntitlementOverrides(ctx, subscriptionID); err != nil {
		return fmt.Errorf("replace subscription entitlement overrides: %w", err)
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := desired[key]
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode entitlement override %q: %w", key, err)
		}
		if err := queries.InsertBootstrapEntitlementOverride(ctx, purserdb.InsertBootstrapEntitlementOverrideParams{
			SubscriptionID: subscriptionID, Key: key, Value: encoded,
		}); err != nil {
			return fmt.Errorf("insert entitlement override %q: %w", key, err)
		}
	}
	return nil
}

// ensurePrepaidBalance mirrors InitializePrepaidAccount's balance step: a
// 0-balance row at the tier currency with the same low-balance threshold the
// runtime path uses. Idempotent via the (tenant_id, currency) UNIQUE.
func ensurePrepaidBalance(ctx context.Context, exec DBTX, tenantID, currency string) error {
	if err := purserdb.New(exec).EnsureBootstrapPrepaidBalance(ctx, purserdb.EnsureBootstrapPrepaidBalanceParams{
		ID: uuid.New(), TenantID: tenantID, Currency: currency,
	}); err != nil {
		return fmt.Errorf("insert prepaid_balances: %w", err)
	}
	return nil
}

// eligibleClusters returns the cluster_pricing rows a tenant qualifies for at
// its tier_level. Mirrors the runtime tier-access SELECT but with official
// cluster IDs supplied by the caller (QM-owned, not derived from
// cluster_pricing).
func eligibleClusters(ctx context.Context, exec DBTX, tierLevel int32, official map[string]bool) ([]tieraccess.EligibleCluster, error) {
	if len(official) == 0 {
		return nil, nil
	}
	idSlice := make([]string, 0, len(official))
	for id := range official {
		idSlice = append(idSlice, id)
	}
	rows, err := purserdb.New(exec).ListBootstrapEligibleClusters(ctx, purserdb.ListBootstrapEligibleClustersParams{
		ClusterIds: idSlice, TierLevel: tierLevel,
	})
	if err != nil {
		return nil, fmt.Errorf("query eligible clusters: %w", err)
	}
	out := make([]tieraccess.EligibleCluster, 0, len(rows))
	for _, row := range rows {
		out = append(out, tieraccess.EligibleCluster{ClusterID: row.ClusterID, RequiredLevel: row.RequiredTierLevel})
	}
	return out, nil
}
