package grpc

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
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
	Expired, Revoked, Unassigned sessionRetentionCursor
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
	return deleted, nil
}

func (s *CommodoreServer) retainEndedSession(ctx context.Context, key sessionRetentionKey, batch int32) (int64, int32, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, 0, err
	}
	defer s.rollbackRetentionTx(tx)
	queries := commodoredb.New(tx)
	_, err = queries.LockRetainedAuthSession(ctx, commodoredb.LockRetainedAuthSessionParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	sid := sql.NullString{String: key.ID, Valid: true}
	tokens, err := queries.LockRetainedSessionTokenKeys(ctx, commodoredb.LockRetainedSessionTokenKeysParams{TenantID: key.TenantID, UserID: key.UserID, SessionID: sid, BatchSize: batch})
	if err != nil {
		return 0, 0, err
	}
	// This must be a separate READ COMMITTED statement after taking token locks:
	// an older issuer may have committed a sid-less successor while we waited.
	live, err := queries.HasLiveUnassignedRefreshToken(ctx, commodoredb.HasLiveUnassignedRefreshTokenParams{TenantID: key.TenantID, UserID: key.UserID})
	if err != nil || live {
		return 0, 0, err
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
	if err != nil {
		return 0, 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, 0, err
	}
	return deleted + count, int32(len(tokens)), nil
}

func (s *CommodoreServer) retainUnassignedToken(ctx context.Context, key sessionRetentionKey) (int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer s.rollbackRetentionTx(tx)
	queries := commodoredb.New(tx)
	_, err = queries.LockRetainedUnassignedToken(ctx, commodoredb.LockRetainedUnassignedTokenParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	live, err := queries.HasLiveUnassignedRefreshToken(ctx, commodoredb.HasLiveUnassignedRefreshTokenParams{TenantID: key.TenantID, UserID: key.UserID})
	if err != nil || live {
		return 0, err
	}
	count, err := queries.DeleteRetainedUnassignedToken(ctx, commodoredb.DeleteRetainedUnassignedTokenParams{TenantID: key.TenantID, UserID: key.UserID, ID: key.ID})
	if err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *CommodoreServer) rollbackRetentionTx(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		s.logger.WithError(err).Warn("Session retention rollback failed")
	}
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
