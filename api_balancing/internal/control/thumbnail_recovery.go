package control

import (
	"context"
	"database/sql"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// ClaimedRecoveryAttempt is one leased stuck-incomplete attempt the recovery worker will re-drive. Token fences
// its settlement so a worker whose lease later expired (and was re-claimed by a peer) cannot clobber the row.
type ClaimedRecoveryAttempt struct {
	AttemptID string
	Attempts  int // prior recovery_attempts, for backoff computation
	Token     string
}

// ClaimStuckIncompleteThumbnailAttempts LEASES a batch of non-expired, pre-'publishing' attempts whose completion
// is presumed LOST (idle past staleBefore) and that are DUE (not backed off, not already leased). It is the HA-safe
// replacement for a plain SELECT: SKIP LOCKED + the lease means two replicas never re-drive the same attempt, and
// the due-ordering (backed-off poison rows sink) means a broken attempt cannot starve other lost completions. Each
// claim mints a fresh fencing token. 'publishing' and expired attempts are excluded — the guarded, self-clearing
// publish/fail/GC phases own those.
//
// The LEASE, DUE-TIME, and EXPIRY comparisons all use PostgreSQL NOW() (the DB owns those timestamps), so a
// replica with a lagging wall clock cannot mint a lease that another replica already considers expired, or reclaim
// a live lease. Only staleBefore (a coarse "how long idle" threshold) is caller-provided.
func ClaimStuckIncompleteThumbnailAttempts(ctx context.Context, dbh *sql.DB, staleBefore time.Time, leaseTTL time.Duration, limit int) ([]ClaimedRecoveryAttempt, error) {
	if dbh == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := foghorndb.New(dbh).ClaimStuckIncompleteThumbnailAttempts(ctx, foghorndb.ClaimStuckIncompleteThumbnailAttemptsParams{
		LeaseSeconds: int64(leaseTTL.Seconds()), StaleBefore: staleBefore, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ClaimedRecoveryAttempt, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClaimedRecoveryAttempt{AttemptID: row.AttemptID, Attempts: int(row.RecoveryAttempts), Token: row.RecoveryLeaseToken.String})
	}
	return out, nil
}

// ClaimUnprojectedPublishedThumbnailAttempts LEASES a batch of published-but-unprojected attempts (the deterministic
// served object never landed after the publish CAS) that are STILL the asset's active pointer, DUE (idle past
// staleBefore, not backed off, not already leased). It mirrors ClaimStuckIncompleteThumbnailAttempts for the projection
// re-drive: SKIP LOCKED + a fencing token so two replicas never re-project the same attempt, and due-ordering so a
// poison row (source still absent, copy persistently failing) sinks and cannot starve healthy projections. A superseded
// attempt is excluded (the JOIN on active_version) — the newer winner owns the served key. All time comparisons use
// DB NOW(); only staleBefore is caller-provided.
func ClaimUnprojectedPublishedThumbnailAttempts(ctx context.Context, dbh *sql.DB, staleBefore time.Time, leaseTTL time.Duration, limit int) ([]ClaimedRecoveryAttempt, error) {
	if dbh == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := foghorndb.New(dbh).ClaimUnprojectedPublishedThumbnailAttempts(ctx, foghorndb.ClaimUnprojectedPublishedThumbnailAttemptsParams{
		LeaseSeconds: int64(leaseTTL.Seconds()), StaleBefore: staleBefore, RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ClaimedRecoveryAttempt, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClaimedRecoveryAttempt{AttemptID: row.AttemptID, Attempts: int(row.RecoveryAttempts), Token: row.RecoveryLeaseToken.String})
	}
	return out, nil
}

// ClaimDueReassertThumbnailAttempts LEASES a batch of attempts whose one-shot reassert clock is DUE
// (deterministic_reassert_at <= now) and that are not backed off / already leased. Past the max-copy window the winner
// re-copies its bytes once (correcting a straggler overwrite), so this MUST be leased/backoff like the other phases: a
// re-copy that keeps failing would otherwise be re-selected at the head every tick and starve newer winners' reasserts.
// staleBefore is accepted for a uniform claim signature but unused — reassert dueness is `deterministic_reassert_at`,
// which is DB NOW()-relative. Not JOINed to the active pointer: a superseded winner is still selected so its clock is
// cleared (ReassertThumbnailProjection skips the copy for it).
func ClaimDueReassertThumbnailAttempts(ctx context.Context, dbh *sql.DB, _ time.Time, leaseTTL time.Duration, limit int) ([]ClaimedRecoveryAttempt, error) {
	if dbh == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := foghorndb.New(dbh).ClaimDueReassertThumbnailAttempts(ctx, foghorndb.ClaimDueReassertThumbnailAttemptsParams{
		LeaseSeconds: int64(leaseTTL.Seconds()), RowLimit: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]ClaimedRecoveryAttempt, 0, len(rows))
	for _, row := range rows {
		out = append(out, ClaimedRecoveryAttempt{AttemptID: row.AttemptID, Attempts: int(row.RecoveryAttempts), Token: row.RecoveryLeaseToken.String})
	}
	return out, nil
}

// UnprojectedThumbnailRecoveryBacklog counts published-but-unprojected active attempts currently DUE for a projection
// re-drive (excludes backed-off + leased) — the observable, bounded projection backlog.
func UnprojectedThumbnailRecoveryBacklog(ctx context.Context, dbh *sql.DB, staleBefore time.Time) (int, error) {
	if dbh == nil {
		return 0, nil
	}
	n, err := foghorndb.New(dbh).UnprojectedThumbnailRecoveryBacklog(ctx, staleBefore)
	return int(n), err
}

// SettleThumbnailRecoveryDone clears the recovery lease + backoff for an attempt the worker made progress on
// (reached a terminal state), token-fenced. A terminal row is never re-selected anyway; this just keeps the
// lease/backoff columns tidy. A stolen lease (token mismatch) makes it a harmless no-op.
func SettleThumbnailRecoveryDone(ctx context.Context, dbh *sql.DB, attemptID, token string) error {
	if dbh == nil {
		return nil
	}
	return foghorndb.New(dbh).SettleThumbnailRecoveryDone(ctx, foghorndb.SettleThumbnailRecoveryDoneParams{AttemptID: attemptID, RecoveryLeaseToken: sql.NullString{String: token, Valid: true}})
}

// BackoffThumbnailRecovery records a non-progressing re-drive: it bumps recovery_attempts, schedules the next
// eligible time (recovery_next_attempt_at = now + backoff) so the attempt is NOT re-selected at the head of the
// next pass, and releases the lease — all token-fenced. This is the poison isolation: a broken/not-yet-uploaded
// attempt spaces out its retries instead of consuming a batch slot every tick and starving valid lost completions.
func BackoffThumbnailRecovery(ctx context.Context, dbh *sql.DB, attemptID, token string, backoff time.Duration, errMsg string) error {
	if dbh == nil {
		return nil
	}
	return foghorndb.New(dbh).BackoffThumbnailRecovery(ctx, foghorndb.BackoffThumbnailRecoveryParams{
		BackoffSeconds: int64(backoff.Seconds()), ErrorMessage: errMsg, AttemptID: attemptID,
		LeaseToken: sql.NullString{String: token, Valid: true},
	})
}

// ThumbnailRecoveryBacklog counts stuck-incomplete attempts that are currently DUE for a re-drive (past staleness,
// not backed off, not leased) — the observable, bounded backlog the reconciler is working down. Backed-off and
// in-flight-leased attempts are excluded so the number reflects actionable work, not the whole non-terminal set.
func ThumbnailRecoveryBacklog(ctx context.Context, dbh *sql.DB, staleBefore time.Time) (int, error) {
	if dbh == nil {
		return 0, nil
	}
	n, err := foghorndb.New(dbh).ThumbnailRecoveryBacklog(ctx, staleBefore)
	return int(n), err
}
