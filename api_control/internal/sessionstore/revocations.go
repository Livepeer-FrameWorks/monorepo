package sessionstore

import (
	"context"
	"database/sql"
	"fmt"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type BeforeRevocationEvent func(context.Context, *sql.Tx, commodoredb.CommodoreAuthSession) error

// FinalizeRevocationTx waits for old refresh writers before deleting the connected
// family. They then return invalid_grant rather than broadening sign-out to a user.
func FinalizeRevocationTx(ctx context.Context, tx *sql.Tx, row commodoredb.CommodoreAuthSession, reason internalv1.SessionRevocationReason, actor events.Actor, before BeforeRevocationEvent) error {
	if !row.RevokedAt.Valid {
		return fmt.Errorf("session revocation timestamp required")
	}
	queries := commodoredb.New(tx)
	if _, err := queries.LockAuthSessionRefreshTokens(ctx, commodoredb.LockAuthSessionRefreshTokensParams{TenantID: row.TenantID, UserID: row.UserID, SessionID: sql.NullString{String: row.ID, Valid: true}}); err != nil {
		return err
	}
	if err := queries.RevokeAuthSessionRefreshTokens(ctx, commodoredb.RevokeAuthSessionRefreshTokensParams{TenantID: row.TenantID, UserID: row.UserID, SessionID: sql.NullString{String: row.ID, Valid: true}}); err != nil {
		return err
	}
	if before != nil {
		if err := before(ctx, tx, row); err != nil {
			return err
		}
	}
	event, err := events.New("commodore", row.TenantID, row.ID, &internalv1.AuthSessionRevoked{UserId: row.UserID, SessionId: row.ID, RevokedAt: timestamppb.New(row.RevokedAt.Time), Reason: reason}, events.WithActor(actor), events.WithTime(row.RevokedAt.Time))
	if err != nil {
		return fmt.Errorf("build domain event: %w", err)
	}
	return eventoutbox.Enqueue(ctx, tx, "commodore", event)
}
