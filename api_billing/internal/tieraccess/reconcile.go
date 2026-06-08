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
	"sync"
	"time"

	"github.com/lib/pq"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
)

// quartermasterAPI is the subset of the Quartermaster gRPC client that the
// reconciler depends on. Declaring it here (consumer-side) lets tests drive
// the grant/primary/suspend ordering against a fake; *qmclient.GRPCClient
// satisfies it in production.
type quartermasterAPI interface {
	ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	ListTenantClusterAccess(ctx context.Context, tenantID string) (*quartermasterpb.ListTenantClusterAccessResponse, error)
	GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error)
	UpdateTenant(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error)
	BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, resourceLimits *tenantlimitspb.TenantResourceLimits) error
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
	db     *sql.DB
	qm     quartermasterAPI
	logger logging.Logger

	mu       sync.RWMutex
	cache    map[string]bool
	cacheExp time.Time
}

// NewReconciler constructs a Reconciler shared by PurserServer and JobManager.
func NewReconciler(db *sql.DB, qm *qmclient.GRPCClient, logger logging.Logger) *Reconciler {
	return &Reconciler{db: db, qm: qm, logger: logger}
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
// clusters the tenant is entitled to at tierLevel. Ordered grant → set
// primary → suspend so primary_cluster_id is never left pointing at a
// suspended access row.
func (r *Reconciler) Reconcile(ctx context.Context, tenantID string, tierLevel int32) (eligibleClusterIDs []string, primaryClusterID string, err error) {
	officialIDs, err := r.OfficialClusterIDs(ctx)
	if err != nil {
		return nil, "", err
	}
	if len(officialIDs) == 0 {
		return nil, "", nil
	}

	idSlice := make([]string, 0, len(officialIDs))
	for id := range officialIDs {
		idSlice = append(idSlice, id)
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT cluster_id, required_tier_level
		FROM purser.cluster_pricing
		WHERE cluster_id = ANY($1)
		  AND required_tier_level <= $2
		  AND (allow_free_tier = true OR $2 > 0)
		ORDER BY required_tier_level DESC, cluster_id ASC
	`, pq.Array(idSlice), tierLevel)
	if err != nil {
		return nil, "", fmt.Errorf("query eligible clusters: %w", err)
	}
	defer rows.Close()

	type eligibleEntry struct {
		clusterID string
		reqLevel  int32
	}
	var eligible []eligibleEntry
	eligibleSet := make(map[string]struct{})
	var bestLevel int32 = -1
	var topLevelCandidates []string
	for rows.Next() {
		var entry eligibleEntry
		if err := rows.Scan(&entry.clusterID, &entry.reqLevel); err != nil {
			return nil, "", fmt.Errorf("scan cluster row: %w", err)
		}
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
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate cluster rows: %w", err)
	}

	currentActive := make(map[string]struct{})
	currentPrimary := ""
	if resp, listErr := r.qm.ListTenantClusterAccess(ctx, tenantID); listErr != nil {
		return nil, "", fmt.Errorf("list tenant cluster access: %w", listErr)
	} else {
		for _, row := range resp.GetRows() {
			if row.GetIsActive() && row.GetIsPlatformOfficial() {
				currentActive[row.GetClusterId()] = struct{}{}
			}
		}
	}
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
		if subErr := r.qm.BootstrapClusterAccess(ctx, tenantID, entry.clusterID, nil); subErr != nil {
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
			if _, err := r.qm.UpdateTenant(ctx, &quartermasterpb.UpdateTenantRequest{
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

	return eligibleClusterIDs, primaryClusterID, nil
}
