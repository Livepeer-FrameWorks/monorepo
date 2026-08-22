package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

type TenantListFilter struct {
	CursorTime *time.Time
	CursorID   string
	Backward   bool
	Limit      int
}
type TenantListRow struct {
	ID, Name                                                      string
	Subdomain, CustomDomain, LogoURL                              sql.NullString
	PrimaryColor, SecondaryColor, DeploymentTier, DeploymentModel string
	PrimaryClusterID, OfficialClusterID, KafkaTopicPrefix         sql.NullString
	KafkaBrokers                                                  []string
	DatabaseURL                                                   sql.NullString
	IsActive, MonitoringEnabled                                   bool
	CreatedAt, UpdatedAt                                          time.Time
}

func (q *Queries) ListTenantsPage(ctx context.Context, filter TenantListFilter) ([]TenantListRow, error) {
	where, direction := "", "DESC"
	args := []any{}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op, direction = ">", "ASC"
		}
		where = fmt.Sprintf("WHERE (created_at, id) %s ($1, $2)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	} else if filter.Backward {
		direction = "ASC"
	}
	query := fmt.Sprintf(`
		SELECT id, name, subdomain, custom_domain, logo_url, primary_color, secondary_color,
		       deployment_tier, deployment_model, primary_cluster_id, official_cluster_id,
		       kafka_topic_prefix, kafka_brokers, database_url, is_active, monitoring_enabled, created_at, updated_at
		FROM quartermaster.tenants %s ORDER BY created_at %s, id %s LIMIT $%d
	`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TenantListRow
	for rows.Next() {
		var row TenantListRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Subdomain, &row.CustomDomain, &row.LogoURL,
			&row.PrimaryColor, &row.SecondaryColor, &row.DeploymentTier, &row.DeploymentModel,
			&row.PrimaryClusterID, &row.OfficialClusterID, &row.KafkaTopicPrefix, database.ArrayScan(&row.KafkaBrokers),
			&row.DatabaseURL, &row.IsActive, &row.MonitoringEnabled, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
