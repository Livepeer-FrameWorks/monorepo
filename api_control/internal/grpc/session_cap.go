package grpc

import (
	"context"
	"database/sql"
	"errors"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/sessionstore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
)

// The overflowing issuance transaction has rolled back before these commits.
// Retrying it rechecks the credential and consumes its grant only on success.
func (s *CommodoreServer) recoverSessionCap(ctx context.Context, cause error) (bool, error) {
	overflow, ok := errors.AsType[*sessionstore.Overflow](cause)
	if !ok {
		return false, nil
	}
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return true, ctxErr
		}
		trimmed, err := sessionstore.TrimCapBatch(ctx, s.db, overflow.TenantID, overflow.UserID, maxLiveAuthSessions-1, overflow.ProtectedSessionID,
			func(batchCtx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession) error {
				return s.finishAuthSessionRevocationTx(batchCtx, tx, row, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT)
			})
		if err != nil {
			return true, err
		}
		if trimmed.Removed > 0 {
			s.domainEventsCommittedNotify()
		}
		if trimmed.Done {
			return true, nil
		}
	}
}

func (s *CommodoreServer) enforceAdoptedSessionCapTx(ctx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession) error {
	count, err := commodoredb.New(tx).CountAuthSessionsForIssuance(ctx, commodoredb.CountAuthSessionsForIssuanceParams{TenantID: row.TenantID, UserID: row.UserID, RowLimit: sessionstore.CountLimit})
	if err != nil {
		return err
	}
	if count.LiveCount > maxLiveAuthSessions {
		return &sessionstore.Overflow{TenantID: row.TenantID, UserID: row.UserID, ProtectedSessionID: row.ID}
	}
	return nil
}
