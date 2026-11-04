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
    active_ingest_claim_id = t.claim_token,
    active_ingest_node_id = CASE WHEN s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token THEN s.active_ingest_node_id ELSE NULL END,
    active_ingest_control_cell_id = CASE WHEN s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token THEN s.active_ingest_control_cell_id ELSE NULL END
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[], $6::boolean[]) AS t(tenant_id, internal_name, claim_token, cluster_id, strict_owner)
WHERE s.tenant_id = t.tenant_id
  AND s.internal_name = t.internal_name
  AND s.deleted_at IS NULL
  AND (NOT t.strict_owner OR (s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token))
  AND (
      s.active_ingest_cluster_id IS NULL
      OR s.active_ingest_cluster_id = ''
      OR s.active_ingest_cluster_updated_at IS NULL
      OR s.active_ingest_cluster_updated_at < NOW() - ($5::bigint * INTERVAL '1 second')
      OR (s.active_ingest_cluster_id = t.cluster_id AND s.active_ingest_claim_id = t.claim_token)
  )`

// Execution-aware outage adoption serializes against abandonment in its caller.
// Missing metadata retains the legacy client's established reconciliation rules.
const renewExecutedIngestPlacementsSQL = `
UPDATE commodore.streams s
SET active_ingest_cluster_id=t.cluster_id,active_ingest_cluster_updated_at=NOW(),active_ingest_claim_id=t.claim_token,
 active_ingest_node_id=CASE WHEN t.node_id<>'' THEN t.node_id WHEN s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token THEN s.active_ingest_node_id ELSE NULL END,
 active_ingest_control_cell_id=CASE WHEN t.cell_id<>'' THEN t.cell_id WHEN s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token THEN s.active_ingest_control_cell_id ELSE NULL END
FROM unnest($1::uuid[],$2::text[],$3::text[],$4::text[],$6::boolean[],$7::text[],$8::text[]) AS t(tenant_id,internal_name,claim_token,cluster_id,strict_owner,node_id,cell_id)
WHERE s.tenant_id=t.tenant_id AND s.internal_name=t.internal_name AND s.deleted_at IS NULL
 AND (t.node_id='' OR NOT EXISTS(SELECT 1 FROM commodore.publisher_lease_abandonments marker WHERE marker.control_cell_id=t.cell_id AND marker.node_id=t.node_id AND marker.execution_token=t.claim_token))
 AND (NOT t.strict_owner OR (s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token
   AND (t.node_id='' OR (s.active_ingest_node_id IS NULL AND s.active_ingest_control_cell_id IS NULL) OR (s.active_ingest_node_id=t.node_id AND s.active_ingest_control_cell_id=t.cell_id))))
 AND (s.active_ingest_cluster_id IS NULL OR s.active_ingest_cluster_id='' OR s.active_ingest_cluster_updated_at IS NULL
   OR s.active_ingest_cluster_updated_at < NOW()-($5::bigint*INTERVAL '1 second')
   OR (s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token
     AND (t.node_id='' OR (s.active_ingest_node_id IS NULL AND s.active_ingest_control_cell_id IS NULL) OR (s.active_ingest_node_id=t.node_id AND s.active_ingest_control_cell_id=t.cell_id))))`

const listRefusedExecutedIngestRenewalsSQL = `
SELECT t.tenant_id::text,t.internal_name,t.claim_token,t.cluster_id
FROM unnest($1::uuid[],$2::text[],$3::text[],$4::text[],$6::text[],$7::text[]) AS t(tenant_id,internal_name,claim_token,cluster_id,node_id,cell_id)
WHERE NOT EXISTS(SELECT 1 FROM commodore.streams s WHERE s.tenant_id=t.tenant_id AND s.internal_name=t.internal_name AND s.deleted_at IS NULL
 AND s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token
 AND s.active_ingest_cluster_updated_at>=NOW()-($5::bigint*INTERVAL '1 second')
 AND (t.node_id='' OR (s.active_ingest_node_id=t.node_id AND s.active_ingest_control_cell_id=t.cell_id)))`

const releaseActiveIngestPlacementsSQL = `
UPDATE commodore.streams s
SET active_ingest_cluster_id = NULL,
    active_ingest_cluster_updated_at = NOW(),
    active_ingest_claim_id = NULL,
    active_ingest_node_id = NULL,
    active_ingest_control_cell_id = NULL,
    updated_at = NOW()
