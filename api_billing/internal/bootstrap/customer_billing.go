package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/pkg/billing"

	"github.com/google/uuid"
	"github.com/lib/pq"
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
func ReconcileCustomerBilling(ctx context.Context, exec DBTX, entries []CustomerBilling, qm QMBootstrapClient) (Result, []PostCommitOp, error) {
	if exec == nil {
		return Result{}, nil, errors.New("ReconcileCustomerBilling: nil executor")
	}
	if qm == nil {
		return Result{}, nil, errors.New("ReconcileCustomerBilling: nil QM bootstrap client")
	}
	if len(entries) == 0 {
		return Result{}, nil, nil
	}

	// Cache the platform-official set once per reconcile run. Bounded (a
	// handful of clusters) and the same set applies to every entry.
	var officialIDs map[string]struct{}
	loadOfficial := func() (map[string]struct{}, error) {
		if officialIDs != nil {
			return officialIDs, nil
		}
		ids, err := qm.PlatformOfficialClusterIDs(ctx)
		if err != nil {
			return nil, err
		}
		officialIDs = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			officialIDs[id] = struct{}{}
		}
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
		tenantID, err := qm.Resolve(ctx, alias)
		if err != nil {
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
		}
		tierID, tierLevel, currency, err := resolveTier(ctx, exec, e.Tier)
		if err != nil {
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
		}

		subAction, err := upsertTenantSubscription(ctx, exec, tenantID, tierID, e)
		if err != nil {
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
					Kind: PostCommitGrantClusterAccess, TenantID: tenantID, ClusterID: c.ID, Alias: alias,
				})
			}
			if primary := pickPrimary(eligible); primary != "" {
				post = append(post, PostCommitOp{
					Kind: PostCommitSetPrimaryCluster, TenantID: tenantID, ClusterID: primary, Alias: alias,
				})
			}
		case "none":
			// operator-asserted opt-out; no QM calls.
		default:
			return Result{}, nil, fmt.Errorf("customer_billing[%s]: cluster_access %q invalid (expected \"\"|\"derived\"|\"none\")", alias, e.ClusterAccess)
		}
	}

	return res, post, nil
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
	Alias     string // for human-readable reporting
}

type PostCommitKind string

const (
	// PostCommitGrantClusterAccess invokes Quartermaster's
	// BootstrapClusterAccess RPC — the service-token-gated entitlement entry
	// point a bootstrap caller (no tenant session) is allowed to use.
	PostCommitGrantClusterAccess PostCommitKind = "grant_cluster_access"
	// PostCommitSetPrimaryCluster invokes UpdateTenant with primary_cluster_id.
	PostCommitSetPrimaryCluster PostCommitKind = "set_primary_cluster"
)

// QMBootstrapClient is the cross-service surface ReconcileCustomerBilling
// needs from Quartermaster. The cobra dispatcher wires it to the QM gRPC
// client; tests inject a fake. Keeping it narrow keeps the bootstrap pkg
// free of any gRPC dependency.
type QMBootstrapClient interface {
	// Resolve maps a bootstrap tenant alias to the tenant UUID. Backed by
	// ResolveTenantAliases at runtime.
	Resolve(ctx context.Context, alias string) (string, error)
	// PlatformOfficialClusterIDs returns the QM-projected set of clusters
	// flagged is_platform_official. Bootstrap intersects this with priced
	// clusters to compute customer entitlement; private clusters with pricing
	// rows must NOT auto-grant.
	PlatformOfficialClusterIDs(ctx context.Context) ([]string, error)
}

func validateCustomerBilling(e CustomerBilling) error {
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
	const q = `SELECT id::text, tier_level, currency FROM purser.billing_tiers WHERE tier_name = $1`
	row := exec.QueryRowContext(ctx, q, slug)
	if scanErr := row.Scan(&tierID, &tierLevel, &currency); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", 0, "", fmt.Errorf("tier slug %q not in purser.billing_tiers (run `purser bootstrap` so the embedded catalog is reconciled first)", slug)
		}
		return "", 0, "", fmt.Errorf("resolve tier: %w", scanErr)
	}
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	return tierID, tierLevel, currency, nil
}

