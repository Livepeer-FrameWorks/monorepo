package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

func TestGetMe_MapsInlineUserAndWalletRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	createdAt := time.Unix(1_700_000_000, 0).UTC()
	updatedAt := createdAt.Add(30 * time.Minute)
	lastLoginAt := createdAt.Add(10 * time.Minute)
	walletCreatedAt := createdAt.Add(5 * time.Minute)

	mock.ExpectQuery("FROM commodore.users WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("user-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "email", "first_name", "last_name", "role", "permissions",
			"is_active", "verified", "last_login_at", "created_at", "updated_at",
		}).AddRow(
			"user-1",
			"tenant-1",
			"user@example.com",
			"Ada",
			"Lovelace",
			"owner",
			"{read,write}",
			true,
			true,
			lastLoginAt,
			createdAt,
			updatedAt,
		))

	mock.ExpectQuery("FROM commodore.wallet_identities").
		WithArgs("user-1").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "wallet_address", "created_at", "last_auth_at",
		}).AddRow(
			"wallet-1",
			"0xabc",
			walletCreatedAt,
			lastLoginAt,
		))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	resp, err := server.GetMe(ctx, &pb.GetMeRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.GetId() != "user-1" || resp.GetTenantId() != "tenant-1" {
		t.Fatalf("unexpected identity mapping: id=%q tenant_id=%q", resp.GetId(), resp.GetTenantId())
	}
	if resp.GetEmail() != "user@example.com" {
		t.Fatalf("unexpected email: %q", resp.GetEmail())
	}
	if resp.GetFirstName() != "Ada" || resp.GetLastName() != "Lovelace" {
		t.Fatalf("unexpected name mapping: %q %q", resp.GetFirstName(), resp.GetLastName())
	}
	if resp.GetRole() != "owner" {
		t.Fatalf("unexpected role: %q", resp.GetRole())
	}
	if len(resp.GetPermissions()) != 2 || resp.GetPermissions()[0] != "read" || resp.GetPermissions()[1] != "write" {
		t.Fatalf("unexpected permissions: %#v", resp.GetPermissions())
	}
	if resp.GetCreatedAt() == nil || resp.GetUpdatedAt() == nil || resp.GetLastLoginAt() == nil {
		t.Fatal("expected created_at/updated_at/last_login_at to be populated")
	}
	if len(resp.GetWallets()) != 1 {
		t.Fatalf("expected one wallet, got %d", len(resp.GetWallets()))
	}
	if resp.GetWallets()[0].GetId() != "wallet-1" || resp.GetWallets()[0].GetWalletAddress() != "0xabc" {
		t.Fatalf("unexpected wallet mapping: %+v", resp.GetWallets()[0])
	}
	if resp.GetWallets()[0].GetLastAuthAt() == nil {
		t.Fatal("expected wallet last_auth_at to be mapped")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRefreshToken_MapsInlineUserStructToAuthResponse(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret-for-refresh-token")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	refreshToken := "refresh-token-raw"
	createdAt := time.Unix(1_700_100_000, 0).UTC()
	updatedAt := createdAt.Add(15 * time.Minute)

	mock.ExpectQuery("FROM commodore.refresh_tokens").
		WithArgs(hashToken(refreshToken)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "revoked"}).
			AddRow("rt-1", "user-2", "tenant-2", false))

	mock.ExpectExec("UPDATE commodore.refresh_tokens SET revoked = true WHERE id = \\$1").
		WithArgs("rt-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectQuery("FROM commodore.users WHERE id = \\$1 AND tenant_id = \\$2").
		WithArgs("user-2", "tenant-2").
		WillReturnRows(sqlmock.NewRows([]string{
			"email", "role", "permissions", "first_name", "last_name",
			"is_active", "verified", "created_at", "updated_at",
		}).AddRow(
			"refresh@example.com",
			"member",
			"{billing_read,billing_write}",
			"Grace",
			"Hopper",
			true,
			true,
			createdAt,
			updatedAt,
		))

	mock.ExpectExec("INSERT INTO commodore.refresh_tokens").
		WithArgs("tenant-2", "user-2", sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	resp, err := server.RefreshToken(context.Background(), &pb.RefreshTokenRequest{RefreshToken: refreshToken})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.GetToken() == "" || resp.GetRefreshToken() == "" {
		t.Fatal("expected access token and refresh token in response")
	}
	if resp.GetUser() == nil {
		t.Fatal("expected user in response")
	}
	if resp.GetUser().GetId() != "user-2" || resp.GetUser().GetTenantId() != "tenant-2" {
		t.Fatalf("unexpected identity mapping: %+v", resp.GetUser())
	}
	if resp.GetUser().GetEmail() != "refresh@example.com" {
		t.Fatalf("unexpected email mapping: %q", resp.GetUser().GetEmail())
	}
	if len(resp.GetUser().GetPermissions()) != 2 {
		t.Fatalf("unexpected permission count: %d", len(resp.GetUser().GetPermissions()))
	}
	if resp.GetUser().GetCreatedAt() == nil || resp.GetUser().GetUpdatedAt() == nil {
		t.Fatal("expected created_at/updated_at to be mapped")
	}
	if resp.GetExpiresAt() == nil {
		t.Fatal("expected expires_at to be set")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
