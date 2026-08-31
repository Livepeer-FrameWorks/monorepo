package commodoredb

import (
	"context"
	"fmt"

	"github.com/lib/pq"
)

const renewActiveIngestPlacementsSQL = `
UPDATE commodore.streams s
SET active_ingest_cluster_id = t.cluster_id,
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = t.claim_token
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[]) AS t(tenant_id, internal_name, claim_token, cluster_id)
WHERE s.tenant_id = t.tenant_id
  AND s.internal_name = t.internal_name
  AND s.deleted_at IS NULL
  AND (
      s.active_ingest_cluster_id IS NULL
      OR s.active_ingest_cluster_id = ''
      OR s.active_ingest_cluster_updated_at IS NULL
      OR s.active_ingest_cluster_updated_at < NOW() - ($5::bigint * INTERVAL '1 second')
      OR (s.active_ingest_cluster_id = t.cluster_id AND s.active_ingest_claim_id = t.claim_token)
  )`

const releaseActiveIngestPlacementsSQL = `
UPDATE commodore.streams s
SET active_ingest_cluster_id = NULL,
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = NULL,
    updated_at = NOW()
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[]) AS t(tenant_id, internal_name, claim_token, cluster_id)
WHERE s.tenant_id = t.tenant_id
  AND s.internal_name = t.internal_name
  AND s.deleted_at IS NULL
  AND s.active_ingest_cluster_id = t.cluster_id
  AND s.active_ingest_claim_id = t.claim_token`

const listRefusedActiveIngestRenewalsSQL = `
SELECT t.tenant_id::text, t.internal_name, t.claim_token, t.cluster_id
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[]) AS t(tenant_id, internal_name, claim_token, cluster_id)
WHERE NOT EXISTS (
    SELECT 1 FROM commodore.streams s
    WHERE s.tenant_id = t.tenant_id AND s.internal_name = t.internal_name
      AND s.deleted_at IS NULL
      AND s.active_ingest_cluster_id = t.cluster_id
      AND s.active_ingest_claim_id = t.claim_token
      AND s.active_ingest_cluster_updated_at >= NOW() - ($5::bigint * INTERVAL '1 second')
)`

const listRefusedActiveIngestReleasesSQL = `
SELECT t.tenant_id::text, t.internal_name, t.claim_token, t.cluster_id
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[]) AS t(tenant_id, internal_name, claim_token, cluster_id)
WHERE EXISTS (
    SELECT 1 FROM commodore.streams s
    WHERE s.tenant_id = t.tenant_id AND s.internal_name = t.internal_name
      AND s.deleted_at IS NULL
      AND s.active_ingest_cluster_id = t.cluster_id
      AND s.active_ingest_claim_id = t.claim_token
)`

type ActiveIngestPlacementBatchParams struct {
	TenantIDs, InternalNames, ClaimTokens, ClusterIDs []string
	LeaseSeconds                                      int64
	Renew                                             bool
}

type ActiveIngestPlacementRefusal struct {
	TenantID, InternalName, ClaimToken, ClusterID string
}

// ApplyActiveIngestPlacementBatch is a typed boundary for PostgreSQL's
// multi-array unnest, which sqlc does not currently analyze.
func (q *Queries) ApplyActiveIngestPlacementBatch(ctx context.Context, arg ActiveIngestPlacementBatchParams) (int64, []ActiveIngestPlacementRefusal, error) {
	updateSQL := releaseActiveIngestPlacementsSQL
	verifySQL := listRefusedActiveIngestReleasesSQL
	updateArgs := []any{pq.Array(arg.TenantIDs), pq.Array(arg.InternalNames), pq.Array(arg.ClaimTokens), pq.Array(arg.ClusterIDs)}
	verifyArgs := append([]any(nil), updateArgs...)
	if arg.Renew {
		updateSQL = renewActiveIngestPlacementsSQL
		verifySQL = listRefusedActiveIngestRenewalsSQL
		updateArgs = append(updateArgs, arg.LeaseSeconds)
		verifyArgs = append(verifyArgs, arg.LeaseSeconds)
	}
	result, err := q.db.ExecContext(ctx, updateSQL, updateArgs...)
	if err != nil {
		return 0, nil, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil, err
	}
	rows, err := q.db.QueryContext(ctx, verifySQL, verifyArgs...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	var refused []ActiveIngestPlacementRefusal
	for rows.Next() {
		var row ActiveIngestPlacementRefusal
		if err := rows.Scan(&row.TenantID, &row.InternalName, &row.ClaimToken, &row.ClusterID); err != nil {
			return 0, nil, err
		}
		refused = append(refused, row)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, err
	}
	if err := rows.Close(); err != nil {
		return 0, nil, fmt.Errorf("close placement verification: %w", err)
	}
	return affected, refused, nil
}
