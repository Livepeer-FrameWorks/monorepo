//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
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

func TestDeviceAuthorizationKeepsRequestMetadata_RealPG(t *testing.T) {
	testDeviceRequestMetadata(t, startCommodoreRealPG(t))
}
func TestDeviceAuthorizationKeepsRequestMetadata_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_device_metadata", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testDeviceRequestMetadata(t, db)
}
func testDeviceRequestMetadata(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	tenant, user := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'device-metadata@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	settings := refreshTokenTestSettings()
	settings.Branding = config.EmailBranding{WebAppURL: "https://console.example.test"}
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: func() RuntimeSettings { return settings }}
	original := metadata.NewIncomingContext(ctx, metadata.Pairs("x-client-ip", "203.0.113.14", "x-device-label-bin", "Original workstation", "x-client-version-bin", "v0.3.15-rc1 / linux arm64", "x-client-user-agent-bin", "FrameWorks-CLI/test"))
	started, err := server.StartDeviceAuthorization(original, &commodorepb.StartDeviceAuthorizationRequest{ClientId: "cli", Scope: "account"})
	if err != nil {
		t.Fatal(err)
	}
	var label, version, ip, agent string
	if err := db.QueryRowContext(ctx, `SELECT device_label,client_version,request_ip,user_agent FROM commodore.auth_device_codes WHERE device_code_hash=$1`, hashToken(started.DeviceCode)).Scan(&label, &version, &ip, &agent); err != nil {
		t.Fatal(err)
	}
	if label != "Original workstation" || version != "v0.3.15-rc1 / linux arm64" || ip != "203.0.113.14" || agent != "FrameWorks-CLI/test" {
		t.Errorf("original device request metadata was discarded: label=%q version=%q ip=%q agent=%q", label, version, ip, agent)
	}
	owner := context.WithValue(ctx, ctxkeys.KeyUserID, user)
	owner = context.WithValue(owner, ctxkeys.KeyTenantID, tenant)
	owner = context.WithValue(owner, ctxkeys.KeyAuthType, "jwt")
	preview, err := server.LookupDeviceAuthorization(owner, &commodorepb.LookupDeviceAuthorizationRequest{UserCode: started.UserCode})
	if err != nil {
		t.Fatal(err)
	}
	if preview.GetDeviceLabel() != "Original workstation" || preview.GetClientVersion() != "v0.3.15-rc1 / linux arm64" || preview.GetRequestIp() != "203.0.113.14" || preview.GetUserAgent() != "FrameWorks-CLI/test" || preview.GetRequestedAt() == nil || !preview.GetRequestedAt().AsTime().Before(preview.GetExpiresAt().AsTime()) {
		t.Fatalf("approval preview lost original metadata: %+v", preview)
	}
	if _, err := server.ApproveDeviceAuthorization(owner, &commodorepb.ApproveDeviceAuthorizationRequest{UserCode: started.UserCode}); err != nil {
		t.Fatal(err)
	}
	poll := metadata.NewIncomingContext(ctx, metadata.Pairs("x-client-ip", "198.51.100.22", "x-device-label-bin", "Replacement poll label", "x-client-version-bin", "spoofed version", "x-client-user-agent-bin", "poll-agent"))
	result, err := server.PollDeviceAuthorization(poll, &commodorepb.PollDeviceAuthorizationRequest{DeviceCode: started.DeviceCode, ClientId: "cli"})
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
	if label != "Original workstation" || version != "v0.3.15-rc1 / linux arm64" || ip != "203.0.113.14" || agent != "FrameWorks-CLI/test" {
		t.Errorf("poll replaced approved device metadata: label=%q version=%q ip=%q agent=%q", label, version, ip, agent)
	}
}
