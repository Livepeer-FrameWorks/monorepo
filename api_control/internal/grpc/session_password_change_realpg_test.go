//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPasswordChangePreservesCurrentSession_RealPG(t *testing.T) {
	testPasswordChangePreservesCurrentSession(t, startCommodoreRealPG(t))
}

func TestPasswordChangePreservesCurrentSession_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_password_change", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testPasswordChangePreservesCurrentSession(t, db)
}

func testPasswordChangePreservesCurrentSession(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	userID, tenantID, foreignUser := uuid.NewString(), uuid.NewString(), uuid.NewString()
	hash, err := auth.HashPassword("old-password", 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{userID, foreignUser} {
		if _, err = db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,is_active,verified,reset_token,reset_token_expires)
VALUES($1,$2,$4,$3,true,true,'outstanding-reset',NOW()+INTERVAL '1 hour')`, owner, tenantID, hash, owner+"@example.test"); err != nil {
			t.Fatal(err)
		}
	}
	s := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: refreshTokenTestSettings}
	issue := func(owner string) *commodorepb.AuthResponse {
		t.Helper()
		var result *commodorepb.AuthResponse
		err = s.withEventTx(ctx, func(tx *sql.Tx) error {
			var issueErr error
			result, issueErr = s.issueUserSessionTx(ctx, tx, owner, tenantID,
				SessionContext{AuthMethod: "password", ClientID: "web", DeviceLabel: "Browser", passwordHash: hash})
			return issueErr
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	current, other, foreign := issue(userID), issue(userID), issue(foreignUser)
	claims, err := auth.ValidateInteractiveJWT(current.Token, s.settings().JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	caller := context.WithValue(ctxAs(userID, tenantID, "owner"), ctxkeys.KeyAuthType, "jwt")
	caller = context.WithValue(caller, ctxkeys.KeySessionID, claims.SessionID)
	request := &commodorepb.ChangePasswordRequest{CurrentPassword: "wrong-password", NewPassword: "new-password"}
	if _, err = s.ChangePassword(caller, request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("wrong current password: %v", err)
	}
	request.CurrentPassword = "old-password"
	// An older issuer can rotate the current named family into a sid-less head.
	if _, err = db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at)
VALUES($1,$2,$3,$4,NOW()+INTERVAL '1 hour')`, uuid.NewString(), tenantID, userID, hashToken("current-legacy-child")); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET revoked=true, rotated_at=NOW(), replaced_by=
(SELECT id FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$3)
WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$4`, tenantID, userID, hashToken("current-legacy-child"), hashToken(current.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	current.RefreshToken = "current-legacy-child"
	changed, err := s.ChangePassword(caller, request)
	if err != nil || !changed.GetSuccess() || changed.GetRevokedCount() != 1 {
		t.Fatalf("password change: %+v %v", changed, err)
	}
	var newHash string
	var resetToken sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT password_hash, reset_token FROM commodore.users WHERE tenant_id=$1 AND id=$2`, tenantID, userID).
		Scan(&newHash, &resetToken); err != nil || !auth.CheckPassword("new-password", newHash) || auth.CheckPassword("old-password", newHash) || resetToken.Valid {
		t.Fatalf("password/reset state: reset=%v err=%v", resetToken.Valid, err)
	}
	for _, entry := range []struct {
		name  string
		token string
		valid bool
	}{{"current", current.Token, true}, {"other", other.Token, false}, {"foreign owner", foreign.Token, true}} {
		response, validateErr := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: entry.token})
		if validateErr != nil || response.GetValid() != entry.valid {
			t.Fatalf("%s access valid=%v err=%v", entry.name, response.GetValid(), validateErr)
		}
	}
	if _, err = s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: other.RefreshToken}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("other session refreshed after password change: %v", err)
	}
	if _, err = s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: current.RefreshToken}); err != nil {
		t.Fatalf("current session could not refresh: %v", err)
	}

	// Both requests prove the same old hash before either changes it.
	checked, resume := make(chan struct{}, 2), make(chan struct{})
	s.checkPassword = func(password, hash string) bool {
		match := auth.CheckPassword(password, hash)
		checked <- struct{}{}
		select {
		case <-resume:
		case <-ctx.Done():
		}
		return match
	}
	var group sync.WaitGroup
	results := make([]error, 2)
	for i := range 2 {
		group.Go(func() {
			_, results[i] = s.ChangePassword(caller, &commodorepb.ChangePasswordRequest{
				CurrentPassword: "new-password", NewPassword: "racing-password"})
		})
	}
	for range 2 {
		select {
		case <-checked:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(resume)
	group.Wait()
	wins, stale := 0, 0
	for _, result := range results {
		switch status.Code(result) {
		case codes.OK:
			wins++
		case codes.Unauthenticated:
			stale++
		default:
			t.Fatalf("concurrent password change: %v", result)
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("password-change wins=%d stale proofs=%d", wins, stale)
	}

	s.checkPassword = nil
	if err = db.QueryRowContext(ctx, `SELECT password_hash FROM commodore.users WHERE tenant_id=$1 AND id=$2`, tenantID, userID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	rollbackSession := issue(userID)
	if _, err = db.ExecContext(ctx, `ALTER TABLE commodore.domain_event_outbox RENAME TO password_change_unavailable_outbox`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := db.ExecContext(ctx, `ALTER TABLE commodore.password_change_unavailable_outbox RENAME TO domain_event_outbox`); restoreErr != nil {
			t.Errorf("restore event outbox: %v", restoreErr)
		}
	}()
	if _, err = s.ChangePassword(caller, &commodorepb.ChangePasswordRequest{CurrentPassword: "racing-password", NewPassword: "must-not-commit"}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unavailable revocation event: %v", err)
	}
	if err = db.QueryRowContext(ctx, `SELECT password_hash FROM commodore.users WHERE tenant_id=$1 AND id=$2`, tenantID, userID).Scan(&newHash); err != nil || newHash != hash {
		t.Fatalf("password committed without its revocation event: %v", err)
	}
	response, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: rollbackSession.Token})
	if err != nil || !response.GetValid() {
		t.Fatalf("session revocation did not roll back with password: valid=%v err=%v", response.GetValid(), err)
	}
}
