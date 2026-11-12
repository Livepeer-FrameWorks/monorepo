package datamigrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/sessionstore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/google/uuid"
)

type authCapCheckpoint struct {
	AfterUserID string `json:"cap_after_user_id,omitempty"`
}

func authCapCursor(data json.RawMessage) (sql.NullString, error) {
	var checkpoint authCapCheckpoint
	if len(data) > 0 {
		if err := json.Unmarshal(data, &checkpoint); err != nil {
			return sql.NullString{}, fmt.Errorf("decode session cap checkpoint: %w", err)
		}
	}
	if checkpoint.AfterUserID == "" {
		return sql.NullString{}, nil
	}
	id, err := uuid.Parse(checkpoint.AfterUserID)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("invalid session cap checkpoint: %w", err)
	}
	return sql.NullString{String: id.String(), Valid: true}, nil
}

func authCapQueries(db datamigrate.DB) (*commodoredb.Queries, error) {
	connection, ok := db.(commodoredb.DBTX)
	if !ok {
		return nil, fmt.Errorf("session cap migration requires the service query connection")
	}
	return commodoredb.New(connection), nil
}

func normalizeBackfilledSessionCaps(ctx context.Context, db datamigrate.DB, data json.RawMessage) (datamigrate.Progress, error) {
	after, err := authCapCursor(data)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	queries, queryErr := authCapQueries(db)
	if queryErr != nil {
		return datamigrate.Progress{}, queryErr
	}
	owners, err := queries.ListAuthSessionCapOwners(ctx, commodoredb.ListAuthSessionCapOwnersParams{AfterUserID: after, BatchSize: 1})
	if err != nil {
		return datamigrate.Progress{}, err
	}
	if len(owners) == 0 {
		return datamigrate.Progress{Done: true, Checkpoint: data}, nil
	}
	beginner, ok := db.(database.PostgresTxBeginner)
	if !ok {
		return datamigrate.Progress{}, fmt.Errorf("session cap normalization requires a writable database")
	}
	owner := owners[0]
	trimmed, err := sessionstore.TrimCapBatch(ctx, beginner, owner.TenantID, owner.UserID, sessionstore.MaxLive, "",
		func(batchCtx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession) error {
			return sessionstore.FinalizeRevocationTx(batchCtx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT, events.Actor{AuthType: "service"}, nil)
		})
	if errors.Is(err, sql.ErrNoRows) {
		trimmed.Done = true
		err = nil
	}
	if err != nil {
		return datamigrate.Progress{}, fmt.Errorf("normalize backfilled session cap: %w", err)
	}
	checkpoint := data
	if trimmed.Done {
		checkpoint, err = json.Marshal(authCapCheckpoint{AfterUserID: owner.UserID})
		if err != nil {
			return datamigrate.Progress{}, err
		}
	}
	return datamigrate.Progress{Scanned: 1, Changed: int64(trimmed.Removed + trimmed.Expired), Checkpoint: checkpoint}, nil
}

func previewBackfilledSessionCaps(ctx context.Context, db datamigrate.DB, size int) (datamigrate.Progress, error) {
	queries, queryErr := authCapQueries(db)
	if queryErr != nil {
		return datamigrate.Progress{}, queryErr
	}
	limit := min(size, 100)
	owners, err := queries.ListAuthSessionCapOwners(ctx, commodoredb.ListAuthSessionCapOwnersParams{BatchSize: int32(limit)})
	if err != nil {
		return datamigrate.Progress{}, err
	}
	over := 0
	for _, owner := range owners {
		count, countErr := queries.CountAuthSessionsForIssuance(ctx, commodoredb.CountAuthSessionsForIssuanceParams{TenantID: owner.TenantID, UserID: owner.UserID, RowLimit: sessionstore.MaxLive + 1})
		if countErr != nil {
			return datamigrate.Progress{}, countErr
		}
		if count.LiveCount > sessionstore.MaxLive {
			over++
		}
	}
	return datamigrate.Progress{Scanned: int64(len(owners)), Done: len(owners) < limit && over == 0, Summary: []string{fmt.Sprintf("sampled at most %d owners; %d require session-cap cleanup", limit, over)}}, nil
}

func verifyBackfilledSessionCaps(ctx context.Context, db datamigrate.DB) error {
	queries, queryErr := authCapQueries(db)
	if queryErr != nil {
		return queryErr
	}
	var after sql.NullString
	for {
		owners, err := queries.ListAuthSessionCapOwners(ctx, commodoredb.ListAuthSessionCapOwnersParams{AfterUserID: after, BatchSize: 100})
		if err != nil {
			return err
		}
		if len(owners) == 0 {
			return nil
		}
		for _, owner := range owners {
			count, countErr := queries.CountAuthSessionsForIssuance(ctx, commodoredb.CountAuthSessionsForIssuanceParams{TenantID: owner.TenantID, UserID: owner.UserID, RowLimit: sessionstore.MaxLive + 1})
			if countErr != nil {
				return countErr
			}
			if count.LiveCount > sessionstore.MaxLive {
				return fmt.Errorf("auth sessions still exceed the per-user cap")
			}
		}
		after = sql.NullString{String: owners[len(owners)-1].UserID, Valid: true}
	}
}
