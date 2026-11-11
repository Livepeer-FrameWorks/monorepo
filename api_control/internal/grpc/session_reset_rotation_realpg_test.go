//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

func TestPasswordResetDrainsOldRefreshRotation_RealPG(t *testing.T) {
	testPasswordResetDrainsOldRefreshRotation(t, startCommodoreRealPG(t))
}

func TestPasswordResetDrainsOldRefreshRotation_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_reset_rotation", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testPasswordResetDrainsOldRefreshRotation(t, db)
}

func testPasswordResetDrainsOldRefreshRotation(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db.SetMaxOpenConns(5)
	userID, tenantID, rootID, childID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,is_active,verified,reset_token,reset_token_expires)
VALUES($1,$2,'reset-rotation@example.test','unused',true,true,$3,NOW()+INTERVAL '1 hour')`, userID, tenantID, hashToken("reset-rotation")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at) VALUES($1,$2,$3,'old-root',NOW()+INTERVAL '1 hour')`, rootID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	old, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Rollback() }()
	var id string
	if err = old.QueryRowContext(ctx, `SELECT id FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND id=$3 FOR UPDATE`, tenantID, userID, rootID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at) VALUES($1,$2,$3,'old-child',NOW()+INTERVAL '1 hour')`, childID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = old.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET revoked=true,rotated_at=NOW(),replaced_by=$4 WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, rootID, childID); err != nil {
		t.Fatal(err)
	}
	s := &CommodoreServer{db: db, logger: logrus.New(), runtimeSettings: refreshTokenTestSettings, tokenHasher: testTokenHasher(t)}
	resetDone := make(chan error, 1)
	go func() {
		response, resetErr := s.ResetPassword(ctx, &commodorepb.ResetPasswordRequest{Token: "reset-rotation", Password: "new-password"})
		if resetErr == nil && !response.GetSuccess() {
			resetErr = sql.ErrNoRows
		}
		resetDone <- resetErr
	}()
	var transactionStart time.Time
	for {
		queryErr := db.QueryRowContext(ctx, `SELECT xact_start FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
AND state='active' AND (query LIKE '-- name: DeleteRefreshTokensForUser%' OR query LIKE '-- name: LockUserRefreshTokensForRevocation%') LIMIT 1`).Scan(&transactionStart)
		if queryErr == nil {
			break
		}
		if queryErr != sql.ErrNoRows {
			t.Fatal(queryErr)
		}
		select {
		case err := <-resetDone:
			t.Fatalf("reset did not wait for old rotation: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	for time.Now().Unix() <= transactionStart.Unix() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	oldAccess, err := auth.GenerateSessionJWT(userID, tenantID, "reset-rotation@example.test", "owner", nil, time.Now(), s.settings().JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-resetDone; err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID).Scan(&remaining); err != nil || remaining != 0 {
		t.Errorf("old issuer left %d refresh rows after password reset: %v", remaining, err)
	}
	checked, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: oldAccess})
	if err != nil || checked.GetValid() {
		t.Errorf("access minted by in-flight old rotation survived password reset: %+v %v", checked, err)
	}
}
