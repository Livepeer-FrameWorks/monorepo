// Package tieraccess reconciles tenant_cluster_access against the
// platform-official clusters a tenant is entitled to at a given tier level.
//
// It is the single source of truth for "what cluster access should this
// tenant have, given their tier?" — invoked both from the gRPC
// ChangeBillingTier path and from the billing-close job's downgrade applier.
package tieraccess

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// quartermasterAPI is the subset of the Quartermaster gRPC client that the
// reconciler depends on. Declaring it here (consumer-side) lets tests drive
// the grant/primary/suspend ordering against a fake; *qmclient.GRPCClient
// satisfies it in production.
type quartermasterAPI interface {
	ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	ListTenantClusterAccess(ctx context.Context, tenantID string) (*quartermasterpb.ListTenantClusterAccessResponse, error)
	ListTenants(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)
	GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error)
	UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error
	ApplyTenantBillingEntitlements(ctx context.Context, req *quartermasterpb.ApplyTenantBillingEntitlementsRequest) (*quartermasterpb.ApplyTenantBillingEntitlementsResponse, error)
	CompleteTenantDNSEntitlementHandoff(ctx context.Context, subscriptionCount int64) (*quartermasterpb.CompleteTenantDNSEntitlementHandoffResponse, error)
	BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, resourceLimits *tenantlimitspb.TenantResourceLimits, actor *commonpb.RequestActor) error
	DeactivateClusterAccess(ctx context.Context, tenantID, clusterID, reason string) error
}

// Reconciler computes desired cluster access from Purser-owned data and
// applies it via Quartermaster RPCs. Holds a short cache of the
// platform-official cluster ID set (5 min) to keep reconciles cheap.
//
// Service-boundary rule: Reconciler reads only Purser-owned tables and never
// touches quartermaster.tenant_cluster_access directly — current state comes
// from Quartermaster.ListTenantClusterAccess.
type Reconciler struct {
	db                     *sql.DB
	qm                     quartermasterAPI
	logger                 logging.Logger
	reconciliationFailures *prometheus.CounterVec

	mu       sync.RWMutex
	cache    map[string]bool
	cacheExp time.Time
}

// ReconcileCanonical applies the tenant's current committed subscription tier
// and retries if a concurrent billing change supersedes that tier while the
// cross-service reconcile is in flight.
func (r *Reconciler) ReconcileCanonical(ctx context.Context, tenantID string) ([]string, string, error) {
	queries := purserdb.New(r.db)
	for attempt := 0; attempt < 4; attempt++ {
		canonical, err := queries.GetCanonicalSubscriptionTier(ctx, tenantID)
		if err != nil {
			return nil, "", fmt.Errorf("load canonical tier: %w", err)
		}
		eligible, primary, err := r.Reconcile(ctx, tenantID, canonical.TierLevel, canonical.TierName)
		if err != nil {
			return nil, "", err
		}
		currentTierID, err := queries.GetTenantSubscriptionTierID(ctx, tenantID)
		if err != nil {
			return nil, "", fmt.Errorf("verify canonical tier after reconcile: %w", err)
		}
		if currentTierID == canonical.TierID {
			return eligible, primary, nil
		}
	}
	return nil, "", fmt.Errorf("subscription tier kept changing during cluster reconciliation")
}

// NewReconciler constructs a Reconciler shared by PurserServer and JobManager.
func NewReconciler(db *sql.DB, qm *qmclient.GRPCClient, logger logging.Logger, failureMetrics ...*prometheus.CounterVec) *Reconciler {
	var failures *prometheus.CounterVec
	if len(failureMetrics) > 0 {
		failures = failureMetrics[0]
	}
	return &Reconciler{db: db, qm: qm, logger: logger, reconciliationFailures: failures}
}

func (r *Reconciler) recordFailure(operation string) {
	if r.reconciliationFailures != nil {
		r.reconciliationFailures.WithLabelValues(operation).Inc()
	}
}

// OfficialClusterIDs returns the set of platform-official cluster IDs from
// Quartermaster, cached for 5 minutes. Returns an empty map (never nil) when
// no rows exist.
func (r *Reconciler) OfficialClusterIDs(ctx context.Context) (map[string]bool, error) {
	r.mu.RLock()
	if r.cache != nil && time.Now().Before(r.cacheExp) {
		ids := r.cache
		r.mu.RUnlock()
		return ids, nil
	}
	r.mu.RUnlock()

	if r.qm == nil {
		return map[string]bool{}, nil
	}
	resp, err := r.qm.ListOfficialClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("list official clusters: %w", err)
	}
	ids := make(map[string]bool, len(resp.GetClusters()))
	for _, c := range resp.GetClusters() {
		ids[c.GetClusterId()] = true
	}

	r.mu.Lock()
	r.cache = ids
	r.cacheExp = time.Now().Add(5 * time.Minute)
	r.mu.Unlock()
	return ids, nil
}

