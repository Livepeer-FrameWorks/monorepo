package quartermasterdb

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type EdgeReleaseFilter struct {
	Channel *string
	Version *string
}

type EdgeReleaseRow struct {
	Channel        string
	Version        string
	ComponentsJSON string
	PublishedAt    time.Time
}

func (q *Queries) ListEdgeReleases(ctx context.Context, filter EdgeReleaseFilter) ([]EdgeReleaseRow, error) {
	where := []string{"TRUE"}
	args := []any{}
	if filter.Channel != nil {
		args = append(args, *filter.Channel)
		where = append(where, fmt.Sprintf("channel = $%d", len(args)))
	}
	if filter.Version != nil {
		args = append(args, *filter.Version)
		where = append(where, fmt.Sprintf("version = $%d", len(args)))
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT channel, version, components::text, published_at
		FROM quartermaster.edge_releases
		WHERE %s
		ORDER BY channel, published_at DESC, version DESC
	`, strings.Join(where, " AND ")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EdgeReleaseRow{}
	for rows.Next() {
		var row EdgeReleaseRow
		if err := rows.Scan(&row.Channel, &row.Version, &row.ComponentsJSON, &row.PublishedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type UpsertEdgeReleaseParams struct {
	Channel, Version, ComponentsJSON string
	PublishedAt                      time.Time
}

func (q *Queries) UpsertEdgeRelease(ctx context.Context, arg UpsertEdgeReleaseParams) (EdgeReleaseRow, error) {
	row := q.db.QueryRowContext(ctx, `
		INSERT INTO quartermaster.edge_releases (channel, version, components, published_at)
		VALUES ($1, $2, $3::jsonb, $4)
		ON CONFLICT (channel, version) DO UPDATE SET
			components = EXCLUDED.components,
			published_at = EXCLUDED.published_at
		RETURNING channel, version, components::text, published_at
	`, arg.Channel, arg.Version, arg.ComponentsJSON, arg.PublishedAt)
	var out EdgeReleaseRow
	err := row.Scan(&out.Channel, &out.Version, &out.ComponentsJSON, &out.PublishedAt)
	return out, err
}

type ClusterReleaseTargetFilter struct{ ClusterID *string }

type ClusterReleaseTargetRow struct {
	ClusterID, Channel, TargetVersion, RolloutPlanJSON string
	Paused                                             bool
	UpdatedAt                                          time.Time
}

func (q *Queries) ListClusterReleaseTargets(ctx context.Context, filter ClusterReleaseTargetFilter) ([]ClusterReleaseTargetRow, error) {
	where := "TRUE"
	args := []any{}
	if filter.ClusterID != nil {
		where = "cluster_id = $1"
		args = append(args, *filter.ClusterID)
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
			SELECT cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
		FROM quartermaster.cluster_release_targets
		WHERE %s
		ORDER BY updated_at DESC, cluster_id
	`, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ClusterReleaseTargetRow{}
	for rows.Next() {
		var row ClusterReleaseTargetRow
		if err := rows.Scan(&row.ClusterID, &row.Channel, &row.TargetVersion, &row.RolloutPlanJSON, &row.Paused, &row.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type UpsertClusterReleaseTargetParams struct {
	ClusterID, Channel, TargetVersion, RolloutPlanJSON string
	Paused                                             bool
}

func (q *Queries) UpsertClusterReleaseTarget(ctx context.Context, arg UpsertClusterReleaseTargetParams) (ClusterReleaseTargetRow, error) {
	row := q.db.QueryRowContext(ctx, `
			INSERT INTO quartermaster.cluster_release_targets (cluster_id, channel, target_version, rollout_plan, paused, updated_at)
			VALUES ($1, $2, NULLIF($3, ''), $4::jsonb, $5, NOW())
		ON CONFLICT (cluster_id) DO UPDATE SET
			channel = EXCLUDED.channel,
			target_version = EXCLUDED.target_version,
			rollout_plan = EXCLUDED.rollout_plan,
			paused = EXCLUDED.paused,
			updated_at = NOW()
		RETURNING cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
	`, arg.ClusterID, arg.Channel, arg.TargetVersion, arg.RolloutPlanJSON, arg.Paused)
	var out ClusterReleaseTargetRow
	err := row.Scan(&out.ClusterID, &out.Channel, &out.TargetVersion, &out.RolloutPlanJSON, &out.Paused, &out.UpdatedAt)
	return out, err
}

func (q *Queries) EdgeReleaseTargetExists(ctx context.Context, channel, version string) (bool, error) {
	var exists bool
	if strings.TrimSpace(version) == "" {
		err := q.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM quartermaster.edge_releases
				WHERE channel = $1
			)
		`, channel).Scan(&exists)
		return exists, err
	}
	err := q.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM quartermaster.edge_releases
			WHERE channel = $1 AND version = $2
		)
	`, channel, version).Scan(&exists)
	return exists, err
}

func (q *Queries) GetClusterReleaseTarget(ctx context.Context, clusterID string) (ClusterReleaseTargetRow, error) {
	row := q.db.QueryRowContext(ctx, `
			SELECT cluster_id, channel, COALESCE(target_version, ''), rollout_plan::text, COALESCE(paused, false), updated_at
		FROM quartermaster.cluster_release_targets
		WHERE cluster_id = $1
	`, clusterID)
	var out ClusterReleaseTargetRow
	err := row.Scan(&out.ClusterID, &out.Channel, &out.TargetVersion, &out.RolloutPlanJSON, &out.Paused, &out.UpdatedAt)
	return out, err
}
