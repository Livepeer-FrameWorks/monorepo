//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/metadata"
)

func TestAuthorizationCodeKeepsApprovedMetadata_RealPG(t *testing.T) {
	testAuthorizationMetadata(t, startCommodoreRealPG(t))
}
func TestAuthorizationCodeKeepsApprovedMetadata_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_authorization_metadata", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthorizationMetadata(t, db)
}
func testAuthorizationMetadata(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	tenant, user := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'authorization-metadata@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	settings := refreshTokenTestSettings()
	settings.Branding = config.EmailBranding{WebAppURL: "https://console.example.test"}
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: func() RuntimeSettings { return settings }}
	owner := context.WithValue(ctx, ctxkeys.KeyUserID, user)
	owner = context.WithValue(owner, ctxkeys.KeyTenantID, tenant)
	owner = context.WithValue(owner, ctxkeys.KeyAuthType, "jwt")
	owner = metadata.NewIncomingContext(owner, metadata.Pairs("x-client-ip", "203.0.113.14", "x-device-label-bin", "Approved workstation", "x-client-version-bin", "tray macOS arm64", "x-client-user-agent-bin", "original-tray"))
	const verifier = "authorization-metadata-verifier-with-sufficient-entropy"
	digest := sha256.Sum256([]byte(verifier))
	code, err := server.CompleteAuthorization(owner, &commodorepb.CompleteAuthorizationRequest{ClientId: "tray-mac", RedirectUri: "http://127.0.0.1:54321/callback", CodeChallenge: base64.RawURLEncoding.EncodeToString(digest[:]), CodeChallengeMethod: "S256", Scope: "account", State: "metadata-state"})
	if err != nil {
		t.Fatal(err)
	}
	var label, version, ip, agent string
	if err := db.QueryRowContext(ctx, `SELECT device_label,client_version,request_ip,user_agent FROM commodore.auth_authorization_codes WHERE tenant_id=$1 AND user_id=$2 AND code_hash=$3`, tenant, user, hashToken(code.Code)).Scan(&label, &version, &ip, &agent); err != nil {
		t.Fatal(err)
	}
	if label != "Approved workstation" || version != "tray macOS arm64" || ip != "203.0.113.14" || agent != "original-tray" {
		t.Errorf("authorization code discarded approved metadata: label=%q version=%q ip=%q agent=%q", label, version, ip, agent)
	}
	redeem := metadata.NewIncomingContext(ctx, metadata.Pairs("x-client-ip", "198.51.100.22", "x-device-label-bin", "Changed workstation", "x-client-version-bin", "changed version", "x-client-user-agent-bin", "changed-agent"))
	result, err := server.ExchangeAuthorizationCode(redeem, &commodorepb.ExchangeAuthorizationCodeRequest{Code: code.Code, CodeVerifier: verifier, ClientId: "tray-mac", RedirectUri: "http://127.0.0.1:54321/callback"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateInteractiveJWT(result.Token, settings.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT device_label,client_version,created_ip,user_agent FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenant, user, claims.SessionID).Scan(&label, &version, &ip, &agent); err != nil {
		t.Fatal(err)
	}
	if label != "Approved workstation" || version != "tray macOS arm64" || ip != "203.0.113.14" || agent != "original-tray" {
		t.Errorf("code exchange replaced approved metadata: label=%q version=%q ip=%q agent=%q", label, version, ip, agent)
	}
	if bucket := readSignInAudit(t, ctx, db, tenant, user).GetAuthEvent().GetIpBucket(); bucket != "203.0.113.0/24" {
		t.Errorf("sign-in audit replaced approved native origin with redemption metadata: bucket=%q", bucket)
	}

}
