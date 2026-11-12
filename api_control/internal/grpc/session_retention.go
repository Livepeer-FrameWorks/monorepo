package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const sessionRetentionBatch int32 = 100

type sessionRetentionKey struct {
	ID, TenantID, UserID string
	EndedAt              time.Time
}

type sessionRetentionCursor struct {
	At time.Time
	ID string
}

func (c sessionRetentionCursor) values() (time.Time, string) {
	if c.ID == "" {
		return time.Time{}, "00000000-0000-0000-0000-000000000000"
	}
	return c.At, c.ID
}

type sessionRetentionProgress struct {
	Expired, Revoked, Unassigned, NamedRotations sessionRetentionCursor
}

// Each replica walks keys rather than repeatedly selecting protected oldest
// owners. Cursors wrap at the end; locked rows and large families are revisited.
func (s *CommodoreServer) sweepSessionRetention(ctx context.Context, progress *sessionRetentionProgress) (int64, error) {
	queries := commodoredb.New(s.db)
	var deleted int64
	remaining := sessionRetentionBatch
	for _, branch := range []struct {
		cursor *sessionRetentionCursor
		list   func(time.Time, string) ([]sessionRetentionKey, error)
	}{
		{&progress.Expired, func(at time.Time, id string) ([]sessionRetentionKey, error) {
			rows, err := queries.ListExpiredSessionRetentionKeys(ctx, commodoredb.ListExpiredSessionRetentionKeysParams{AfterAt: at, AfterID: id, BatchSize: sessionRetentionBatch})
			keys := make([]sessionRetentionKey, len(rows))
			for i, row := range rows {
				keys[i] = sessionRetentionKey(row)
			}
			return keys, err
		}},
		{&progress.Revoked, func(at time.Time, id string) ([]sessionRetentionKey, error) {
			rows, err := queries.ListRevokedSessionRetentionKeys(ctx, commodoredb.ListRevokedSessionRetentionKeysParams{AfterAt: at, AfterID: id, BatchSize: sessionRetentionBatch})
			keys := make([]sessionRetentionKey, len(rows))
			for i, row := range rows {
				keys[i] = sessionRetentionKey(row)
			}
			return keys, err
		}},
	} {
		at, id := branch.cursor.values()
		keys, err := branch.list(at, id)
		if err != nil {
			return deleted, err
		}
		if len(keys) == 0 {
			*branch.cursor = sessionRetentionCursor{}
		}
		for _, key := range keys {
			if remaining == 0 {
				break
			}
			count, tokens, sweepErr := s.retainEndedSession(ctx, key, remaining)
			if sweepErr != nil {
				return deleted, sweepErr
			}
			deleted += count
			remaining -= tokens
			*branch.cursor = sessionRetentionCursor{At: key.EndedAt, ID: key.ID}
		}
	}
	at, id := progress.Unassigned.values()
	keys, err := queries.ListUnassignedRefreshRetentionKeys(ctx, commodoredb.ListUnassignedRefreshRetentionKeysParams{AfterAt: at, AfterID: id, BatchSize: sessionRetentionBatch})
	if err != nil {
		return deleted, err
	}
	if len(keys) == 0 {
		progress.Unassigned = sessionRetentionCursor{}
	}
	for _, key := range keys {
		count, sweepErr := s.retainUnassignedToken(ctx, sessionRetentionKey(key))
		if sweepErr != nil {
			return deleted, sweepErr
		}
		deleted += count
		progress.Unassigned = sessionRetentionCursor{At: key.EndedAt, ID: key.ID}
	}

	at, id = progress.NamedRotations.values()
	rotations, err := queries.ListNamedRefreshRetentionKeys(ctx, commodoredb.ListNamedRefreshRetentionKeysParams{AfterAt: at, AfterID: id, BatchSize: sessionRetentionBatch})
	if err != nil {
		return deleted, err
	}
	if len(rotations) == 0 {
		progress.NamedRotations = sessionRetentionCursor{}
	}
	for _, row := range rotations {
		key := sessionRetentionKey{ID: row.ID, TenantID: row.TenantID, UserID: row.UserID, EndedAt: row.EndedAt}
		count, sweepErr := s.retainExpiredRefreshToken(ctx, key, sql.NullString{String: row.SessionID, Valid: true})
		if sweepErr != nil {
			return deleted, sweepErr
		}
		deleted += count
		progress.NamedRotations = sessionRetentionCursor{At: row.EndedAt, ID: row.ID}
	}
	return deleted, nil
}

var errRetentionDeferred = errors.New("retention deferred for an active or locked chain")

