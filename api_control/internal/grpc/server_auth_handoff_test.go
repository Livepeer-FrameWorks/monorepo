package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestValidateAuthorizationClient locks the loopback-only redirect_uri rule
// for the tray, and the fail-closed behaviour for unknown client_ids. Any
// drift here is an open-redirect / unauthorized-client class of bug.
func TestValidateAuthorizationClient(t *testing.T) {
	tests := []struct {
		name        string
		clientID    string
		redirectURI string
		wantCode    codes.Code
	}{
		{"tray-mac loopback v4", "tray-mac", "http://127.0.0.1:54321/callback", codes.OK},
		{"tray-mac loopback v6", "tray-mac", "http://[::1]:54321/callback", codes.OK},
		{"tray-mac https rejected", "tray-mac", "https://127.0.0.1/callback", codes.InvalidArgument},
		{"tray-mac non-loopback rejected", "tray-mac", "http://example.com/callback", codes.InvalidArgument},
		{"tray-mac wrong path", "tray-mac", "http://127.0.0.1:54321/oauth", codes.InvalidArgument},
		{"tray-mac garbage uri", "tray-mac", "::not-a-url", codes.InvalidArgument},
		{"unknown client_id fails closed", "rogue-client", "http://127.0.0.1:54321/callback", codes.PermissionDenied},
		{"empty client_id fails closed", "", "http://127.0.0.1:54321/callback", codes.PermissionDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAuthorizationClient(tc.clientID, tc.redirectURI)
			if tc.wantCode == codes.OK {
				if err != nil {
					t.Fatalf("expected OK, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error %v, got nil", tc.wantCode)
			}
			if got := status.Code(err); got != tc.wantCode {
				t.Fatalf("expected code %v, got %v (%v)", tc.wantCode, got, err)
			}
		})
	}
}

