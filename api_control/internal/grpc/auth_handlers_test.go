package grpc

import (
	"context"
	"database/sql"
	"github.com/yugabyte/pgx/v5/pgconn"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/turnstile"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type recoveryTurnstileStub struct {
	response *turnstile.VerifyResponse
	err      error
	token    string
	remoteIP string
}

func (s *recoveryTurnstileStub) Verify(_ context.Context, token, remoteIP string) (*turnstile.VerifyResponse, error) {
	s.token = token
	s.remoteIP = remoteIP
	return s.response, s.err
}

// goodBehavior is a bot-check payload that passes validateBehavior: a clicked
// human checkbox, no honeypot, and a >=3s human-plausible interaction.
func goodBehavior() *commodorepb.BehaviorData {
	return &commodorepb.BehaviorData{FormShownAt: 0, SubmittedAt: 5000, Mouse: true}
}

func TestRegister(t *testing.T) {
	t.Run("missing_credentials", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.Register(context.Background(), &commodorepb.RegisterRequest{Email: "a@b.com"})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Turnstile is unconfigured (nil) in tests, so the behavioral fallback is
	// the active bot gate; a request without the human-check must be rejected.
	t.Run("behavioral_bot_check_fails", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.Register(context.Background(), &commodorepb.RegisterRequest{
			Email: "a@b.com", Password: "pw",
		})
		wantCode(t, err, codes.PermissionDenied)
	})

	// Existing email is a soft failure (Success=false), not an error — the
	// surface must not leak whether registration errored vs. collided.
	t.Run("existing_user_soft_fails", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT id FROM commodore.users WHERE email").
			WithArgs("a@b.com").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("existing-id"))
		resp, err := s.Register(context.Background(), &commodorepb.RegisterRequest{
			Email: " A@B.COM ", Password: "pw", HumanCheck: "human", Behavior: goodBehavior(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetSuccess() {
			t.Errorf("Success = true, want false for existing user")
		}
	})

	// A concurrent duplicate registration loses the unique email insert. Production runs the pgx driver, so the
	// violation arrives as *pgconn.PgError and must converge like the lib/pq form did.
	t.Run("concurrent_duplicate_converges_under_pgx", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT id FROM commodore.users WHERE email").
			WithArgs("new@example.com").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("COUNT").
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO commodore.users").
			WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"})
		mock.ExpectRollback()

		resp, err := s.Register(context.Background(), &commodorepb.RegisterRequest{
			Email: "new@example.com", Password: "pw", HumanCheck: "human", Behavior: goodBehavior(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetSuccess() {
			t.Errorf("Success = false, want the converged response")
		}
	})

	// Happy path with all cross-service clients nil: quartermaster nil →
	// generated tenant id; first user of a fresh tenant becomes owner.
	t.Run("happy_path_creates_owner", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("SELECT id FROM commodore.users WHERE email").
			WithArgs("new@example.com").
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery("COUNT").
			WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO commodore.users").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "new@example.com", sqlmock.AnyArg(),
				"", "", "owner", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		// The verification email obligation commits with the user row.
		mock.ExpectExec("INSERT INTO commodore.account_email_outbox").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), accountEmailPurposeVerification).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectLegacyEventInsert(mock, eventAuthRegistered)
		mock.ExpectCommit()

		resp, err := s.Register(context.Background(), &commodorepb.RegisterRequest{
			Email: "new@example.com", Password: "pw", HumanCheck: "human", Behavior: goodBehavior(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resp.GetSuccess() {
			t.Errorf("Success = false, want true")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestVerifyEmailKeepsIdentityPendingUntilFreeSetupSucceeds(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectQuery("SELECT id, tenant_id FROM commodore.users").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id"}).AddRow("user-1", "tenant-1"))

	_, err := s.VerifyEmail(context.Background(), &commodorepb.VerifyEmailRequest{Token: "valid-token"})
	wantCode(t, err, codes.Unavailable)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("verification must not clear the token before Free setup: %v", err)
	}
}

func TestResendVerificationDoesNotRevealAccountState(t *testing.T) {
	tests := []struct {
		name string
		rows *sqlmock.Rows
		err  error
	}{
		{name: "unknown", err: sql.ErrNoRows},
		{name: "verified", rows: sqlmock.NewRows([]string{"id", "tenant_id", "verified", "token_expires_at"}).AddRow("user-1", "tenant-1", true, nil)},
		{name: "cooldown", rows: sqlmock.NewRows([]string{"id", "tenant_id", "verified", "token_expires_at"}).AddRow("user-1", "tenant-1", false, time.Now().Add(24*time.Hour))},
	}
	var wantMessage string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			expectation := mock.ExpectQuery("SELECT id, tenant_id").WithArgs("user@example.com")
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			resp, err := s.ResendVerification(context.Background(), &commodorepb.ResendVerificationRequest{Email: "user@example.com"})
			if err != nil {
				t.Fatal(err)
			}
			if !resp.GetSuccess() {
				t.Fatalf("response = %#v, want generic success", resp)
			}
			if wantMessage == "" {
				wantMessage = resp.GetMessage()
			} else if resp.GetMessage() != wantMessage {
				t.Fatalf("message = %q, want %q", resp.GetMessage(), wantMessage)
			}
		})
	}
}

func TestResendVerificationCooldownIsAtomic(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery("SELECT id, tenant_id").
		WithArgs("user@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "verified", "token_expires_at"}).AddRow("user-1", "tenant-1", false, time.Now()))
	// A concurrent resend already stamped the cooldown: nothing is queued.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE commodore.users.*token_expires_at <=").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	resp, err := s.ResendVerification(context.Background(), &commodorepb.ResendVerificationRequest{Email: "user@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetSuccess() || !strings.Contains(resp.GetMessage(), "if an account exists") {
		t.Fatalf("response = %#v, want generic success", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A resend that wins the cooldown queues delivery in the same transaction, so
// an SMTP failure leaves a retried obligation instead of a lost email.
func TestResendVerificationQueuesDeliveryWithCooldown(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	kicked := false
	s.accountEmailKickFn = func() { kicked = true }
	mock.ExpectQuery("SELECT id, tenant_id").
		WithArgs("user@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "verified", "token_expires_at"}).AddRow("user-1", "tenant-1", false, time.Now().Add(-time.Hour)))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE commodore.users.*token_expires_at <=").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO commodore.account_email_outbox").
		WithArgs("user-1", "tenant-1", accountEmailPurposeVerification).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	resp, err := s.ResendVerification(context.Background(), &commodorepb.ResendVerificationRequest{Email: "user@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetSuccess() {
		t.Fatalf("response = %#v, want generic success", resp)
	}
	if !kicked {
		t.Fatal("queued verification email was not handed to the outbox for immediate delivery")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestForgotPasswordPersistsPerAccountCooldown(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery("SELECT id FROM commodore.users WHERE email").
		WithArgs("user@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("user-1"))
	mock.ExpectExec("UPDATE commodore.users.*reset_token_expires <=").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "user-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := s.ForgotPassword(context.Background(), &commodorepb.ForgotPasswordRequest{Email: "user@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetSuccess() || !strings.Contains(resp.GetMessage(), "if an account exists") {
		t.Fatalf("response = %#v, want generic success", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResetPasswordRejectsTokenConsumedDuringHashing(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery("SELECT id FROM commodore.users WHERE reset_token").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("user-1"))
	mock.ExpectExec("UPDATE commodore.users.*reset_token =").
		WithArgs(sqlmock.AnyArg(), "user-1", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))

	resp, err := s.ResetPassword(context.Background(), &commodorepb.ResetPasswordRequest{
		Token:    "already-consumed",
		Password: "new password with enough entropy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetSuccess() || !strings.Contains(resp.GetMessage(), "invalid or expired") {
		t.Fatalf("response = %#v, want consumed-token rejection", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryTurnstileIsRequiredWhenConfigured(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	validator := &recoveryTurnstileStub{response: &turnstile.VerifyResponse{Success: true}}
	s.turnstileValidator = validator

	_, err := s.ForgotPassword(context.Background(), &commodorepb.ForgotPasswordRequest{Email: "user@example.com"})
	wantCode(t, err, codes.PermissionDenied)

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-client-ip", "203.0.113.10"))
	mock.ExpectQuery("SELECT id FROM commodore.users WHERE email").
		WithArgs("user@example.com").
		WillReturnError(sql.ErrNoRows)
	resp, err := s.ForgotPassword(ctx, &commodorepb.ForgotPasswordRequest{Email: "user@example.com", TurnstileToken: "proof"})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("response = %#v, error = %v", resp, err)
	}
	if validator.token != "proof" || validator.remoteIP != "203.0.113.10" {
		t.Fatalf("validator received token %q and IP %q", validator.token, validator.remoteIP)
	}
}

func TestGetOrCreateWalletUser(t *testing.T) {
	const ethAddr = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"

	t.Run("invalid_chain_type", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetOrCreateWalletUser(context.Background(), &commodorepb.GetOrCreateWalletUserRequest{
			ChainType: "dogecoin", WalletAddress: ethAddr,
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("invalid_address", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.GetOrCreateWalletUser(context.Background(), &commodorepb.GetOrCreateWalletUserRequest{
			ChainType: string(auth.ChainEthereum), WalletAddress: "not-an-address",
		})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Existing wallet: an unavailable Purser must not become implicit postpaid
	// credit; the identity still resolves as prepaid for fail-closed admission.
	t.Run("existing_wallet_resolves", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.wallet_identities").
			WithArgs(string(auth.ChainEthereum), sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id"}).AddRow("tn-1", "us-1"))
		mock.ExpectExec("UPDATE commodore.wallet_identities").
			WithArgs(string(auth.ChainEthereum), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))

		resp, err := s.GetOrCreateWalletUser(context.Background(), &commodorepb.GetOrCreateWalletUserRequest{
			ChainType: string(auth.ChainEthereum), WalletAddress: ethAddr,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetIsNew() {
			t.Errorf("IsNew = true, want false for existing wallet")
		}
		if resp.GetBillingModel() != "prepaid" {
			t.Errorf("BillingModel = %q, want prepaid (purser unavailable default)", resp.GetBillingModel())
		}
		if resp.GetTenantId() != "tn-1" || resp.GetUserId() != "us-1" {
			t.Errorf("ids = (%s,%s), want (tn-1,us-1)", resp.GetTenantId(), resp.GetUserId())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})

	// New wallet creation depends on Quartermaster (tenant) — when it is
	// unavailable the handler must fail rather than half-provision.
	t.Run("new_wallet_requires_quartermaster", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectQuery("FROM commodore.wallet_identities").
			WithArgs(string(auth.ChainEthereum), sqlmock.AnyArg()).
			WillReturnError(sql.ErrNoRows)
		_, err := s.GetOrCreateWalletUser(context.Background(), &commodorepb.GetOrCreateWalletUserRequest{
			ChainType: string(auth.ChainEthereum), WalletAddress: ethAddr,
		})
		wantCode(t, err, codes.Internal)
	})
}

func TestIssueAndConsumeWalletChallenge(t *testing.T) {
	const address = "0xd8da6bf26964af9d7eed9e03e53415d37aa96045"
	s, mock, done := newMockServer(t)
	defer done()
	s.runtimeSettings = func() RuntimeSettings {
		return RuntimeSettings{Branding: config.EmailBranding{WebAppURL: "https://app.example.com"}}
	}

	mock.ExpectExec("INSERT INTO commodore.wallet_auth_challenges").
		WithArgs(sqlmock.AnyArg(), int64(1), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	challenge, err := s.IssueWalletChallenge(context.Background(), &commodorepb.IssueWalletChallengeRequest{
		WalletAddress: address,
		ChainId:       1,
	})
	if err != nil {
		t.Fatalf("IssueWalletChallenge: %v", err)
	}
	if !strings.Contains(challenge.GetMessage(), "app.example.com wants you to sign in") ||
		!strings.Contains(challenge.GetMessage(), "Chain ID: 1") {
		t.Fatalf("unexpected challenge: %q", challenge.GetMessage())
	}

	mock.ExpectQuery("UPDATE commodore.wallet_auth_challenges").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("challenge-1"))
	if err := s.consumeWalletChallenge(context.Background(), "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045", challenge.GetMessage()); err != nil {
		t.Fatalf("consumeWalletChallenge: %v", err)
	}

	mock.ExpectQuery("UPDATE commodore.wallet_auth_challenges").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	if code := status.Code(s.consumeWalletChallenge(context.Background(), "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045", challenge.GetMessage())); code != codes.Unauthenticated {
		t.Fatalf("replay code = %v, want Unauthenticated", code)
	}
}

func walletUserContext() context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	return context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
}

func TestUnlinkWalletPreservesSigninMethod(t *testing.T) {
	t.Run("wallet-only account cannot remove final wallet", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"has_password"}).AddRow(false))
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
		mock.ExpectQuery("FROM commodore.wallet_identities\\s+WHERE user_id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectRollback()

		_, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
		wantCode(t, err, codes.FailedPrecondition)
		if !strings.Contains(err.Error(), "final wallet") {
			t.Fatalf("error = %v, want final-wallet guidance", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("wallet-only account may remove one of multiple wallets", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"has_password"}).AddRow(false))
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
		mock.ExpectQuery("FROM commodore.wallet_identities\\s+WHERE user_id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
		mock.ExpectQuery("DELETE FROM commodore.wallet_identities").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wallet-1"))
		expectLegacyEventInsert(mock, eventWalletUnlinked)
		mock.ExpectCommit()

		resp, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
		if err != nil || !resp.GetSuccess() {
			t.Fatalf("unlink = (%+v, %v)", resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("password account may remove final wallet", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.users\\s+WHERE id").
			WithArgs("user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"has_password"}).AddRow(true))
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"owned"}).AddRow(true))
		mock.ExpectQuery("DELETE FROM commodore.wallet_identities").
			WithArgs("wallet-1", "user-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("wallet-1"))
		expectLegacyEventInsert(mock, eventWalletUnlinked)
		mock.ExpectCommit()

		resp, err := s.UnlinkWallet(walletUserContext(), &commodorepb.UnlinkWalletRequest{WalletId: "wallet-1"})
		if err != nil || !resp.GetSuccess() {
			t.Fatalf("unlink = (%+v, %v)", resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWalletChallengeOriginAllowed(t *testing.T) {
	for _, raw := range []string{
		"https://app.example.com",
		"http://localhost:18090/app",
		"http://127.0.0.1:18090",
		"http://[::1]:18090",
	} {
		origin, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !walletChallengeOriginAllowed(origin, true) {
			t.Fatalf("expected %q to be allowed", raw)
		}
	}

	for _, raw := range []string{"http://example.com", "ftp://localhost", "https:///missing-host"} {
		origin, _ := url.Parse(raw)
		if walletChallengeOriginAllowed(origin, true) {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}

	loopback, _ := url.Parse("http://localhost:18090/app")
	if walletChallengeOriginAllowed(loopback, false) {
		t.Fatal("production must reject an insecure loopback origin")
	}
}

func TestStartDeviceAuthorization(t *testing.T) {
	t.Run("missing_client_id", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.StartDeviceAuthorization(context.Background(), &commodorepb.StartDeviceAuthorizationRequest{})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Fail-closed allowlist: only known device-grant clients are accepted.
	t.Run("unknown_client_id_denied", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.StartDeviceAuthorization(context.Background(), &commodorepb.StartDeviceAuthorizationRequest{ClientId: "evil-app"})
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("happy_persists_pending_code", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		s.runtimeSettings = func() RuntimeSettings {
			return RuntimeSettings{Branding: config.EmailBranding{WebAppURL: "https://app.example.com"}}
		}
		mock.ExpectExec("INSERT INTO commodore.auth_device_codes").
			WithArgs("cli", sqlmock.AnyArg(), sqlmock.AnyArg(), "account", sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))
		resp, err := s.StartDeviceAuthorization(context.Background(), &commodorepb.StartDeviceAuthorizationRequest{ClientId: "cli"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetDeviceCode() == "" || resp.GetUserCode() == "" {
			t.Errorf("device/user code must be non-empty: %+v", resp)
		}
		if resp.GetVerificationUriComplete() == resp.GetVerificationUri() {
			t.Errorf("complete URI must carry the user_code query param")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("unmet: %v", err)
		}
	})
}

func TestPollDeviceAuthorization(t *testing.T) {
	deviceCodeCols := []string{"id", "client_id", "status", "user_id", "tenant_id", "expires_at", "last_polled_at", "poll_interval_seconds"}
	const devCode = "dev-code-1"
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	t.Run("missing_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{ClientId: "cli"})
		wantCode(t, err, codes.InvalidArgument)
	})

	// Unknown device_code reveals nothing — RFC 8628 ACCESS_DENIED.
	t.Run("unknown_code_access_denied", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.auth_device_codes").
			WithArgs(hashToken(devCode)).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{
			DeviceCode: devCode, ClientId: "cli",
		})
		wantCode(t, err, codes.PermissionDenied)
	})

	// A code issued to a different client must not be pollable by another.
	t.Run("client_mismatch_access_denied", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.auth_device_codes").
			WithArgs(hashToken(devCode)).
			WillReturnRows(sqlmock.NewRows(deviceCodeCols).
				AddRow("row1", "tray-mac", "pending", nil, nil, future, nil, 5))
		mock.ExpectRollback()
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{
			DeviceCode: devCode, ClientId: "cli",
		})
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("pending_returns_authorization_pending", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.auth_device_codes").
			WithArgs(hashToken(devCode)).
			WillReturnRows(sqlmock.NewRows(deviceCodeCols).
				AddRow("row1", "cli", "pending", nil, nil, future, nil, 5))
		mock.ExpectExec("SET last_polled_at").WithArgs("row1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{
			DeviceCode: devCode, ClientId: "cli",
		})
		wantCode(t, err, codes.FailedPrecondition)
	})

	// A poll whose write aborts with 40001 replays and still answers authorization_pending.
	t.Run("pending_poll_replays_serialization_failure", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		for attempt := 0; attempt < 2; attempt++ {
			mock.ExpectBegin()
			mock.ExpectQuery("FROM commodore.auth_device_codes").
				WithArgs(hashToken(devCode)).
				WillReturnRows(sqlmock.NewRows(deviceCodeCols).
					AddRow("row1", "cli", "pending", nil, nil, future, nil, 5))
			poll := mock.ExpectExec("SET last_polled_at").WithArgs("row1")
			if attempt == 0 {
				poll.WillReturnError(serializationFailure())
				mock.ExpectRollback()
				continue
			}
			poll.WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
		}
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{
			DeviceCode: devCode, ClientId: "cli",
		})
		wantCode(t, err, codes.FailedPrecondition)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet: %v", err)
		}
	})

	t.Run("expired_code_marks_and_reports", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("FROM commodore.auth_device_codes").
			WithArgs(hashToken(devCode)).
			WillReturnRows(sqlmock.NewRows(deviceCodeCols).
				AddRow("row1", "cli", "pending", nil, nil, past, nil, 5))
		mock.ExpectExec("status = 'expired'").WithArgs("row1").WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		_, err := s.PollDeviceAuthorization(context.Background(), &commodorepb.PollDeviceAuthorizationRequest{
			DeviceCode: devCode, ClientId: "cli",
		})
		wantCode(t, err, codes.FailedPrecondition)
	})
}

// WalletLogin happy paths require a verified signature; these cover the
// deterministic guard rails before that point.
func TestWalletLoginGuards(t *testing.T) {
	t.Run("walletlogin_missing_fields", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.WalletLogin(context.Background(), &commodorepb.WalletLoginRequest{WalletAddress: "0xabc"})
		wantCode(t, err, codes.InvalidArgument)
	})

	t.Run("walletlogin_bad_signature_rejected", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.WalletLogin(context.Background(), &commodorepb.WalletLoginRequest{
			WalletAddress: "0xd8da6bf26964af9d7eed9e03e53415d37aa96045",
			Message:       "login",
			Signature:     "0xnonsense",
		})
		if code := status.Code(err); code != codes.InvalidArgument && code != codes.Unauthenticated {
			t.Errorf("bad signature: code = %v, want InvalidArgument or Unauthenticated", code)
		}
	})
}
