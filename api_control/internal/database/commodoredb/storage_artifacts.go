package commodoredb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// StorageArtifactFilter is the typed boundary for the storage catalog's
// dynamic filters and allowlisted ordering. sqlc cannot compile a runtime
// ORDER BY identifier, so this reporting query remains an explicit adapter.
type StorageArtifactFilter struct {
	TenantID, StreamID, Status, ArtifactHash, Search string
	Kinds, ArtifactHashes                            []string
	SortField, SortDirection                         string
	Limit, Offset                                    int
}

type StorageArtifactRow struct {
	Kind, ID, ArtifactHash, PlaybackID, StreamID, StreamTitle, Title, SecondaryLabel string
	SizeBytes                                                                        sql.NullInt64
	Status, StorageLocation, SyncStatus                                              sql.NullString
	CreatedAt, UpdatedAt                                                             time.Time
	ExpiresAt                                                                        sql.NullTime
	RetentionSource, OriginType, OriginID, StorageClusterID                          string
	HasThumbnails                                                                    bool
	DurationMs                                                                       sql.NullInt64
	Tracks                                                                           sql.NullString
	IsSynced, IsFinalized                                                            sql.NullBool
	Description, ErrorMessage, ThumbnailServingCluster                               string
}

type StorageArtifactCatalog struct {
	Rows       []StorageArtifactRow
	Total      int32
	KindCounts map[string]int32
}

