package sessionstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const (
	MaxLive       = 100
	EvictionBatch = 20
	CountLimit    = MaxLive + EvictionBatch
)

// Overflow rolls back issuance before housekeeping commits independently. A
// consumed device/authorization grant remains available when cleanup is retried.
type Overflow struct{ TenantID, UserID, ProtectedSessionID string }

func (*Overflow) Error() string { return "session cap cleanup required" }

type FinishRevocation func(context.Context, *sql.Tx, commodoredb.CommodoreAuthSession) error

type TrimResult struct {
	Removed int
	Expired int
	Done    bool
}

// TrimCapBatch holds the owner through selection, revocation and commit. Counts
// saturate at CountLimit and every transaction ends at most EvictionBatch sessions.
func TrimCapBatch(ctx context.Context, db database.PostgresTxBeginner, tenant, user string, keep int, protected string, finish FinishRevocation) (TrimResult, error) {
	var result TrimResult
	if keep < 0 || keep > MaxLive || finish == nil {
		return result, fmt.Errorf("invalid session cap cleanup configuration")
	}
	err := database.WithRetryablePostgresTxBeginner(ctx, db, nil, func(tx *sql.Tx) error {
		result = TrimResult{}
		queries := commodoredb.New(tx)
		if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenant, ID: user}); lockErr != nil {
			return lockErr
		}
		count, countErr := queries.CountAuthSessionsForIssuance(ctx, commodoredb.CountAuthSessionsForIssuanceParams{TenantID: tenant, UserID: user, RowLimit: CountLimit})
		if countErr != nil {
			return countErr
		}
		if count.LiveCount <= int32(keep) {
			result.Done = true
			return nil
		}
		excess := min(count.LiveCount-int32(keep), EvictionBatch)
		trimmed, evictErr := evictCapSessionsTx(ctx, tx, tenant, user, excess, protected, count.CheckedAt, finish)
		if evictErr != nil {
			return evictErr
		}
		result = trimmed

		result.Done = count.LiveCount < CountLimit && int(count.LiveCount)-result.Removed <= keep
		return nil
	})
	if err != nil {
		return TrimResult{}, err
	}
	return result, nil
}

// The LRU index bounds candidate reads even behind expired history. Marking
// expired candidates terminal makes progress survive cancellation and retries.
func evictCapSessionsTx(ctx context.Context, tx *sql.Tx, tenant, user string, count int32, protected string, checkedAt time.Time, finish FinishRevocation) (TrimResult, error) {
	if count < 1 || count > EvictionBatch || finish == nil {
		return TrimResult{}, fmt.Errorf("invalid session cap eviction budget")
	}
	queries := commodoredb.New(tx)
	var rows []commodoredb.CommodoreAuthSession
	var err error
	if protected == "" {
		rows, err = queries.ListOldestAuthSessions(ctx, commodoredb.ListOldestAuthSessionsParams{TenantID: tenant, UserID: user, Limit: EvictionBatch})
	} else {
		rows, err = queries.ListOldestOtherAuthSessions(ctx, commodoredb.ListOldestOtherAuthSessionsParams{TenantID: tenant, UserID: user, ProtectedSessionID: protected, RowLimit: EvictionBatch})
	}
	if err != nil {
		return TrimResult{}, err
	}
	if len(rows) == 0 {
		return TrimResult{}, fmt.Errorf("session cap cleanup made no progress")
	}
	var result TrimResult
	for _, row := range rows {
		if !row.IdleExpiresAt.After(checkedAt) || !row.AbsoluteExpiresAt.After(checkedAt) {
			marked, markErr := queries.MarkAuthSessionExpired(ctx, commodoredb.MarkAuthSessionExpiredParams{TenantID: tenant, UserID: user, ID: row.ID, CheckedAt: checkedAt})
			if markErr != nil {
				return TrimResult{}, markErr
			}
			result.Expired += int(marked)
			continue
		}
		if result.Removed >= int(count) {
			break
		}
		revoked, revokeErr := queries.RevokeAuthSession(ctx, commodoredb.RevokeAuthSessionParams{TenantID: tenant, UserID: user, ID: row.ID, RevokeReason: sql.NullString{String: "expired", Valid: true}})
		if revokeErr != nil {
			return TrimResult{}, revokeErr
		}
		if revokeErr = finish(ctx, tx, revoked); revokeErr != nil {
			return TrimResult{}, revokeErr
		}
		result.Removed++
	}
	return result, nil
}
