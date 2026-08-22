//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAccountSessionRepository_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	q := New(db)
	ctx := context.Background()
	const (
		tenantID      = "10000000-0000-4000-8000-000000000041"
		otherTenantID = "10000000-0000-4000-8000-000000000042"
		userID        = "20000000-0000-4000-8000-000000000041"
		walletUserID  = "20000000-0000-4000-8000-000000000042"
		walletAddress = "0xcontractwallet"
	)

	if err := q.InsertRegisteredUser(ctx, InsertRegisteredUserParams{
		ID:                userID,
		TenantID:          tenantID,
		Email:             sql.NullString{String: "account@example.com", Valid: true},
		PasswordHash:      sql.NullString{String: "password-hash", Valid: true},
		FirstName:         sql.NullString{String: "Account", Valid: true},
		LastName:          sql.NullString{String: "Owner", Valid: true},
		Role:              "owner",
		Permissions:       []string{"streams:read", "streams:write"},
		VerificationToken: sql.NullString{String: "verification-hash", Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("insert registered user: %v", err)
	}
	if count, err := q.CountUsersForTenant(ctx, tenantID); err != nil || count != 1 {
		t.Fatalf("tenant user count=%d err=%v", count, err)
	}
	if found, err := q.FindUserIDByEmail(ctx, sql.NullString{String: "ACCOUNT@example.com", Valid: true}); err != nil || found != userID {
		t.Fatalf("case-insensitive email lookup=%q err=%v", found, err)
	}
	login, err := q.GetLoginUserByEmail(ctx, sql.NullString{String: "account@example.com", Valid: true})
	if err != nil || login.ID != userID || login.PasswordHash != "password-hash" || login.Role != "owner" || len(login.Permissions) != 2 {
		t.Fatalf("login row=%#v err=%v", login, err)
	}
	if _, err := q.GetUserProfile(ctx, GetUserProfileParams{ID: userID, TenantID: otherTenantID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant profile lookup err=%v, want no rows", err)
	}
	profile, err := q.GetUserProfile(ctx, GetUserProfileParams{ID: userID, TenantID: tenantID})
	if err != nil || profile.Email != "account@example.com" || profile.Role != "owner" {
		t.Fatalf("profile row=%#v err=%v", profile, err)
	}
	if err := q.TouchUserLastLogin(ctx, TouchUserLastLoginParams{ID: userID, TenantID: tenantID}); err != nil {
		t.Fatal(err)
	}

	if err := q.InsertWalletUser(ctx, InsertWalletUserParams{
		ID: walletUserID, TenantID: tenantID,
		FirstName: sql.NullString{String: "Wallet User", Valid: true},
	}); err != nil {
		t.Fatalf("insert wallet user: %v", err)
	}
	if err := q.InsertWalletIdentity(ctx, InsertWalletIdentityParams{
		WalletAddress: walletAddress, ChainType: "ethereum", TenantID: tenantID, UserID: walletUserID,
	}); err != nil {
		t.Fatalf("insert wallet identity: %v", err)
	}
	wallet, err := q.GetWalletIdentityByAddress(ctx, GetWalletIdentityByAddressParams{
		ChainType: "ethereum", WalletAddress: walletAddress,
	})
	if err != nil || wallet.TenantID != tenantID || wallet.UserID != walletUserID {
		t.Fatalf("wallet lookup=%#v err=%v", wallet, err)
	}
	if err := q.TouchWalletIdentityAuth(ctx, TouchWalletIdentityAuthParams{
		ChainType: "ethereum", WalletAddress: walletAddress,
	}); err != nil {
		t.Fatal(err)
	}
	wallets, err := q.ListUserWallets(ctx, walletUserID)
	if err != nil || len(wallets) != 1 || !wallets[0].CreatedAt.Valid || !wallets[0].LastAuthAt.Valid {
		t.Fatalf("wallet list=%#v err=%v", wallets, err)
	}
	walletProfile, err := q.GetUserProfile(ctx, GetUserProfileParams{ID: walletUserID, TenantID: tenantID})
	if err != nil || walletProfile.Email != "" || walletProfile.Role != "owner" {
		t.Fatalf("nullable wallet profile=%#v err=%v", walletProfile, err)
	}

	oldExpiry := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	if err := q.InsertRefreshToken(ctx, InsertRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: "old-hash", ExpiresAt: oldExpiry,
	}); err != nil {
		t.Fatalf("insert refresh token: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	txq := New(tx)
	locked, err := txq.LockRefreshTokenByHash(ctx, "old-hash")
	if err != nil || locked.UserID != userID || locked.TenantID != tenantID || locked.Revoked {
		_ = tx.Rollback()
		t.Fatalf("lock refresh token=%#v err=%v", locked, err)
	}
	refreshUser, err := txq.GetRefreshUser(ctx, GetRefreshUserParams{ID: userID, TenantID: tenantID})
	if err != nil || refreshUser.Email != "account@example.com" || !refreshUser.IsActive {
		_ = tx.Rollback()
		t.Fatalf("refresh user=%#v err=%v", refreshUser, err)
	}
	successorID, err := txq.InsertRotatedRefreshToken(ctx, InsertRotatedRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: "successor-hash", ExpiresAt: oldExpiry,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := txq.RotateRefreshToken(ctx, RotateRefreshTokenParams{
		ID: locked.ID, ReplacedBy: sql.NullString{String: successorID, Valid: true},
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	txq = New(tx)
	rotated, err := txq.LockRefreshTokenByHash(ctx, "old-hash")
	if err != nil || !rotated.Revoked || !rotated.RotatedAt.Valid || rotated.ReplacedBy.String != successorID {
		_ = tx.Rollback()
		t.Fatalf("rotated token=%#v err=%v", rotated, err)
	}
	if used, err := txq.GetRefreshTokenSuccessorState(ctx, successorID); err != nil || used {
		_ = tx.Rollback()
		t.Fatalf("unused successor state=%v err=%v", used, err)
	}
	recoveryID, err := txq.InsertRotatedRefreshToken(ctx, InsertRotatedRefreshTokenParams{
		TenantID: tenantID, UserID: userID, TokenHash: "recovery-hash", ExpiresAt: oldExpiry,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := txq.RevokeRefreshTokenByID(ctx, successorID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := txq.RelinkRefreshToken(ctx, RelinkRefreshTokenParams{
		ID: rotated.ID, ReplacedBy: sql.NullString{String: recoveryID, Valid: true},
	}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := q.RevokeRefreshTokensForUser(ctx, RevokeRefreshTokensForUserParams{UserID: userID, TenantID: otherTenantID}); err != nil {
		t.Fatal(err)
	}
	if active, err := q.GetRefreshTokenSuccessorState(ctx, recoveryID); err != nil || active {
		t.Fatalf("cross-tenant revoke changed recovery token: revoked=%v err=%v", active, err)
	}
	if err := q.DeleteRefreshTokensForUser(ctx, DeleteRefreshTokensForUserParams{UserID: userID, TenantID: tenantID}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.LockRefreshTokenByHash(ctx, "recovery-hash"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("logout cleanup lookup err=%v, want no rows", err)
	}
}