const storageArtifactBaseSQL = `
SELECT kind, id, artifact_hash, playback_id, stream_id, stream_title, title, secondary_label,
       size_bytes, status, storage_location, created_at, updated_at, expires_at,
       retention_source, origin_type, origin_id, storage_cluster_id, has_thumbnails, duration_ms,
       tracks, sync_status, is_synced, is_finalized, description, error_message, thumbnail_serving_cluster
FROM (
    SELECT
        CASE WHEN COALESCE(v.origin_type, '') = 'dvr_chapter' THEN 'chapter' ELSE 'vod' END AS kind,
        v.id::text AS id, v.vod_hash AS artifact_hash, COALESCE(v.playback_id, '') AS playback_id,
        COALESCE(v.stream_id::text, '') AS stream_id, COALESCE(st.title, '') AS stream_title,
        COALESCE(NULLIF(v.title, ''), NULLIF(v.filename, ''), v.vod_hash) AS title,
        COALESCE(NULLIF(v.filename, ''), v.content_type, '') AS secondary_label,
        v.size_bytes,
        CASE WHEN v.retention_until IS NOT NULL AND v.retention_until <= NOW() THEN 'expired'
             WHEN v.lifecycle_status IN ('failed', 'aborted') OR v.sync_status IN ('failed', 'lost_local') THEN 'failed'
             WHEN v.lifecycle_status IN ('ready', 'completed', 'completed_partial') OR COALESCE(v.is_synced, false) THEN 'ready'
             ELSE 'processing' END AS status,
        v.storage_location, v.created_at, v.updated_at, v.retention_until AS expires_at,
        COALESCE(v.retention_source, '') AS retention_source,
        COALESCE(v.origin_type, '') AS origin_type, COALESCE(v.origin_id, '') AS origin_id,
        COALESCE(v.storage_cluster_id, v.origin_cluster_id, '') AS storage_cluster_id,
        v.has_thumbnails, v.duration AS duration_ms, v.tracks, v.sync_status, v.is_synced, v.is_finalized,
        COALESCE(v.description, '') AS description, COALESCE(v.error_message, '') AS error_message,
        COALESCE(v.thumbnail_serving_cluster_id, v.storage_cluster_id, v.origin_cluster_id, '') AS thumbnail_serving_cluster
    FROM commodore.vod_assets v
    LEFT JOIN commodore.streams st ON st.id = v.stream_id AND st.tenant_id = v.tenant_id
    WHERE v.tenant_id = $1 AND (v.library_visible = true OR COALESCE(v.origin_type, '') = 'dvr_chapter')
    UNION ALL
    SELECT
        'dvr', d.id::text, d.dvr_hash, COALESCE(d.playback_id, ''), COALESCE(d.stream_id::text, ''),
        COALESCE(st.title, ''), COALESCE(st.title, d.internal_name, d.dvr_hash), COALESCE(d.internal_name, ''),
        d.size_bytes,
        CASE WHEN d.retention_until IS NOT NULL AND d.retention_until <= NOW() THEN 'expired'
             WHEN d.lifecycle_status IN ('failed', 'aborted') OR d.sync_status IN ('failed', 'lost_local') THEN 'failed'
             WHEN d.lifecycle_status IN ('ready', 'completed', 'completed_partial') OR COALESCE(d.is_synced, false) THEN 'ready'
             ELSE 'processing' END,
        d.storage_location, d.created_at, d.updated_at, d.retention_until,
        COALESCE(d.retention_source, ''), '', '', COALESCE(d.storage_cluster_id, d.origin_cluster_id, ''),
        d.has_thumbnails, d.duration, d.tracks, d.sync_status, d.is_synced, d.is_finalized,
        '', COALESCE(d.error_message, ''),
        COALESCE(d.thumbnail_serving_cluster_id, d.storage_cluster_id, d.origin_cluster_id, '')
    FROM commodore.dvr_recordings d
    LEFT JOIN commodore.streams st ON st.id = d.stream_id AND st.tenant_id = d.tenant_id
    WHERE d.tenant_id = $1
    UNION ALL
    SELECT
        'clip', c.id::text, c.clip_hash, COALESCE(c.playback_id, ''), COALESCE(c.stream_id::text, ''),
        COALESCE(st.title, ''), COALESCE(NULLIF(c.title, ''), c.clip_hash), COALESCE(c.clip_mode, ''),
        c.size_bytes,
        CASE WHEN c.retention_until IS NOT NULL AND c.retention_until <= NOW() THEN 'expired'
             WHEN c.lifecycle_status IN ('failed', 'aborted') OR c.sync_status IN ('failed', 'lost_local') THEN 'failed'
             WHEN c.lifecycle_status IN ('ready', 'completed', 'completed_partial') OR COALESCE(c.is_synced, false) THEN 'ready'
             ELSE 'processing' END,
        c.storage_location, c.created_at, c.updated_at, c.retention_until,
        COALESCE(c.retention_source, ''), '', '', COALESCE(c.storage_cluster_id, c.origin_cluster_id, ''),
        c.has_thumbnails, c.duration, c.tracks, c.sync_status, c.is_synced, c.is_finalized,
        COALESCE(c.description, ''), COALESCE(c.error_message, ''),
        COALESCE(c.thumbnail_serving_cluster_id, c.storage_cluster_id, c.origin_cluster_id, '')
    FROM commodore.clips c
    LEFT JOIN commodore.streams st ON st.id = c.stream_id AND st.tenant_id = c.tenant_id
    WHERE c.tenant_id = $1
) artifacts`

