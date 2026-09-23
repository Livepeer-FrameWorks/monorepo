package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// NodeFingerprintBindingRow is one node_fingerprints row as the operator
// listing exposes it. The identity key and seen IPs are deliberately absent.
type NodeFingerprintBindingRow struct {
	ID, NodeID, TenantID, ClusterID string
	MachineSHA256, MACsSHA256       string
	HasIdentityKey                  bool
	FirstSeen, LastSeen             sql.NullTime
	SortTime                        time.Time
	MachineDuplicates, MACsDupes    int32
}

type NodeFingerprintBindingFilter struct {
	ClusterID      string
	DuplicatesOnly bool
	CursorTime     *time.Time
	CursorID       string
	Backward       bool
	Limit          int
}

// nodeFingerprintBindingsCTE counts duplicates over every binding before any
// filter, because uq_qm_fingerprints_machine and uq_qm_fingerprints_macs are
// global. A hash counts only when the index predicate includes it: not NULL and
// not blank after btrim; rows are grouped by the raw column value, as the index
// compares them.
const nodeFingerprintBindingsCTE = `
WITH bindings AS (
	SELECT nf.id,
	       nf.node_id,
	       COALESCE(nf.tenant_id::text, '') AS tenant_id,
	       COALESCE(n.cluster_id, '') AS cluster_id,
	       COALESCE(nf.fingerprint_machine_sha256, '') AS machine_sha256,
	       COALESCE(nf.fingerprint_macs_sha256, '') AS macs_sha256,
	       nf.node_identity_public_key_ed25519 IS NOT NULL AS has_identity_key,
	       nf.first_seen,
	       nf.last_seen,
	       COALESCE(nf.first_seen, 'epoch'::timestamp) AS sort_time,
	       CASE WHEN nf.fingerprint_machine_sha256 IS NOT NULL AND btrim(nf.fingerprint_machine_sha256) <> ''
	            THEN COUNT(*) OVER (PARTITION BY nf.fingerprint_machine_sha256) ELSE 0 END AS machine_count,
	       CASE WHEN nf.fingerprint_macs_sha256 IS NOT NULL AND btrim(nf.fingerprint_macs_sha256) <> ''
	            THEN COUNT(*) OVER (PARTITION BY nf.fingerprint_macs_sha256) ELSE 0 END AS macs_count
	FROM quartermaster.node_fingerprints nf
	LEFT JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
)`

// ListNodeFingerprintBindingsPage reads bindings across every tenant for the
// platform-operator fingerprint listing. Duplicate counts are reported only
// when greater than one; a unique hash reads as 0.
func (q *Queries) ListNodeFingerprintBindingsPage(ctx context.Context, filter NodeFingerprintBindingFilter) ([]NodeFingerprintBindingRow, int32, error) {
	where := `WHERE ($1::text = '' OR b.cluster_id = $1::text)
	  AND (NOT $2::boolean OR b.machine_count > 1 OR b.macs_count > 1)`
	args := []any{filter.ClusterID, filter.DuplicatesOnly}

	var total int32
	if err := q.db.QueryRowContext(ctx, nodeFingerprintBindingsCTE+`
		SELECT COUNT(*) FROM bindings b `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	direction := "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (b.sort_time, b.id) %s ($3::timestamp, $4::uuid)", op)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	args = append(args, filter.Limit)
	query := nodeFingerprintBindingsCTE + fmt.Sprintf(`
		SELECT b.id::text, b.node_id, b.tenant_id, b.cluster_id, b.machine_sha256, b.macs_sha256,
		       b.has_identity_key, b.first_seen, b.last_seen, b.sort_time,
		       CASE WHEN b.machine_count > 1 THEN b.machine_count ELSE 0 END::int,
		       CASE WHEN b.macs_count > 1 THEN b.macs_count ELSE 0 END::int
		FROM bindings b %s
		ORDER BY b.sort_time %s, b.id %s
		LIMIT $%d`, where, direction, direction, len(args))
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []NodeFingerprintBindingRow
	for rows.Next() {
		var row NodeFingerprintBindingRow
		if err := rows.Scan(&row.ID, &row.NodeID, &row.TenantID, &row.ClusterID, &row.MachineSHA256, &row.MACsSHA256,
			&row.HasIdentityKey, &row.FirstSeen, &row.LastSeen, &row.SortTime, &row.MachineDuplicates, &row.MACsDupes); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// UnboundNodeFingerprint is what the deleted binding pointed at: its tenant
// and the cluster of its node, each empty when absent.
type UnboundNodeFingerprint struct {
	TenantID, ClusterID string
}

// DeleteNodeFingerprintBinding deletes the binding only when fingerprintID
// still belongs to nodeID, so an operator acting on a stale listing cannot
// remove a different node's binding. sql.ErrNoRows means nothing matched.
func (q *Queries) DeleteNodeFingerprintBinding(ctx context.Context, fingerprintID, nodeID string) (UnboundNodeFingerprint, error) {
	var out UnboundNodeFingerprint
	err := q.db.QueryRowContext(ctx, `
		WITH deleted AS (
			DELETE FROM quartermaster.node_fingerprints
			WHERE id = $1::uuid AND node_id = $2
			RETURNING tenant_id, node_id
		)
		SELECT COALESCE(d.tenant_id::text, ''), COALESCE(n.cluster_id, '')
		FROM deleted d
		LEFT JOIN quartermaster.infrastructure_nodes n ON n.node_id = d.node_id
	`, fingerprintID, nodeID).Scan(&out.TenantID, &out.ClusterID)
	return out, err
}