// TestNormalizeUserCode locks the input-normalization rules for the webapp
// /device form: users typing "abcd efgh", "abcdefgh", or "ABCD-EFGH" all
// resolve to the same canonical row. Anything not exactly 8 alphanumeric
// chars normalizes to "" and the lookup returns NotFound.
func TestNormalizeUserCode(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ABCD-EFGH", "ABCD-EFGH"},
		{"abcd-efgh", "ABCD-EFGH"},
		{"abcdefgh", "ABCD-EFGH"},
		{"abcd efgh", "ABCD-EFGH"},
		{"  ABCD-EFGH  ", "ABCD-EFGH"},
		{"ABCD.EFGH", "ABCD-EFGH"},
		{"12345678", "1234-5678"},
		// Reject anything that isn't exactly 8 alphanumerics after stripping.
		{"ABCD-EFG", ""},
		{"ABCD-EFGHI", ""},
		{"", ""},
		{"!!!!!!!!", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			if got := normalizeUserCode(tc.input); got != tc.want {
				t.Fatalf("normalizeUserCode(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestGenerateUserCode_FormatAndAlphabet locks the user_code shape: 9 chars
// total ("ABCD-EFGH"), dash at position 4, all other positions drawn from
// the Crockford alphabet (no I/L/O/U so users can read aloud without
// transcription errors).
func TestGenerateUserCode_FormatAndAlphabet(t *testing.T) {
	const allowed = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for range 64 {
		code, err := generateUserCode()
		if err != nil {
			t.Fatalf("generateUserCode: %v", err)
		}
		if len(code) != 9 || code[4] != '-' {
			t.Fatalf("unexpected user_code shape: %q", code)
		}
		for j, r := range code {
			if j == 4 {
				continue
			}
			if !strings.ContainsRune(allowed, r) {
				t.Fatalf("user_code %q contains forbidden char %q at %d", code, r, j)
			}
		}
	}
}

func TestDeviceVerificationBaseURLUsesConfiguredWebappURL(t *testing.T) {
	t.Setenv("DEVICE_VERIFICATION_URL", "")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://chartroom.frameworks.network/app/")

	server := &CommodoreServer{logger: logrus.New()}
	got, err := server.deviceVerificationBaseURL()
	if err != nil {
		t.Fatalf("deviceVerificationBaseURL: %v", err)
	}
	if want := "https://chartroom.frameworks.network/app/device"; got != want {
		t.Fatalf("deviceVerificationBaseURL() = %q, want %q", got, want)
	}
}

func TestDeviceVerificationBaseURLOverride(t *testing.T) {
	t.Setenv("DEVICE_VERIFICATION_URL", "https://login.example.com/device/")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://chartroom.frameworks.network/app")

	server := &CommodoreServer{logger: logrus.New()}
	got, err := server.deviceVerificationBaseURL()
	if err != nil {
		t.Fatalf("deviceVerificationBaseURL: %v", err)
	}
	if want := "https://login.example.com/device"; got != want {
		t.Fatalf("deviceVerificationBaseURL() = %q, want %q", got, want)
	}
}

// TestExchangeAuthorizationCode_VerifierMismatch is the security primitive
// of PKCE: an attacker who intercepts the authorization code on the loopback
// redirect cannot redeem it without the verifier held only by the originating
// native process. Stored challenge = base64url(SHA256(verifier)); a wrong
// verifier must fail with PermissionDenied (no token issuance).
func TestExchangeAuthorizationCode_VerifierMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rawCode := "test-code-raw"
	codeHash := hashToken(rawCode)
	realVerifier := "real-verifier-that-belongs-to-the-tray"
	h := sha256.Sum256([]byte(realVerifier))
	storedChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.auth_authorization_codes").
		WithArgs(codeHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "code_challenge", "code_challenge_method",
			"client_id", "redirect_uri", "consumed_at",
		}).AddRow(
			"authz-1", "user-1", "tenant-1", storedChallenge, "S256",
			"tray-mac", "http://127.0.0.1:54321/callback", nil,
		))
	mock.ExpectRollback()

	server := &CommodoreServer{db: db, logger: logrus.New()}

	_, err = server.ExchangeAuthorizationCode(context.Background(), &pb.ExchangeAuthorizationCodeRequest{
		Code:         rawCode,
		CodeVerifier: "WRONG-verifier-does-not-match-stored-challenge",
		ClientId:     "tray-mac",
		RedirectUri:  "http://127.0.0.1:54321/callback",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied, got nil error (verifier mismatch should never succeed)")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected codes.PermissionDenied, got %v (%v)", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestExchangeAuthorizationCode_AlreadyConsumed locks the single-use
// invariant: a successfully redeemed authorization code cannot be redeemed
// again. Without this guard, an attacker who replays a stolen code (e.g.
// from process memory) could mint a second session.
func TestExchangeAuthorizationCode_AlreadyConsumed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	rawCode := "consumed-code"
	codeHash := hashToken(rawCode)
	consumedAt := time.Now().Add(-1 * time.Minute)

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.auth_authorization_codes").
		WithArgs(codeHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "code_challenge", "code_challenge_method",
			"client_id", "redirect_uri", "consumed_at",
		}).AddRow(
			"authz-1", "user-1", "tenant-1", "any-challenge", "S256",
			"tray-mac", "http://127.0.0.1:54321/callback", consumedAt,
		))
	mock.ExpectRollback()

	server := &CommodoreServer{db: db, logger: logrus.New()}

	_, err = server.ExchangeAuthorizationCode(context.Background(), &pb.ExchangeAuthorizationCodeRequest{
		Code:         rawCode,
		CodeVerifier: "any",
		ClientId:     "tray-mac",
		RedirectUri:  "http://127.0.0.1:54321/callback",
	})
	if err == nil {
		t.Fatal("expected AlreadyExists, got nil (replay must be rejected)")
	}
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("expected codes.AlreadyExists, got %v (%v)", status.Code(err), err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}

// TestCompleteAuthorization_RequiresSession verifies that the handler
// refuses to mint an authorization code without a verified session in the
// gRPC metadata. The webapp must never be able to mint codes for arbitrary
// users by lying in the body — identity comes from the session only.
func TestCompleteAuthorization_RequiresSession(t *testing.T) {
	server := &CommodoreServer{logger: logrus.New()}

	// No ctxkeys.KeyUserID / KeyTenantID on the context.
	_, err := server.CompleteAuthorization(context.Background(), &pb.CompleteAuthorizationRequest{
		ClientId:            "tray-mac",
		RedirectUri:         "http://127.0.0.1:54321/callback",
		CodeChallenge:       "any",
		CodeChallengeMethod: "S256",
	})
	if err == nil {
		t.Fatal("expected Unauthenticated, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v (%v)", status.Code(err), err)
	}
}

// TestApproveDeviceAuthorization_RequiresSession is the same fail-closed
// guard for the device-code flow: an unauthenticated caller must not be
// able to approve any user_code.
func TestApproveDeviceAuthorization_RequiresSession(t *testing.T) {
	server := &CommodoreServer{logger: logrus.New()}

	_, err := server.ApproveDeviceAuthorization(context.Background(), &pb.ApproveDeviceAuthorizationRequest{
		UserCode: "ABCD-EFGH",
	})
	if err == nil {
		t.Fatal("expected Unauthenticated, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v (%v)", status.Code(err), err)
	}

	// A populated context but invalid user_code shape should fail with
	// InvalidArgument, not silently match some row.
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	_, err = server.ApproveDeviceAuthorization(ctx, &pb.ApproveDeviceAuthorizationRequest{
		UserCode: "garbage",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

func TestLookupDeviceAuthorization_RequiresSession(t *testing.T) {
	server := &CommodoreServer{logger: logrus.New()}

	_, err := server.LookupDeviceAuthorization(context.Background(), &pb.LookupDeviceAuthorizationRequest{
		UserCode: "ABCD-EFGH",
	})
	if err == nil {
		t.Fatal("expected Unauthenticated, got nil")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v (%v)", status.Code(err), err)
	}

	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")
	_, err = server.LookupDeviceAuthorization(ctx, &pb.LookupDeviceAuthorizationRequest{
		UserCode: "garbage",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v (%v)", status.Code(err), err)
	}
}

func TestLookupDeviceAuthorization_ReturnsPendingClientMetadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	expiresAt := time.Now().Add(5 * time.Minute)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.auth_device_codes").
		WithArgs("ABCD-EFGH").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "client_id", "scope", "status", "expires_at",
		}).AddRow("device-row-1", "cli", "account", "pending", expiresAt))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-1")

	resp, err := server.LookupDeviceAuthorization(ctx, &pb.LookupDeviceAuthorizationRequest{
		UserCode: "abcd efgh",
	})
	if err != nil {
		t.Fatalf("LookupDeviceAuthorization: %v", err)
	}
	if resp.ClientId != "cli" || resp.Scope != "account" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if resp.ExpiresAt.AsTime().Sub(expiresAt).Abs() > time.Second {
		t.Fatalf("expires_at = %v, want near %v", resp.ExpiresAt.AsTime(), expiresAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sql expectations: %v", err)
	}
}