func storageArtifactPredicates(arg StorageArtifactFilter, includeKinds bool) ([]string, []any) {
	args := []any{arg.TenantID}
	filters := []string{"TRUE"}
	add := func(predicate string, value any) {
		filters = append(filters, fmt.Sprintf(predicate, len(args)+1))
		args = append(args, value)
	}
	if arg.StreamID != "" {
		add("stream_id = $%d", arg.StreamID)
	}
	if includeKinds && len(arg.Kinds) > 0 {
		placeholders := make([]string, len(arg.Kinds))
		for i, kind := range arg.Kinds {
			args = append(args, kind)
			placeholders[i] = fmt.Sprintf("$%d", len(args))
		}
		filters = append(filters, "kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	if arg.Status != "" {
		add("status = $%d", arg.Status)
	}
	if arg.ArtifactHashes != nil {
		if len(arg.ArtifactHashes) == 0 {
			filters = append(filters, "FALSE")
		} else {
			add("artifact_hash = ANY($%d)", pq.Array(arg.ArtifactHashes))
		}
	} else if arg.ArtifactHash != "" {
		add("artifact_hash = $%d", arg.ArtifactHash)
	} else if arg.Search != "" {
		idx := len(args) + 1
		filters = append(filters, fmt.Sprintf("(LOWER(title) LIKE $%d OR LOWER(artifact_hash) LIKE $%d OR LOWER(stream_title) LIKE $%d OR LOWER(secondary_label) LIKE $%d)", idx, idx, idx, idx))
		args = append(args, "%"+strings.ToLower(arg.Search)+"%")
	}
	return filters, args
}

func (q *Queries) ListStorageArtifactCatalog(ctx context.Context, arg StorageArtifactFilter) (StorageArtifactCatalog, error) {
	filters, args := storageArtifactPredicates(arg, true)
	where := strings.Join(filters, " AND ")
	var out StorageArtifactCatalog
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM (%s WHERE %s) counted", storageArtifactBaseSQL, where), args...).Scan(&out.Total); err != nil {
		return out, err
	}

	facetFilters, facetArgs := storageArtifactPredicates(arg, false)
	facetRows, err := q.db.QueryContext(ctx, fmt.Sprintf("SELECT kind, COUNT(*) FROM (%s WHERE %s) f GROUP BY kind", storageArtifactBaseSQL, strings.Join(facetFilters, " AND ")), facetArgs...)
	if err != nil {
		return out, err
	}
	defer facetRows.Close()
	out.KindCounts = map[string]int32{}
	for facetRows.Next() {
		var kind string
		var count int32
		if scanErr := facetRows.Scan(&kind, &count); scanErr != nil {
			return out, scanErr
		}
		out.KindCounts[kind] = count
	}
	if rowsErr := facetRows.Err(); rowsErr != nil {
		return out, rowsErr
	}
	if closeErr := facetRows.Close(); closeErr != nil {
		return out, closeErr
	}

	sortField := map[string]bool{"created_at": true, "title": true, "kind": true, "size_bytes": true, "expires_at": true}
	if !sortField[arg.SortField] {
		return out, fmt.Errorf("unsupported storage artifact sort field %q", arg.SortField)
	}
	direction := strings.ToUpper(arg.SortDirection)
	if direction != "ASC" && direction != "DESC" {
		return out, fmt.Errorf("unsupported storage artifact sort direction %q", arg.SortDirection)
	}
	nulls := "NULLS LAST"
	if arg.SortField == "created_at" && direction == "DESC" {
		nulls = ""
	}
	dataArgs := append([]any(nil), args...)
	limitArg, offsetArg := len(dataArgs)+1, len(dataArgs)+2
	dataArgs = append(dataArgs, arg.Limit+1, arg.Offset)
	dataSQL := fmt.Sprintf("%s WHERE %s ORDER BY %s %s %s, created_at DESC, artifact_hash DESC LIMIT $%d OFFSET $%d",
		storageArtifactBaseSQL, where, arg.SortField, direction, nulls, limitArg, offsetArg)
	rows, err := q.db.QueryContext(ctx, dataSQL, dataArgs...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var row StorageArtifactRow
		if err := rows.Scan(
			&row.Kind, &row.ID, &row.ArtifactHash, &row.PlaybackID, &row.StreamID, &row.StreamTitle, &row.Title, &row.SecondaryLabel,
			&row.SizeBytes, &row.Status, &row.StorageLocation, &row.CreatedAt, &row.UpdatedAt, &row.ExpiresAt,
			&row.RetentionSource, &row.OriginType, &row.OriginID, &row.StorageClusterID, &row.HasThumbnails, &row.DurationMs,
			&row.Tracks, &row.SyncStatus, &row.IsSynced, &row.IsFinalized, &row.Description, &row.ErrorMessage, &row.ThumbnailServingCluster,
		); err != nil {
			return out, err
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}
