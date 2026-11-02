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

const unretireIngestCell = `DELETE FROM commodore.retired_ingest_cells WHERE cell_id = $1
RETURNING cell_id, retired_by, reason, retired_at`

// UnretireIngestCell removes the retirement record and returns it, or
// deleted=false when the cell had none.
func (q *Queries) UnretireIngestCell(ctx context.Context, cellID string) (RetiredIngestCell, bool, error) {
	var cell RetiredIngestCell
	err := q.db.QueryRowContext(ctx, unretireIngestCell, cellID).Scan(&cell.CellID, &cell.RetiredBy, &cell.Reason, &cell.RetiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RetiredIngestCell{CellID: cellID}, false, nil
	}
	if err != nil {
		return RetiredIngestCell{}, false, err
	}
	return cell, true, nil
}

// PendingIngestStopRow is one undelivered session-stop obligation with the
// cells it is delivered to and the retired subset of them.
type PendingIngestStopRow struct {
	TargetKey     string
	TargetKind    string
	TenantID      string
	Status        string
	Attempts      int32
	PendingSince  time.Time
	NextAttemptAt time.Time
	LastError     string
	Cells         []string
	RetiredCells  []string
}

// pendingIngestStopsCTE selects the undelivered session stops: $1 narrows them
// to one tenant (an empty $1 reads every tenant) and $2 is the live-stream target key
// prefix the stop's stream ID follows. The status predicate matches the
// partial due index, so the read never scans renewal rows of other lanes.
const pendingIngestStopsCTE = `pending AS (
  SELECT target_key, target_kind, tenant_id, status, attempts, pending_since, next_attempt_at,
         COALESCE(last_error, '') AS last_error,
         CASE WHEN target_kind = 'live_stream' AND left(target_key, length($2::text)) = $2::text
              THEN substr(target_key, length($2::text) + 1) ELSE '' END AS stream_id
  FROM commodore.media_authority_refresh_obligations
  WHERE lane = 'ingest_revocation' AND status IN ('pending', 'processing')
    AND ($1::text = '' OR tenant_id = NULLIF($1::text, '')::uuid)
  ORDER BY pending_since, target_key`

// ingestStopCellsCTE lists, per pending stop, the cells it is delivered to:
// the set ListAccountIngestCells returns for the stop's tenant and stream,
// which stopAccountIngest contacts.
const ingestStopCellsCTE = `stop_streams AS (
  SELECT pending.target_key, stream.id, stream.active_ingest_cluster_id, stream.last_ingest_cluster_id
  FROM pending
  JOIN commodore.streams stream ON stream.tenant_id = pending.tenant_id
   AND (pending.stream_id = '' OR stream.id::text = pending.stream_id)
), stop_cells AS (
  SELECT target_key, active_ingest_cluster_id::text AS cell_id FROM stop_streams
  UNION SELECT target_key, last_ingest_cluster_id::text FROM stop_streams
  UNION
  SELECT stop_streams.target_key, target.cell_id::text
  FROM stop_streams
  JOIN commodore.media_authority_targets target
    ON target.authority_kind = 'media_object'
   AND target.authority_id = 'live_stream:' || stop_streams.id::text
  UNION
  SELECT pending.target_key, target.cell_id::text
  FROM pending
  JOIN commodore.media_authority_targets target
    ON pending.stream_id = ''
   AND target.authority_kind = 'tenant'
   AND target.authority_id = pending.tenant_id::text
), cells AS (
  SELECT target_key, cell_id FROM stop_cells WHERE cell_id IS NOT NULL AND btrim(cell_id) <> ''
)`

const listPendingIngestStops = `WITH ` + pendingIngestStopsCTE + `
  LIMIT $3
), ` + ingestStopCellsCTE + `
SELECT pending.target_key, pending.target_kind, pending.tenant_id::text, pending.status, pending.attempts,
       pending.pending_since, pending.next_attempt_at, pending.last_error,
       ARRAY(SELECT cells.cell_id FROM cells WHERE cells.target_key = pending.target_key ORDER BY cells.cell_id),
       ARRAY(SELECT cells.cell_id FROM cells
             JOIN commodore.retired_ingest_cells retired ON retired.cell_id = cells.cell_id
             WHERE cells.target_key = pending.target_key ORDER BY cells.cell_id)
FROM pending
ORDER BY pending.pending_since, pending.target_key`

// ListPendingIngestStops lists one tenant's undelivered session stops, oldest
// first, each with its cells, in one statement.
func (q *Queries) ListPendingIngestStops(ctx context.Context, tenantID string, limit int32) ([]PendingIngestStopRow, error) {
	rows, err := q.db.QueryContext(ctx, listPendingIngestStops, tenantID, LiveStreamMediaAuthorityTarget("").Key, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingIngestStopRow
	for rows.Next() {
		var row PendingIngestStopRow
		if err := rows.Scan(&row.TargetKey, &row.TargetKind, &row.TenantID, &row.Status, &row.Attempts, &row.PendingSince, &row.NextAttemptAt, &row.LastError, pq.Array(&row.Cells), pq.Array(&row.RetiredCells)); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// A stop being delivered keeps its lease and settles on its own; only waiting
// stops have their backoff ended.
const retryIngestStopsForCell = `WITH ` + pendingIngestStopsCTE + `
), ` + ingestStopCellsCTE + `, affected AS (
  SELECT DISTINCT target_key FROM cells WHERE cell_id = $3::text
), retried AS (
  UPDATE commodore.media_authority_refresh_obligations obligation
  SET next_attempt_at = NOW(), updated_at = NOW()
  FROM affected
  WHERE obligation.lane = 'ingest_revocation' AND obligation.target_key = affected.target_key
    AND obligation.status = 'pending' AND obligation.next_attempt_at > NOW()
  RETURNING obligation.target_key
)
SELECT (SELECT count(*) FROM affected), (SELECT count(*) FROM retried)`

// RetryIngestStopsForCell finds every pending session stop delivered to cell
// and ends the backoff of those waiting, in one statement. matched counts the
// stops that include the cell; retried counts those rescheduled.
func (q *Queries) RetryIngestStopsForCell(ctx context.Context, cellID string) (matched, retried int64, err error) {
	err = q.db.QueryRowContext(ctx, retryIngestStopsForCell, "", LiveStreamMediaAuthorityTarget("").Key, cellID).Scan(&matched, &retried)
	return matched, retried, err
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