// Reconcile brings tenant_cluster_access in line with the platform-official
// clusters the tenant is entitled to at tierLevel, then stamps tierName into
// Quartermaster's tenants.deployment_tier — Purser is that column's
// authority; Quartermaster's alias/custom-domain gates read it. Ordered
// grant → set primary → suspend → stamp so primary_cluster_id is never left
// pointing at a suspended access row, and the stamp's alias side-effects in
// Quartermaster (UpdateTenant enqueues alias ensure/remove on tier change)
// see the final cluster-access state.
func (r *Reconciler) Reconcile(ctx context.Context, tenantID string, tierLevel int32, tierName string) (eligibleClusterIDs []string, primaryClusterID string, err error) {
	defer func() {
		if err != nil {
			r.recordFailure("reconcile")
		}
	}()
	observedAt := time.Now().UTC()
	officialIDs, err := r.OfficialClusterIDs(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(officialIDs) == 0 {
		// An empty official inventory must not revoke cluster access, but DNS
		// entitlement materialization is independent of cluster discovery and
		// still has to converge (especially for fresh and suspended tenants).
		if r.qm == nil {
			return []string{}, "", nil
		}
		tenantResp, tenantErr := r.qm.GetTenant(ctx, tenantID)
		if tenantErr != nil {
			return nil, "", fmt.Errorf("get tenant for DNS entitlements: %w", tenantErr)
		}
		if applyErr := r.applyDNSEntitlements(ctx, tenantID, tierName, tenantResp.GetTenant(), observedAt); applyErr != nil {
			return nil, "", applyErr
		}
		return []string{}, "", nil
	}

	// The access rows are read before eligibility: a cluster Quartermaster
	// reports as platform-official in this read is judged by its pricing even
	// when the cached official list predates it. Suspension below is
	// destructive, so it must never follow from a stale cache alone.
	currentActive := make(map[string]struct{})
	if resp, listErr := r.qm.ListTenantClusterAccess(ctx, tenantID); listErr != nil {
		return nil, "", fmt.Errorf("list tenant cluster access: %w", listErr)
	} else {
		for _, row := range resp.GetRows() {
			if row.GetIsActive() && row.GetIsPlatformOfficial() {
				currentActive[row.GetClusterId()] = struct{}{}
			}
		}
	}

	idSlice := make([]string, 0, len(officialIDs)+len(currentActive))
	for id := range officialIDs {
		idSlice = append(idSlice, id)
	}
	for id := range currentActive {
		if !officialIDs[id] {
			idSlice = append(idSlice, id)
		}
	}

	rows, err := purserdb.New(r.db).ListEligibleOfficialClusters(ctx, purserdb.ListEligibleOfficialClustersParams{
		ClusterIds: idSlice, TierLevel: tierLevel,
	})
	if err != nil {
		return nil, "", fmt.Errorf("query eligible clusters: %w", err)
	}

	type eligibleEntry struct {
		clusterID string
		reqLevel  int32
	}
	var eligible []eligibleEntry
	eligibleSet := make(map[string]struct{})
	var bestLevel int32 = -1
	var topLevelCandidates []string
	for _, row := range rows {
		entry := eligibleEntry{clusterID: row.ClusterID, reqLevel: row.RequiredTierLevel.Int32}
		eligible = append(eligible, entry)
		eligibleSet[entry.clusterID] = struct{}{}
		eligibleClusterIDs = append(eligibleClusterIDs, entry.clusterID)
		if entry.reqLevel > bestLevel {
			bestLevel = entry.reqLevel
			topLevelCandidates = topLevelCandidates[:0]
		}
		if entry.reqLevel == bestLevel {
			topLevelCandidates = append(topLevelCandidates, entry.clusterID)
		}
	}
	currentPrimary := ""
	tenantResp, tErr := r.qm.GetTenant(ctx, tenantID)
	if tErr != nil {
		return nil, "", fmt.Errorf("get tenant primary cluster: %w", tErr)
	}
	if tenantResp != nil && tenantResp.GetTenant() != nil {
		currentPrimary = tenantResp.GetTenant().GetPrimaryClusterId()
	}

	// (a) Grant: eligible \ currentActive.
	for _, entry := range eligible {
		if _, already := currentActive[entry.clusterID]; already {
			continue
		}
		if subErr := r.qm.BootstrapClusterAccess(ctx, tenantID, entry.clusterID, nil, nil); subErr != nil {
			return eligibleClusterIDs, primaryClusterID, fmt.Errorf("grant cluster access %s: %w", entry.clusterID, subErr)
		}
	}

	// (b) Pick + set primary. Prefer the existing primary when it is in the
	// top-level subset (avoid churn on tied configurations); otherwise the
	// alphabetically-first cluster at the highest required_tier_level.
	if len(topLevelCandidates) > 0 {
		primaryClusterID = topLevelCandidates[0]
		if slices.Contains(topLevelCandidates, currentPrimary) {
			primaryClusterID = currentPrimary
		}
		if primaryClusterID != currentPrimary {
			if err := r.qm.UpdateTenantCluster(ctx, &quartermasterpb.UpdateTenantClusterRequest{
				TenantId:         tenantID,
				PrimaryClusterId: &primaryClusterID,
			}); err != nil {
				return eligibleClusterIDs, primaryClusterID, fmt.Errorf("set primary cluster %s: %w", primaryClusterID, err)
			}
		}
	}

	// (c) Suspend: currentActive \ eligible. Safe after primary has moved.
	for clusterID := range currentActive {
		if _, stillEligible := eligibleSet[clusterID]; stillEligible {
			continue
		}
		if subErr := r.qm.DeactivateClusterAccess(ctx, tenantID, clusterID, "tier_downgrade"); subErr != nil {
			return eligibleClusterIDs, primaryClusterID, fmt.Errorf("deactivate cluster access %s: %w", clusterID, subErr)
		}
	}

	// (d) Materialize the canonical, Purser-owned DNS entitlements.
	if applyErr := r.applyDNSEntitlements(ctx, tenantID, tierName, tenantResp.GetTenant(), observedAt); applyErr != nil {
		return eligibleClusterIDs, primaryClusterID, applyErr
	}

	return eligibleClusterIDs, primaryClusterID, nil
}

func (r *Reconciler) applyDNSEntitlements(ctx context.Context, tenantID, tierName string, currentTenant *quartermasterpb.Tenant, observedAt time.Time) error {
	// Unknown or missing keys resolve false in SQL. observedAt was captured
	// before the read/reconcile so Quartermaster can reject a late stale writer.
	dns, dnsErr := purserdb.New(r.db).LoadEffectiveDNSEntitlements(ctx, tenantID)
	if dnsErr != nil {
		return fmt.Errorf("load DNS entitlements: %w", dnsErr)
	}
	if tierName != "" && dns.TierName != tierName {
		return fmt.Errorf("effective tier changed during reconcile: got %s, expected %s", dns.TierName, tierName)
	}
	if currentTenant == nil || !currentTenant.GetBillingEntitlementsObserved() ||
		currentTenant.GetDeploymentTier() != dns.TierName ||
		currentTenant.GetCustomSubdomainEnabled() != dns.CustomSubdomainEnabled ||
		currentTenant.GetCustomDomainEnabled() != dns.CustomDomainEnabled {
		if _, applyErr := r.qm.ApplyTenantBillingEntitlements(ctx, &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
			TenantId:               tenantID,
			DeploymentTier:         dns.TierName,
			CustomSubdomainEnabled: dns.CustomSubdomainEnabled,
			CustomDomainEnabled:    dns.CustomDomainEnabled,
			ObservedAt:             timestamppb.New(observedAt),
		}); applyErr != nil {
			return fmt.Errorf("apply DNS entitlements: %w", applyErr)
		}
	}
	return nil
}

// RevokeDNSEntitlements advances Quartermaster's billing-observation fence and
// tears down both served DNS forms after a subscription cancellation. The
// requested names remain in Quartermaster; only the effective grants change.
func (r *Reconciler) RevokeDNSEntitlements(ctx context.Context, tenantID string) (err error) {
	defer func() {
		if err != nil {
			r.recordFailure("revoke")
		}
	}()
	if r.qm == nil {
		return nil
	}
	observedAt := time.Now().UTC()
	resp, err := r.qm.GetTenant(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("get tenant for DNS entitlement revocation: %w", err)
	}
	tenant := resp.GetTenant()
	if tenant == nil || strings.TrimSpace(tenant.GetDeploymentTier()) == "" {
		return fmt.Errorf("tenant has no materialized deployment tier")
	}
	_, err = r.qm.ApplyTenantBillingEntitlements(ctx, &quartermasterpb.ApplyTenantBillingEntitlementsRequest{
		TenantId: tenantID, DeploymentTier: tenant.GetDeploymentTier(), ObservedAt: timestamppb.New(observedAt),
	})
	if err != nil {
		return fmt.Errorf("apply DNS entitlement revocation: %w", err)
	}
	return nil
}
