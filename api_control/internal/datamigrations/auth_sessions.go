package datamigrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const AuthSessionsID = "commodore_auth_sessions_v0_3_15"

// AuthSessionsBatchSQL binds each live head and its predecessors to one owner.
// A head written by an old issuer inherits any session already on its chain.
// Hour intervals keep the maximum age fixed across timezone and DST changes.
// UTC timestamp cutoffs match the legacy column so expiry ranges reach storage.
const AuthSessionsBatchSQL = `
WITH RECURSIVE heads AS (
    SELECT id, tenant_id, user_id, created_at, expires_at
    FROM commodore.refresh_tokens
    WHERE tenant_id = $1 AND session_id IS NULL
      AND COALESCE(revoked, false) = false AND expires_at > (NOW() AT TIME ZONE 'UTC')
    ORDER BY expires_at, id LIMIT $2 FOR UPDATE SKIP LOCKED
), chain AS (
    SELECT h.id AS head_id, r.id AS token_id, r.tenant_id, r.user_id, r.created_at, r.session_id
    FROM heads h JOIN commodore.refresh_tokens r
      ON r.tenant_id = h.tenant_id AND r.user_id = h.user_id AND r.id = h.id
    UNION
    SELECT c.head_id, p.id, p.tenant_id, p.user_id, p.created_at, p.session_id
    FROM chain c JOIN commodore.refresh_tokens p
      ON p.tenant_id = c.tenant_id AND p.user_id = c.user_id AND p.replaced_by = c.token_id
), owners AS (
    SELECT head_id, COALESCE(MIN(session_id::text)::uuid, head_id) AS session_id,
           MIN(COALESCE(created_at AT TIME ZONE 'UTC', NOW())) AS created_at
    FROM chain GROUP BY head_id
), inserted AS (
    INSERT INTO commodore.auth_sessions (
        id, tenant_id, user_id, auth_method, client_id, device_label,
        created_at, last_seen_at, idle_expires_at, absolute_expires_at
    )
    SELECT o.session_id, h.tenant_id, h.user_id, 'unknown', 'unknown',
           'Signed in before sessions were tracked', o.created_at,
           COALESCE(h.created_at AT TIME ZONE 'UTC', NOW()),
           LEAST(h.expires_at AT TIME ZONE 'UTC', o.created_at + INTERVAL '2160 hours'),
           o.created_at + INTERVAL '2160 hours'
    FROM owners o JOIN heads h ON h.id = o.head_id
    ON CONFLICT (id) DO NOTHING RETURNING id, user_id
), updated AS (
    UPDATE commodore.refresh_tokens r SET session_id = o.session_id
    FROM chain c JOIN owners o ON o.head_id = c.head_id
    JOIN (SELECT id, user_id FROM inserted UNION ALL
          SELECT existing.id, existing.user_id
          FROM owners owned JOIN heads head ON head.id = owned.head_id
          JOIN commodore.auth_sessions existing
            ON existing.tenant_id = head.tenant_id AND existing.user_id = head.user_id
           AND existing.id = owned.session_id) s
      ON s.id = o.session_id AND s.user_id = c.user_id
    WHERE r.tenant_id = $1 AND r.tenant_id = c.tenant_id AND r.user_id = c.user_id
      AND r.id = c.token_id AND r.session_id IS NULL
    RETURNING r.id
)
SELECT (SELECT COUNT(*) FROM heads), (SELECT COUNT(*) FROM updated)`

func registerAuthSessions() {
	datamigrate.Register(datamigrate.Migration{
		ID: AuthSessionsID, Service: "commodore", IntroducedIn: "v0.3.15",
		RequiredBeforePhase: "contract",
		Description:         "attach legacy refresh-token chains to bounded auth sessions",
		Run:                 runAuthSessions, Verify: verifyAuthSessions,
	})
}

func runAuthSessions(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	if !opts.Scope.IsZero() {
		return datamigrate.Progress{}, fmt.Errorf("auth session backfill runs across all tenants in bounded tenant batches")
	}
	size := opts.BatchSize
	if size <= 0 || size > 1000 {
		size = 100
	}
	// Yugabyte can prefer the tenant-leading pending index even for a global
	// ordered probe. Pin the expiry range so LIMIT bounds physical reads.
	// PostgreSQL ignores the hint comment and uses its normal range plan.
	if opts.DryRun {
		rows, err := db.QueryContext(ctx, `SELECT /*+ IndexOnlyScan(refresh_tokens idx_refresh_tokens_pending_session_range) */ id FROM commodore.refresh_tokens
            WHERE session_id IS NULL AND COALESCE(revoked, false) = false AND expires_at > (NOW() AT TIME ZONE 'UTC')
            ORDER BY expires_at, tenant_id, id LIMIT $1`, size)
		if err != nil {
			return datamigrate.Progress{}, err
		}
		defer rows.Close()
		var scanned int64
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return datamigrate.Progress{}, err
			}
			scanned++
		}
		if err := rows.Err(); err != nil {
			return datamigrate.Progress{}, err
		}
		if err := rows.Err(); err != nil {
			return datamigrate.Progress{}, err
		}
		if scanned == 0 {
			return previewBackfilledSessionCaps(ctx, db, size)
		}
		return datamigrate.Progress{Scanned: scanned, Done: scanned < int64(size),
			Summary: []string{fmt.Sprintf("sampled at most %d pending refresh heads; a full batch does not establish completion", size)}}, nil
	}
	// Discovery is read-only across tenants; each mutation locks one tenant.
	var tenantID string
	err := db.QueryRowContext(ctx, `SELECT /*+ IndexOnlyScan(refresh_tokens idx_refresh_tokens_pending_session_range) */ tenant_id FROM commodore.refresh_tokens
		WHERE session_id IS NULL AND COALESCE(revoked, false) = false AND expires_at > (NOW() AT TIME ZONE 'UTC')
        ORDER BY expires_at, tenant_id, id LIMIT 1`).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return normalizeBackfilledSessionCaps(ctx, db, opts.Checkpoint)
	}
	if err != nil {
		return datamigrate.Progress{}, err
	}
	var scanned, changed int64
	if err := db.QueryRowContext(ctx, AuthSessionsBatchSQL, tenantID, size).Scan(&scanned, &changed); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill auth sessions: %w", err)
	}
	return datamigrate.Progress{Scanned: scanned, Changed: changed, Done: false}, nil
}

func verifyAuthSessions(ctx context.Context, db datamigrate.DB) error {
	// Keep the global completion probe ordered: EXISTS can discard ordering
	// and choose an owner-leading index instead of the pending-head range.
	var pendingID string
	err := db.QueryRowContext(ctx, `SELECT /*+ IndexOnlyScan(refresh_tokens idx_refresh_tokens_pending_session_range) */ id FROM commodore.refresh_tokens
        WHERE session_id IS NULL AND COALESCE(revoked, false) = false AND expires_at > (NOW() AT TIME ZONE 'UTC')
        ORDER BY expires_at, tenant_id, id LIMIT 1`).Scan(&pendingID)
	if err == nil {
		return fmt.Errorf("auth sessions still have live unassigned refresh tokens")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return verifyBackfilledSessionCaps(ctx, db)
}
