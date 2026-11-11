package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestVerifyWalletAuthCreatesNoSessionOrRefreshCredential(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	for i := range 2 {
		message := fmt.Sprintf("Sign in to FrameWorks. Nonce: fixture-%d", i)
		address, signature, err := auth.SignWalletMessage("4c0883a69102937d6231471b5dbb6204fe51296170827922b7a56c91b8b56d09", message)
		if err != nil {
			t.Fatal(err)
		}
		mock.ExpectQuery(`UPDATE commodore.wallet_auth_challenges`).
			WithArgs(address, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("challenge"))
		mock.ExpectQuery(`FROM commodore.wallet_identities`).WithArgs("ethereum", address).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id"}).AddRow(testTenantID, sessionTestUserID))
		mock.ExpectExec(`UPDATE commodore.wallet_identities`).WithArgs("ethereum", address).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`FROM commodore.users\s+WHERE id = \$1 AND tenant_id = \$2`).WithArgs(sessionTestUserID, testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "email", "first_name", "last_name", "role", "permissions", "is_active", "verified", "last_login_at", "created_at", "updated_at", "platform_operator"}).
				AddRow(sessionTestUserID, testTenantID, "wallet@example.com", nil, nil, "owner", "{}", true, true, nil, time.Now(), time.Now(), false))
		response, err := s.VerifyWalletAuth(ctx, &commodorepb.WalletLoginRequest{WalletAddress: address, Message: message, Signature: signature})
		if err != nil || response.GetUser().GetId() != sessionTestUserID || response.GetWalletAddress() != address {
			t.Fatalf("verified wallet: response=%+v err=%v", response, err)
		}
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Fatal(expectationErr)
		}
		mock.ExpectQuery(`UPDATE commodore.wallet_auth_challenges`).WithArgs(address, sqlmock.AnyArg()).WillReturnError(sql.ErrNoRows)
		_, err = s.VerifyWalletAuth(ctx, &commodorepb.WalletLoginRequest{WalletAddress: address, Message: message, Signature: signature})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("replayed challenge: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWalletBearerRechecksActiveOwnerAndWalletLink(t *testing.T) {
	for _, linked := range []bool{true, false} {
		t.Run(fmt.Sprintf("linked=%v", linked), func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			s.runtimeSettings = refreshTokenTestSettings
			token, err := auth.GenerateWalletBearerJWT(sessionTestUserID, testTenantID, "", "owner", "0xwallet", s.settings().JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`FROM commodore.users\s+WHERE id = \$1 AND tenant_id = \$2`).WithArgs(sessionTestUserID, testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}).AddRow(true, nil))
			mock.ExpectQuery(`SELECT EXISTS \(SELECT 1 FROM commodore.wallet_identities`).WithArgs(testTenantID, sessionTestUserID, "0xwallet").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(linked))
			response, err := s.ValidateSession(context.Background(), &commodorepb.ValidateSessionRequest{Token: token})
			if err != nil || response.GetValid() != linked {
				t.Fatalf("wallet state: response=%+v err=%v", response, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
