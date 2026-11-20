package quartermasterdb

import (
	"context"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

type ClusterUpdate struct {
	ClusterName             *string
	BaseURL                 *string
	DatabaseURL             *string
	PeriscopeURL            *string
	KafkaBrokers            []string
	MaxConcurrentStreams    *int32
	MaxConcurrentViewers    *int32
	HealthStatus            *string
	IsActive                *bool
	OwnerTenantIDSet        bool
	OwnerTenantID           *string
	DeploymentModel         *string
	IsPlatformOfficial      *bool
	IsDefaultCluster        *bool
	AllowPrivatePullSources *bool
	PublicTopology          *bool
}

// UpdateClusterFields builds only from this finite identifier allowlist.
func (q *Queries) UpdateClusterFields(ctx context.Context, clusterID string, patch ClusterUpdate) (int64, error) {
	updates := make([]string, 0, 12)
	args := make([]any, 0, 12)
	add := func(column string, value any) {
		args = append(args, value)
		updates = append(updates, fmt.Sprintf("%s = $%d", column, len(args)))
	}
	if patch.ClusterName != nil {
		add("cluster_name", *patch.ClusterName)
	}
	if patch.BaseURL != nil {
		add("base_url", *patch.BaseURL)
	}
	if patch.DatabaseURL != nil {
		add("database_url", *patch.DatabaseURL)
	}
	if patch.PeriscopeURL != nil {
		add("periscope_url", *patch.PeriscopeURL)
	}
	if len(patch.KafkaBrokers) > 0 {
		add("kafka_brokers", pq.Array(patch.KafkaBrokers))
	}
	if patch.MaxConcurrentStreams != nil {
		add("max_concurrent_streams", *patch.MaxConcurrentStreams)
	}
	if patch.MaxConcurrentViewers != nil {
		add("max_concurrent_viewers", *patch.MaxConcurrentViewers)
	}
	if patch.HealthStatus != nil {
		add("health_status", *patch.HealthStatus)
	}
	if patch.IsActive != nil {
		add("is_active", *patch.IsActive)
	}
	if patch.OwnerTenantIDSet {
		args = append(args, valueOrEmpty(patch.OwnerTenantID))
		updates = append(updates, fmt.Sprintf("owner_tenant_id = NULLIF($%d, '')::uuid", len(args)))
	}
	if patch.DeploymentModel != nil {
		add("deployment_model", *patch.DeploymentModel)
	}
	if patch.IsPlatformOfficial != nil {
		add("is_platform_official", *patch.IsPlatformOfficial)
	}
	if patch.IsDefaultCluster != nil {
		add("is_default_cluster", *patch.IsDefaultCluster)
	}
	if patch.AllowPrivatePullSources != nil {
		add("allow_private_pull_sources", *patch.AllowPrivatePullSources)
	}
	if patch.PublicTopology != nil {
		add("public_topology", *patch.PublicTopology)
	}
	updates = append(updates, "updated_at = NOW()")
	args = append(args, clusterID)
	query := fmt.Sprintf("UPDATE quartermaster.infrastructure_clusters SET %s WHERE cluster_id = $%d", strings.Join(updates, ", "), len(args))
	result, err := q.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
