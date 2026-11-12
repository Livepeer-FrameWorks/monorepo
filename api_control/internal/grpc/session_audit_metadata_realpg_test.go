//go:build schema_verify

package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"

	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestWalletSignInAuditContainsVerifiedWalletAndCoarseIP_RealPG(t *testing.T) {
	testWalletSignInAuditMetadata(t, startCommodoreRealPG(t))
}
func TestWalletSignInAuditContainsVerifiedWalletAndCoarseIP_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_wallet_audit", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testWalletSignInAuditMetadata(t, db)
}
func testWalletSignInAuditMetadata(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	tenant, user, wallet := uuid.NewString(), uuid.NewString(), uuid.NewString()
	message := "Sign in to FrameWorks. Nonce: audit-metadata-fixture"
	address, signature, err := auth.SignWalletMessage("4c0883a69102937d6231471b5dbb6204fe51296170827922b7a56c91b8b56d09", message)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := auth.NormalizeEthAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(message))
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'audit-wallet@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.wallet_identities(id,user_id,tenant_id,wallet_address,chain_type) VALUES($1,$2,$3,$4,'ethereum')`, wallet, user, tenant, normalized); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.wallet_auth_challenges(wallet_address,chain_id,message_hash,expires_at) VALUES($1,1,$2,NOW()+INTERVAL '10 minutes')`, normalized, digest[:]); err != nil {
		t.Fatal(err)
	}
	settings := refreshTokenTestSettings()
	settings.Branding = config.EmailBranding{WebAppURL: "https://console.example.test"}
	server := &CommodoreServer{db: db, logger: logrus.New(), tokenHasher: testTokenHasher(t), runtimeSettings: func() RuntimeSettings { return settings }}
	source := metadata.NewIncomingContext(ctx, metadata.Pairs("x-client-ip", "203.0.113.147", "x-device-label-bin", "Audit wallet browser"))
	if _, err := server.WalletLogin(source, &commodorepb.WalletLoginRequest{WalletAddress: address, Message: message, Signature: signature}); err != nil {
		t.Fatal(err)
	}
	event := readSignInAudit(t, ctx, db, tenant, user)
	if event.GetAuthEvent().GetWalletId() != wallet {
		t.Errorf("wallet sign-in audit lost verified identity: got %q want %q", event.GetAuthEvent().GetWalletId(), wallet)
	}
	if event.GetAuthEvent().GetIpBucket() != "203.0.113.0/24" {
		t.Errorf("wallet sign-in audit lacks coarse origin: got %q want /24 without full address", event.GetAuthEvent().GetIpBucket())
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM commodore.wallet_identities WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenant, user, wallet); err != nil {
		t.Fatal(err)
	}
	err = server.withEventTx(source, func(tx *sql.Tx) error {
		info := sessionContextFromRequest(source, "wallet", "web")
		info.walletAddress = normalized
		_, issueErr := server.issueUserSessionTx(source, tx, user, tenant, info)
		return issueErr
	})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("removed wallet proof issued a session: %v", err)
	}
	var sessions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("removed credential changed session state: got %d sessions, want 1", sessions)
	}

}

func readSignInAudit(t *testing.T, ctx context.Context, db *sql.DB, tenant, user string) *ipcpb.ServiceEvent {
	t.Helper()
	var payload []byte
	if err := db.QueryRowContext(ctx, `SELECT payload FROM commodore.service_event_outbox WHERE tenant_id=$1 AND user_id=$2 AND event_type='auth_login_succeeded' ORDER BY created_at DESC LIMIT 1`, tenant, user).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var event ipcpb.ServiceEvent
	if err := protojson.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	return &event
}
