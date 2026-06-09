package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
			Email: "a@b.com", Password: "pw", HumanCheck: "human", Behavior: goodBehavior(),
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetSuccess() {
			t.Errorf("Success = true, want false for existing user")
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
		mock.ExpectExec("INSERT INTO commodore.users").
			WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), "new@example.com", sqlmock.AnyArg(),
				"", "", "owner", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(0, 1))

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

	// Existing wallet: purser nil → billing defaults to postpaid; the row is
	// resolved and last_auth_at is refreshed.
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
		if resp.GetBillingModel() != "postpaid" {
			t.Errorf("BillingModel = %q, want postpaid (purser nil default)", resp.GetBillingModel())
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
		t.Setenv("WEBAPP_PUBLIC_URL", "https://app.example.com")
		s, mock, done := newMockServer(t)
		defer done()
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

// WalletLogin / WalletLoginWithX402 happy paths require a verified signature /
// settled payment; these cover the deterministic guard rails before that point.
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

	// X402 login is gated on a configured Purser; nil → Unavailable before any
	// payment work.
	t.Run("x402_requires_purser", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.WalletLoginWithX402(context.Background(), &commodorepb.WalletLoginWithX402Request{})
		wantCode(t, err, codes.Unavailable)
	})
}
