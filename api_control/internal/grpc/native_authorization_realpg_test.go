//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNativeAuthorizationRepository_RealPG(t *testing.T) {
	t.Setenv("JWT_SECRET", "native-authorization-realpg-secret")
	t.Setenv("WEBAPP_PUBLIC_URL", "https://console.example.com")
	db := startCommodoreRealPG(t)
	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.Background()
	const (
		tenantID = "10000000-0000-4000-8000-000000000051"
		userID   = "20000000-0000-4000-8000-000000000051"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users (
			id, tenant_id, email, password_hash, role, permissions,
			is_active, verified, first_name, last_name
		)
		VALUES ($1::uuid, $2::uuid, 'native@example.com', 'unused', 'owner',
		        ARRAY['streams:read'], true, true, 'Native', 'User')
	`, userID, tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userCtx := context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyTenantID, tenantID)

	const verifier = "native-pkce-verifier-with-sufficient-entropy"
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])
	completed, err := server.CompleteAuthorization(userCtx, &commodorepb.CompleteAuthorizationRequest{
		ClientId:            "tray-mac",
		RedirectUri:         "http://127.0.0.1:45678/callback",
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		Scope:               "account",
		State:               "opaque-state",
	})
	if err != nil || completed.GetCode() == "" {
		t.Fatalf("complete authorization=%#v err=%v", completed, err)
	}
	var storedCodeHash string
	if err := db.QueryRowContext(ctx, `SELECT code_hash FROM commodore.auth_authorization_codes`).Scan(&storedCodeHash); err != nil {
		t.Fatal(err)
	}
	if storedCodeHash != hashToken(completed.GetCode()) || storedCodeHash == completed.GetCode() {
		t.Fatalf("authorization code not hashed at rest: %q", storedCodeHash)
	}
	exchanged, err := server.ExchangeAuthorizationCode(ctx, &commodorepb.ExchangeAuthorizationCodeRequest{
		Code:         completed.GetCode(),
		CodeVerifier: verifier,
		ClientId:     "tray-mac",
		RedirectUri:  "http://127.0.0.1:45678/callback",
	})
	if err != nil || exchanged.GetToken() == "" || exchanged.GetRefreshToken() == "" || exchanged.GetUser().GetId() != userID {
		t.Fatalf("exchange authorization=%#v err=%v", exchanged, err)
	}
	if _, err := server.ExchangeAuthorizationCode(ctx, &commodorepb.ExchangeAuthorizationCodeRequest{
		Code:         completed.GetCode(),
		CodeVerifier: verifier,
		ClientId:     "tray-mac",
		RedirectUri:  "http://127.0.0.1:45678/callback",
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("authorization-code replay code=%v err=%v", status.Code(err), err)
	}

	started, err := server.StartDeviceAuthorization(ctx, &commodorepb.StartDeviceAuthorizationRequest{
		ClientId: "cli", Scope: "account",
	})
	if err != nil || started.GetDeviceCode() == "" || started.GetUserCode() == "" ||
		started.GetVerificationUri() != "https://console.example.com/device" {
		t.Fatalf("start device authorization=%#v err=%v", started, err)
	}
	var storedDeviceHash string
	if err := db.QueryRowContext(ctx, `SELECT device_code_hash FROM commodore.auth_device_codes`).Scan(&storedDeviceHash); err != nil {
		t.Fatal(err)
	}
	if storedDeviceHash != hashToken(started.GetDeviceCode()) || storedDeviceHash == started.GetDeviceCode() {
		t.Fatalf("device code not hashed at rest: %q", storedDeviceHash)
	}
	lookup, err := server.LookupDeviceAuthorization(userCtx, &commodorepb.LookupDeviceAuthorizationRequest{
		UserCode: started.GetUserCode(),
	})
	if err != nil || lookup.GetClientId() != "cli" || lookup.GetScope() != "account" {
		t.Fatalf("lookup device authorization=%#v err=%v", lookup, err)
	}
	if _, err := server.PollDeviceAuthorization(ctx, &commodorepb.PollDeviceAuthorizationRequest{
		DeviceCode: started.GetDeviceCode(), ClientId: "cli",
	}); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "AUTHORIZATION_PENDING" {
		t.Fatalf("initial device poll code=%v err=%v", status.Code(err), err)
	}
	if _, err := server.PollDeviceAuthorization(ctx, &commodorepb.PollDeviceAuthorizationRequest{
		DeviceCode: started.GetDeviceCode(), ClientId: "cli",
	}); status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "SLOW_DOWN" {
		t.Fatalf("fast device poll code=%v err=%v", status.Code(err), err)
	}
	approved, err := server.ApproveDeviceAuthorization(userCtx, &commodorepb.ApproveDeviceAuthorizationRequest{
		UserCode: started.GetUserCode(),
	})
	if err != nil || !approved.GetSuccess() || approved.GetClientId() != "cli" {
		t.Fatalf("approve device authorization=%#v err=%v", approved, err)
	}
	deviceSession, err := server.PollDeviceAuthorization(ctx, &commodorepb.PollDeviceAuthorizationRequest{
		DeviceCode: started.GetDeviceCode(), ClientId: "cli",
	})
	if err != nil || deviceSession.GetToken() == "" || deviceSession.GetRefreshToken() == "" || deviceSession.GetUser().GetId() != userID {
		t.Fatalf("approved device poll=%#v err=%v", deviceSession, err)
	}
	if _, err := server.PollDeviceAuthorization(ctx, &commodorepb.PollDeviceAuthorizationRequest{
		DeviceCode: started.GetDeviceCode(), ClientId: "cli",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("consumed device-code replay code=%v err=%v", status.Code(err), err)
	}
}