func (s *CommodoreServer) retentionTx(ctx context.Context, fn func(*commodoredb.Queries) (int64, int32, error)) (int64, int32, error) {
	var deleted int64
	var tokens int32
	err := database.WithRetryablePostgresTx(ctx, s.db, &sql.TxOptions{Isolation: sql.LevelReadCommitted}, func(tx *sql.Tx) error {
		// Only the final committed attempt contributes to the sweep's write budget.
		var attemptErr error
		deleted, tokens, attemptErr = fn(commodoredb.New(tx))
		return attemptErr
	})
	if errors.Is(err, errRetentionDeferred) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return deleted, tokens, nil
}

func (s *CommodoreServer) retainEndedSession(ctx context.Context, key sessionRetentionKey, batch int32) (int64, int32, error) {
	return s.retentionTx(ctx, func(queries *commodoredb.Queries) (int64, int32, error) {
		_, err := queries.LockRetainedAuthSession(ctx, commodoredb.LockRetainedAuthSessionParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, errRetentionDeferred
		}
		if err != nil {
			return 0, 0, err
		}
		sid := sql.NullString{String: key.ID, Valid: true}
		tokens, err := queries.LockRetainedSessionTokenKeys(ctx, commodoredb.LockRetainedSessionTokenKeysParams{TenantID: key.TenantID, UserID: key.UserID, SessionID: sid, BatchSize: batch})
		if err != nil {
			return 0, 0, err
		}
		// This separate READ COMMITTED statement observes any legacy successor
		// committed before the token locks were obtained.
		live, err := queries.HasLiveUnassignedRefreshToken(ctx, commodoredb.HasLiveUnassignedRefreshTokenParams{TenantID: key.TenantID, UserID: key.UserID})
		if err != nil {
			return 0, 0, err
		}
		if live {
			return 0, 0, errRetentionDeferred
		}
		var deleted int64
		for _, tokenID := range tokens {
			count, deleteErr := queries.DeleteRetainedSessionToken(ctx, commodoredb.DeleteRetainedSessionTokenParams{TenantID: key.TenantID, UserID: key.UserID, SessionID: sid, ID: tokenID})
			if deleteErr != nil {
				return 0, 0, deleteErr
			}
			deleted += count
		}
		count, err := queries.DeleteRetainedAuthSession(ctx, commodoredb.DeleteRetainedAuthSessionParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
		return deleted + count, int32(len(tokens)), err
	})
}

func (s *CommodoreServer) retainUnassignedToken(ctx context.Context, key sessionRetentionKey) (int64, error) {
	return s.retainExpiredRefreshToken(ctx, key, sql.NullString{})
}

func (s *CommodoreServer) retainExpiredRefreshToken(ctx context.Context, key sessionRetentionKey, sid sql.NullString) (int64, error) {
	count, _, err := s.retentionTx(ctx, func(queries *commodoredb.Queries) (int64, int32, error) {
		var lockErr error
		if sid.Valid {
			_, lockErr = queries.LockRetainedNamedToken(ctx, commodoredb.LockRetainedNamedTokenParams{TenantID: key.TenantID, UserID: key.UserID, SessionID: sid, ID: key.ID})
		} else {
			_, lockErr = queries.LockRetainedUnassignedToken(ctx, commodoredb.LockRetainedUnassignedTokenParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
		}
		if errors.Is(lockErr, sql.ErrNoRows) {
			return 0, 0, errRetentionDeferred
		}
		if lockErr != nil {
			return 0, 0, lockErr
		}
		live, queryErr := queries.HasLiveUnassignedRefreshToken(ctx, commodoredb.HasLiveUnassignedRefreshTokenParams{TenantID: key.TenantID, UserID: key.UserID})
		if queryErr != nil {
			return 0, 0, queryErr
		}
		if live {
			return 0, 0, errRetentionDeferred
		}
		var deleted int64
		var deleteErr error
		if sid.Valid {
			deleted, deleteErr = queries.DeleteRetainedSessionToken(ctx, commodoredb.DeleteRetainedSessionTokenParams{TenantID: key.TenantID, UserID: key.UserID, SessionID: sid, ID: key.ID})
		} else {
			deleted, deleteErr = queries.DeleteRetainedUnassignedToken(ctx, commodoredb.DeleteRetainedUnassignedTokenParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
		}
		return deleted, 0, deleteErr
	})
	return count, err
}

func (s *CommodoreServer) runSessionRetention(ctx context.Context) {
	if s.db == nil {
		return
	}
	var progress sessionRetentionProgress
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		deleted, err := s.sweepSessionRetention(sweepCtx, &progress)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Session retention sweep failed")
		} else if deleted > 0 {
			s.logger.WithField("deleted", deleted).Debug("Ended session records removed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
