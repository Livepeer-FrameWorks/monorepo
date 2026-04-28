package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
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
	clauses := []string{}
	if cfg.DefaultClusters {
		clauses = append(clauses, "is_default_cluster = true")
	}
	if cfg.PlatformOfficialClusters {
		clauses = append(clauses, "is_platform_official = true")
	}
	where := strings.Join(clauses, " OR ")
	q := "SELECT cluster_id FROM quartermaster.infrastructure_clusters WHERE is_active = true AND (" + where + ") ORDER BY cluster_id"
	rows, err := exec.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("select matching clusters: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan cluster id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func upsertTenantClusterAccess(ctx context.Context, exec DBTX, tenantID, clusterID string) (string, error) {
	const probeSQL = `
		SELECT subscription_status, is_active
		FROM quartermaster.tenant_cluster_access
		WHERE tenant_id = $1::uuid AND cluster_id = $2`
	var sub string
	var active bool
	err := exec.QueryRowContext(ctx, probeSQL, tenantID, clusterID).Scan(&sub, &active)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO quartermaster.tenant_cluster_access
				(tenant_id, cluster_id, access_level, subscription_status, is_active, granted_at, created_at, updated_at)
			VALUES ($1::uuid, $2, 'shared', 'active', true, NOW(), NOW(), NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL, tenantID, clusterID); insertErr != nil {
			return "", fmt.Errorf("insert tenant_cluster_access: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe tenant_cluster_access: %w", err)
	}
	if active && sub == "active" {
		return "noop", nil
	}
	const updateSQL = `
		UPDATE quartermaster.tenant_cluster_access
		SET subscription_status = 'active', is_active = true, updated_at = NOW()
		WHERE tenant_id = $1::uuid AND cluster_id = $2`
	if _, err := exec.ExecContext(ctx, updateSQL, tenantID, clusterID); err != nil {
		return "", fmt.Errorf("update tenant_cluster_access: %w", err)
	}
	return "created", nil
}