func upsertTenantSubscription(ctx context.Context, exec DBTX, tenantID, tierID string, e CustomerBilling) (string, error) {
	const probeSQL = `
		SELECT tier_id::text, billing_model
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1::uuid`
	var curTier, curModel string
	err := exec.QueryRowContext(ctx, probeSQL, tenantID).Scan(&curTier, &curModel)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO purser.tenant_subscriptions
				(id, tenant_id, tier_id, billing_model, status, started_at, created_at, updated_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'active', NOW(), NOW(), NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL, uuid.New().String(), tenantID, tierID, e.Model); insertErr != nil {
			return "", fmt.Errorf("insert tenant_subscriptions: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe tenant_subscriptions: %w", err)
	}
	if curTier == tierID && curModel == e.Model {
		return "noop", nil
	}
	const updateSQL = `
		UPDATE purser.tenant_subscriptions
		SET tier_id = $2::uuid, billing_model = $3, updated_at = NOW()
		WHERE tenant_id = $1::uuid`
	if _, err := exec.ExecContext(ctx, updateSQL, tenantID, tierID, e.Model); err != nil {
		return "", fmt.Errorf("update tenant_subscriptions: %w", err)
	}
	return "updated", nil
}

// ensurePrepaidBalance mirrors InitializePrepaidAccount's balance step: a
// 0-balance row at the tier currency with the same low-balance threshold the
// runtime path uses. Idempotent via the (tenant_id, currency) UNIQUE.
func ensurePrepaidBalance(ctx context.Context, exec DBTX, tenantID, currency string) error {
	const insertSQL = `
		INSERT INTO purser.prepaid_balances (id, tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at)
		VALUES ($1::uuid, $2::uuid, 0, $3, 500, NOW(), NOW())
		ON CONFLICT (tenant_id, currency) DO NOTHING`
	if _, err := exec.ExecContext(ctx, insertSQL, uuid.New().String(), tenantID, currency); err != nil {
		return fmt.Errorf("insert prepaid_balances: %w", err)
	}
	return nil
}

// eligibleCluster is one cluster_pricing row a tenant qualifies for at its
// tier_level. Mirrors the runtime ensureTierClusterAccess SELECT but with
// official cluster IDs supplied by the caller (QM-owned, not derived from
// cluster_pricing).
type eligibleCluster struct {
	ID            string
	RequiredLevel int32
}

func eligibleClusters(ctx context.Context, exec DBTX, tierLevel int32, official map[string]struct{}) ([]eligibleCluster, error) {
	if len(official) == 0 {
		return nil, nil
	}
	idSlice := make([]string, 0, len(official))
	for id := range official {
		idSlice = append(idSlice, id)
	}
	const q = `
		SELECT cluster_id, required_tier_level
		FROM purser.cluster_pricing
		WHERE cluster_id = ANY($1)
		  AND required_tier_level <= $2
		  AND (allow_free_tier = true OR $2 > 0)
		ORDER BY required_tier_level DESC`
	rows, err := exec.QueryContext(ctx, q, pq.Array(idSlice), tierLevel)
	if err != nil {
		return nil, fmt.Errorf("query eligible clusters: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []eligibleCluster
	for rows.Next() {
		var ec eligibleCluster
		if scanErr := rows.Scan(&ec.ID, &ec.RequiredLevel); scanErr != nil {
			return nil, fmt.Errorf("scan eligible cluster: %w", scanErr)
		}
		out = append(out, ec)
	}
	return out, rows.Err()
}

// pickPrimary picks the highest-tier-level cluster as the tenant's primary,
// matching ensureTierClusterAccess's selection (rows are pre-sorted by
// required_tier_level DESC, so out[0] is the best match if any).
func pickPrimary(eligible []eligibleCluster) string {
	if len(eligible) == 0 {
		return ""
	}
	return eligible[0].ID
}
