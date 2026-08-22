//go:build schema_verify

package commodoredb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestAccountRecoveryWalletRepository_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	q := New(db)
	ctx := context.Background()
	const (
		tenantID      = "10000000-0000-4000-8000-000000000061"
		otherTenantID = "10000000-0000-4000-8000-000000000062"
		userID        = "20000000-0000-4000-8000-000000000061"
		walletUserID  = "20000000-0000-4000-8000-000000000062"
	)
	verificationHash := "verification-hash"
	if err := q.InsertRegisteredUser(ctx, InsertRegisteredUserParams{
		ID: userID, TenantID: tenantID,
		Email:             sql.NullString{String: "recover@example.com", Valid: true},
		PasswordHash:      sql.NullString{String: "old-password", Valid: true},
		FirstName:         sql.NullString{String: "Before", Valid: true},
		LastName:          sql.NullString{String: "Recovery", Valid: true},
		Role:              "owner",
		Permissions:       []string{"streams:read"},
		VerificationToken: sql.NullString{String: verificationHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	verification, err := q.GetVerificationUser(ctx, sql.NullString{String: verificationHash, Valid: true})
	if err != nil || verification.ID != userID || verification.TenantID != tenantID {
		t.Fatalf("verification lookup=%#v err=%v", verification, err)
	}
	if err := q.VerifyUserEmail(ctx, VerifyUserEmailParams{ID: userID, TenantID: otherTenantID}); err != nil {
		t.Fatal(err)
	}
	resend, err := q.GetVerificationResendUser(ctx, sql.NullString{String: "recover@example.com", Valid: true})
	if err != nil || resend.Verified {
		t.Fatalf("cross-tenant verification changed user: %#v err=%v", resend, err)
	}
	if err := q.VerifyUserEmail(ctx, VerifyUserEmailParams{ID: userID, TenantID: tenantID}); err != nil {
		t.Fatal(err)
	}
	resend, err = q.GetVerificationResendUser(ctx, sql.NullString{String: "recover@example.com", Valid: true})
	if err != nil || !resend.Verified || resend.TokenExpiresAt.Valid {
		t.Fatalf("verified user=%#v err=%v", resend, err)
	}

	newVerificationHash := "new-verification-hash"
	if err := q.UpdateVerificationToken(ctx, UpdateVerificationTokenParams{
		VerificationToken: sql.NullString{String: newVerificationHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		ID:                userID,
	}); err != nil {
		t.Fatal(err)
	}
	resetHash := "reset-hash"
	if err := q.SetPasswordResetToken(ctx, SetPasswordResetTokenParams{
		ResetToken: sql.NullString{String: resetHash, Valid: true},
		ResetTokenExpires: sql.NullTime{
			Time: time.Now().Add(time.Hour), Valid: true,
		},
		ID: userID,
	}); err != nil {
		t.Fatal(err)
	}
	if found, err := q.FindUserByResetToken(ctx, sql.NullString{String: resetHash, Valid: true}); err != nil || found != userID {
		t.Fatalf("reset lookup=%q err=%v", found, err)
	}
	if err := q.ResetUserPassword(ctx, ResetUserPasswordParams{
		PasswordHash: sql.NullString{String: "new-password", Valid: true}, ID: userID,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.FindUserByResetToken(ctx, sql.NullString{String: resetHash, Valid: true}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("consumed reset token err=%v", err)
	}

	if err := q.UpdateUserFirstName(ctx, UpdateUserFirstNameParams{
		FirstName: sql.NullString{String: "First", Valid: true}, ID: userID, TenantID: tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateUserLastName(ctx, UpdateUserLastNameParams{
		LastName: sql.NullString{String: "Last", Valid: true}, ID: userID, TenantID: tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpdateUserName(ctx, UpdateUserNameParams{
		FirstName: sql.NullString{String: "Both", Valid: true},
		LastName:  sql.NullString{String: "Names", Valid: true},
		ID:        userID, TenantID: tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	newsletter, err := q.GetNewsletterUser(ctx, GetNewsletterUserParams{ID: userID, TenantID: tenantID})
	if err != nil || newsletter.FirstName.String != "Both" || newsletter.LastName.String != "Names" {
		t.Fatalf("newsletter user=%#v err=%v", newsletter, err)
	}

	messageHash := sha256.Sum256([]byte("wallet challenge"))
	if err := q.InsertWalletChallenge(ctx, InsertWalletChallengeParams{
		WalletAddress: "0xchallenge", ChainID: 1, MessageHash: messageHash[:], ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ConsumeWalletChallenge(ctx, ConsumeWalletChallengeParams{
		WalletAddress: "0xchallenge", MessageHash: messageHash[:],
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ConsumeWalletChallenge(ctx, ConsumeWalletChallengeParams{
		WalletAddress: "0xchallenge", MessageHash: messageHash[:],
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wallet challenge replay err=%v", err)
	}

	if err := q.InsertWalletUser(ctx, InsertWalletUserParams{
		ID: walletUserID, TenantID: tenantID,
		FirstName: sql.NullString{String: "Wallet", Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	linked, err := q.InsertLinkedWallet(ctx, InsertLinkedWalletParams{
		TenantID: tenantID, UserID: walletUserID, WalletAddress: "0xlinked",
	})
	if err != nil || !linked.CreatedAt.Valid {
		t.Fatalf("linked wallet=%#v err=%v", linked, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	txq := New(tx)
	hasPassword, err := txq.LockUserAuthenticationMethods(ctx, LockUserAuthenticationMethodsParams{
		ID: walletUserID, TenantID: tenantID,
	})
	if err != nil || !hasPassword.Valid || hasPassword.Bool {
		_ = tx.Rollback()
		t.Fatalf("wallet-only auth methods=%#v err=%v", hasPassword, err)
	}
	owned, err := txq.UserOwnsWallet(ctx, UserOwnsWalletParams{
		ID: linked.ID, UserID: walletUserID, TenantID: tenantID,
	})
	if err != nil || !owned {
		_ = tx.Rollback()
		t.Fatalf("wallet ownership=%v err=%v", owned, err)
	}
	count, err := txq.CountUserWallets(ctx, CountUserWalletsParams{UserID: walletUserID, TenantID: tenantID})
	if err != nil || count != 1 {
		_ = tx.Rollback()
		t.Fatalf("wallet count=%d err=%v", count, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	linkedEmailHash := "linked-email-verification-hash"
	if err := q.LinkUserEmail(ctx, LinkUserEmailParams{
		Email:             sql.NullString{String: "linked@example.com", Valid: true},
		PasswordHash:      sql.NullString{String: "linked-password", Valid: true},
		VerificationToken: sql.NullString{String: linkedEmailHash, Valid: true},
		TokenExpiresAt:    sql.NullTime{Time: time.Now().Add(time.Hour), Valid: true},
		ID:                walletUserID,
		TenantID:          tenantID,
	}); err != nil {
		t.Fatal(err)
	}
	if found, err := q.GetVerificationUser(ctx, sql.NullString{String: linkedEmailHash, Valid: true}); err != nil || found.ID != walletUserID {
		t.Fatalf("linked-email verification lookup=%#v err=%v", found, err)
	}
	if _, err := q.FindOtherUserIDByEmail(ctx, FindOtherUserIDByEmailParams{
		Email: sql.NullString{String: "linked@example.com", Valid: true}, ID: userID,
	}); err != nil {
		t.Fatalf("other-user email lookup: %v", err)
	}
	if _, err := q.DeleteUserWallet(ctx, DeleteUserWalletParams{
		ID: linked.ID, UserID: walletUserID, TenantID: otherTenantID,
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant wallet delete err=%v", err)
	}
	if _, err := q.DeleteUserWallet(ctx, DeleteUserWalletParams{
		ID: linked.ID, UserID: walletUserID, TenantID: tenantID,
	}); err != nil {
		t.Fatal(err)
	}
}
