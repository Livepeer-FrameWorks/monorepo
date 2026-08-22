package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_tenants/internal/database/quartermasterdb"
)

// ReconcileSystemTenantClusterAccess subscribes the system tenant to clusters
// based on the SystemTenantClusterAccess flags. Runs after clusters are
// reconciled so the SELECT against infrastructure_clusters returns the desired
// rows.
//
//   - DefaultClusters=true        ⇒ system tenant gets access to every
//     is_default_cluster row.
//   - PlatformOfficialClusters    ⇒ system tenant gets access to every
//     is_platform_official row.
//
// Each match upserts (system_tenant_id, cluster_id) into
// quartermaster.tenant_cluster_access with subscription_status='active'. The
// reconciler is additive — it never revokes existing access, only ensures the
// desired rows exist.
func ReconcileSystemTenantClusterAccess(ctx context.Context, exec DBTX, cfg *SystemTenantClusterAccess, aliases *AliasMap) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileSystemTenantClusterAccess: nil executor")
	}
	if cfg == nil {
		return Result{}, nil
	}
	if !cfg.DefaultClusters && !cfg.PlatformOfficialClusters {
		return Result{}, nil
	}

	systemTenantID, ok := aliases.LookupAlias(SystemTenantAlias)
	if !ok {
		return Result{}, fmt.Errorf("system tenant alias %q not in alias map (run tenants reconcile first)", SystemTenantAlias)
	}

	res := Result{}
	clusters, err := selectMatchingClusters(ctx, exec, cfg)
	if err != nil {
		return Result{}, err
	}
	for _, clusterID := range clusters {
		action, err := upsertTenantClusterAccess(ctx, exec, systemTenantID, clusterID)
		if err != nil {
			return Result{}, fmt.Errorf("system_tenant access cluster %q: %w", clusterID, err)
		}
		key := SystemTenantAlias + "→" + clusterID
		switch action {
		case "created":
			res.Created = append(res.Created, key)
		case "noop":
			res.Noop = append(res.Noop, key)
		}
	}

	return res, nil
}

func selectMatchingClusters(ctx context.Context, exec DBTX, cfg *SystemTenantClusterAccess) ([]string, error) {
	queries := quartermasterdb.New(exec)
	var (
		rows []string
		err  error
	)
	switch {
	case cfg.DefaultClusters && cfg.PlatformOfficialClusters:
		rows, err = queries.ListBootstrapDefaultOrOfficialAccessClusters(ctx)
	case cfg.DefaultClusters:
		rows, err = queries.ListBootstrapDefaultAccessClusters(ctx)
	default:
		rows, err = queries.ListBootstrapOfficialAccessClusters(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("select matching clusters: %w", err)
	}
	return rows, nil
}

func upsertTenantClusterAccess(ctx context.Context, exec DBTX, tenantID, clusterID string) (string, error) {
	queries := quartermasterdb.New(exec)
	params := quartermasterdb.GetBootstrapTenantClusterAccessParams{TenantID: tenantID, ClusterID: clusterID}
	current, err := queries.GetBootstrapTenantClusterAccess(ctx, params)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if insertErr := queries.InsertBootstrapTenantClusterAccess(ctx, quartermasterdb.InsertBootstrapTenantClusterAccessParams(params)); insertErr != nil {
			return "", fmt.Errorf("insert tenant_cluster_access: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe tenant_cluster_access: %w", err)
	}
	if current.IsActive && current.SubscriptionStatus == "active" {
		return "noop", nil
	}
	if err := queries.ActivateBootstrapTenantClusterAccess(ctx, quartermasterdb.ActivateBootstrapTenantClusterAccessParams(params)); err != nil {
		return "", fmt.Errorf("update tenant_cluster_access: %w", err)
	}
	return "created", nil
}