FROM unnest($1::uuid[], $2::text[], $3::text[], $4::text[]) AS t(tenant_id, internal_name, claim_token, cluster_id)
WHERE s.tenant_id = t.tenant_id
  AND s.internal_name = t.internal_name
  AND s.deleted_at IS NULL
  AND s.active_ingest_cluster_id = t.cluster_id
  AND s.active_ingest_claim_id = t.claim_token`

const recordReleasedPublisherExecutionsSQL = `
INSERT INTO commodore.publisher_lease_abandonments(control_cell_id,node_id,execution_token)
SELECT cell_id,node_id,claim_token FROM unnest($1::text[],$2::text[],$3::text[]) AS executions(cell_id,node_id,claim_token)
WHERE cell_id<>'' AND node_id<>''
ON CONFLICT(control_cell_id,node_id,execution_token) DO NOTHING`

const releaseExecutedIngestPlacementsSQL = `
UPDATE commodore.streams s
SET active_ingest_cluster_id=NULL,active_ingest_cluster_updated_at=NOW(),active_ingest_claim_id=NULL,
 active_ingest_node_id=NULL,active_ingest_control_cell_id=NULL,updated_at=NOW()
FROM unnest($1::uuid[],$2::text[],$3::text[],$4::text[],$5::text[],$6::text[]) AS t(tenant_id,internal_name,claim_token,cluster_id,node_id,cell_id)
WHERE s.tenant_id=t.tenant_id AND s.internal_name=t.internal_name AND s.deleted_at IS NULL
 AND s.active_ingest_cluster_id=t.cluster_id AND s.active_ingest_claim_id=t.claim_token
 AND (t.node_id='' OR (s.active_ingest_node_id IS NULL AND s.active_ingest_control_cell_id IS NULL) OR (s.active_ingest_node_id=t.node_id AND s.active_ingest_control_cell_id=t.cell_id))`

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
	StrictOwners                                      []bool
	NodeIDs, ControlCellIDs                           []string
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
	executed := false
	for _, nodeID := range arg.NodeIDs {
		executed = executed || nodeID != ""
	}
	if executed && (len(arg.NodeIDs) != len(arg.TenantIDs) || len(arg.ControlCellIDs) != len(arg.TenantIDs)) {
		return 0, nil, fmt.Errorf("publisher execution metadata does not match streams")
	}
	if !arg.Renew && executed {
		if _, err := q.db.ExecContext(ctx, recordReleasedPublisherExecutionsSQL, pq.Array(arg.ControlCellIDs), pq.Array(arg.NodeIDs), pq.Array(arg.ClaimTokens)); err != nil {
			return 0, nil, err
		}
		updateSQL = releaseExecutedIngestPlacementsSQL
		updateArgs = append(updateArgs, pq.Array(arg.NodeIDs), pq.Array(arg.ControlCellIDs))
	}
	if arg.Renew {
		updateSQL = renewActiveIngestPlacementsSQL
		verifySQL = listRefusedActiveIngestRenewalsSQL
		strictOwners := arg.StrictOwners
		if len(strictOwners) == 0 {
			strictOwners = make([]bool, len(arg.TenantIDs))
		}
		if len(strictOwners) != len(arg.TenantIDs) {
			return 0, nil, fmt.Errorf("publisher lease renewal owner flags do not match streams")
		}
		updateArgs = append(updateArgs, arg.LeaseSeconds, pq.Array(strictOwners))
		verifyArgs = append(verifyArgs, arg.LeaseSeconds)
		if executed {
			if len(arg.NodeIDs) != len(arg.TenantIDs) || len(arg.ControlCellIDs) != len(arg.TenantIDs) {
				return 0, nil, fmt.Errorf("publisher execution metadata does not match streams")
			}
			updateSQL, verifySQL = renewExecutedIngestPlacementsSQL, listRefusedExecutedIngestRenewalsSQL
			updateArgs = append(updateArgs, pq.Array(arg.NodeIDs), pq.Array(arg.ControlCellIDs))
			verifyArgs = append(verifyArgs, pq.Array(arg.NodeIDs), pq.Array(arg.ControlCellIDs))
		}
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
