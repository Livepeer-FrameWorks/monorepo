package quartermasterdb

import (
	"context"
	"fmt"
	"strings"
)

type TenantUpdate struct {
	Name              *string
	SubdomainSet      bool
	Subdomain         *string
	CustomDomain      *string
	LogoURL           *string
	PrimaryColor      *string
	SecondaryColor    *string
	DeploymentTier    *string
	DeploymentModel   *string
	PrimaryClusterID  *string
	IsActive          *bool
	MonitoringEnabled *bool
}

// UpdateTenantFields builds only from this finite field allowlist. The update
// shape is dynamic, but neither identifiers nor SQL fragments come from callers.
func (q *Queries) UpdateTenantFields(ctx context.Context, tenantID string, patch TenantUpdate) (int64, error) {
	updates := make([]string, 0, 12)
	args := make([]any, 0, 12)
	add := func(column string, value any) {
		args = append(args, value)
		updates = append(updates, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if patch.Name != nil {
		add("name", *patch.Name)
	}
	if patch.SubdomainSet {
		if patch.Subdomain == nil {
			add("subdomain", nil)
		} else {
			add("subdomain", *patch.Subdomain)
		}
	}
	if patch.CustomDomain != nil {
		add("custom_domain", *patch.CustomDomain)
	}
	if patch.LogoURL != nil {
		add("logo_url", *patch.LogoURL)
	}
	if patch.PrimaryColor != nil {
		add("primary_color", *patch.PrimaryColor)
	}
	if patch.SecondaryColor != nil {
		add("secondary_color", *patch.SecondaryColor)
	}
	if patch.DeploymentTier != nil {
		add("deployment_tier", *patch.DeploymentTier)
	}
	if patch.DeploymentModel != nil {
		add("deployment_model", *patch.DeploymentModel)
	}
	if patch.PrimaryClusterID != nil {
		add("primary_cluster_id", *patch.PrimaryClusterID)
	}
	if patch.IsActive != nil {
		add("is_active", *patch.IsActive)
	}
	if patch.MonitoringEnabled != nil {
		add("monitoring_enabled", *patch.MonitoringEnabled)
	}
	updates = append(updates, "updated_at = NOW()")
	args = append(args, tenantID)
	query := fmt.Sprintf("UPDATE quartermaster.tenants SET %s WHERE id = $%d", strings.Join(updates, ", "), len(args))
	result, err := q.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
