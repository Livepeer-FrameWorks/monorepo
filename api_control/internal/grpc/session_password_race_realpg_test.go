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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPasswordLoginCannotCrossPasswordReset_RealPG(t *testing.T) {
	testPasswordLoginCannotCrossPasswordReset(t, startCommodoreRealPG(t))
}

func TestPasswordLoginCannotCrossPasswordReset_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_session_password_race", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testPasswordLoginCannotCrossPasswordReset(t, db)
}

func testPasswordLoginCannotCrossPasswordReset(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	userID, tenantID := uuid.NewString(), uuid.NewString()
	passwordHash, err := auth.HashPassword("old-password", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,is_active,verified,reset_token,reset_token_expires)
VALUES($1,$2,'password-race@example.test',$3,true,true,$4,NOW()+INTERVAL '1 hour')`, userID, tenantID, passwordHash, hashToken("reset-race")); err != nil {
		t.Fatal(err)
	}
	checked, resume := make(chan struct{}), make(chan struct{})
	s := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: refreshTokenTestSettings}
	s.checkPassword = func(password, hash string) bool {
		match := auth.CheckPassword(password, hash)
		close(checked)
		select {
		case <-resume:
		case <-ctx.Done():
		}
		return match
	}
	result := make(chan error, 1)
	go func() {
		_, loginErr := s.Login(ctx, &commodorepb.LoginRequest{Email: "password-race@example.test", Password: "old-password", HumanCheck: "human", Behavior: goodBehavior()})
		result <- loginErr
	}()
	select {
	case <-checked:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	reset, err := s.ResetPassword(ctx, &commodorepb.ResetPasswordRequest{Token: "reset-race", Password: "new-password"})
	close(resume)
	if err != nil || !reset.GetSuccess() {
		t.Fatalf("reset: %+v %v", reset, err)
	}
	select {
	case loginErr := <-result:
		if status.Code(loginErr) != codes.Unauthenticated {
			t.Fatalf("old password created a session after reset: %v", loginErr)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
