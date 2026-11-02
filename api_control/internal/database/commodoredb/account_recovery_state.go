package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"
)

// RetiredIngestCell is an operator record that a cell was permanently removed.
type RetiredIngestCell struct {
	CellID    string
	RetiredBy string
	Reason    string
	RetiredAt time.Time
}

const retireIngestCell = `INSERT INTO commodore.retired_ingest_cells (cell_id, retired_by, reason)
VALUES ($1, $2, $3)
ON CONFLICT (cell_id) DO NOTHING
RETURNING cell_id, retired_by, reason, retired_at`

const getRetiredIngestCell = `SELECT cell_id, retired_by, reason, retired_at
FROM commodore.retired_ingest_cells WHERE cell_id = $1`

// RetireIngestCell records the retirement once. A repeated call returns the
// first record and created=false.
func (q *Queries) RetireIngestCell(ctx context.Context, cellID, retiredBy, reason string) (RetiredIngestCell, bool, error) {
	var cell RetiredIngestCell
	err := q.db.QueryRowContext(ctx, retireIngestCell, cellID, retiredBy, reason).Scan(&cell.CellID, &cell.RetiredBy, &cell.Reason, &cell.RetiredAt)
	if err == nil {
		return cell, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetiredIngestCell{}, false, err
	}
	err = q.db.QueryRowContext(ctx, getRetiredIngestCell, cellID).Scan(&cell.CellID, &cell.RetiredBy, &cell.Reason, &cell.RetiredAt)
	return cell, false, err
}

const listRetiredIngestCells = `SELECT cell_id FROM commodore.retired_ingest_cells
WHERE cell_id = ANY($1::text[]) ORDER BY cell_id`

// ListRetiredIngestCells returns the retired subset of cells.
func (q *Queries) ListRetiredIngestCells(ctx context.Context, cells []string) ([]string, error) {
	if len(cells) == 0 {
		return nil, nil
	}
	rows, err := q.db.QueryContext(ctx, listRetiredIngestCells, pq.Array(cells))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var retired []string
	for rows.Next() {
		var cell string
		if err := rows.Scan(&cell); err != nil {
			return nil, err
		}
		retired = append(retired, cell)
	}
	return retired, rows.Err()
}

// PendingIngestStopRow is one undelivered session-stop obligation.
type PendingIngestStopRow struct {
	TargetKey     string
	TargetKind    string
	TenantID      string
	Status        string
	Attempts      int32
	PendingSince  time.Time
	NextAttemptAt time.Time
	LastError     string
}

// The status predicate matches the partial due index, so the read never scans
// renewal rows of other lanes.
const listPendingIngestStops = `SELECT target_key, target_kind, tenant_id::text, status, attempts,
       pending_since, next_attempt_at, COALESCE(last_error, '')
FROM commodore.media_authority_refresh_obligations
WHERE lane = 'ingest_revocation' AND status IN ('pending', 'processing')
  AND ($1::text = '' OR tenant_id = NULLIF($1::text, '')::uuid)
ORDER BY pending_since, target_key
LIMIT $2`

// ListPendingIngestStops lists undelivered session stops, oldest first, for
// one tenant or, with an empty tenantID, for every tenant.
func (q *Queries) ListPendingIngestStops(ctx context.Context, tenantID string, limit int32) ([]PendingIngestStopRow, error) {
	rows, err := q.db.QueryContext(ctx, listPendingIngestStops, tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingIngestStopRow
	for rows.Next() {
		var row PendingIngestStopRow
		if err := rows.Scan(&row.TargetKey, &row.TargetKind, &row.TenantID, &row.Status, &row.Attempts, &row.PendingSince, &row.NextAttemptAt, &row.LastError); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

const retryIngestStopsNow = `UPDATE commodore.media_authority_refresh_obligations
SET next_attempt_at = NOW(), updated_at = NOW()
WHERE lane = 'ingest_revocation' AND status = 'pending'
  AND target_key = ANY($1::text[]) AND next_attempt_at > NOW()`

// RetryIngestStopsNow ends the backoff of pending session stops. A stop being
// delivered keeps its lease and settles on its own.
func (q *Queries) RetryIngestStopsNow(ctx context.Context, targetKeys []string) (int64, error) {
	if len(targetKeys) == 0 {
		return 0, nil
	}
	result, err := q.db.ExecContext(ctx, retryIngestStopsNow, pq.Array(targetKeys))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// TenantAccountClosureState is the closure and Quartermaster synchronization
// record of one tenant.
type TenantAccountClosureState struct {
	ClosedAt              sql.NullTime
	ClosedBy              string
	QuartermasterClosedAt sql.NullTime
	ClosureAttemptedAt    sql.NullTime
	ClosureRetryAfter     sql.NullTime
	ClosureError          string
	ClosureFailures       int32
}

const getTenantAccountClosureState = `SELECT closed_at, closed_by, quartermaster_closed_at, closure_attempted_at,
       closure_retry_after, COALESCE(closure_error, ''), closure_failures
FROM commodore.tenant_account_states WHERE tenant_id = $1::uuid`

// GetTenantAccountClosureState returns sql.ErrNoRows for a tenant that never
// had a lifecycle change.
func (q *Queries) GetTenantAccountClosureState(ctx context.Context, tenantID string) (TenantAccountClosureState, error) {
	var state TenantAccountClosureState
	err := q.db.QueryRowContext(ctx, getTenantAccountClosureState, tenantID).Scan(&state.ClosedAt, &state.ClosedBy, &state.QuartermasterClosedAt, &state.ClosureAttemptedAt, &state.ClosureRetryAfter, &state.ClosureError, &state.ClosureFailures)
	return state, err
}
