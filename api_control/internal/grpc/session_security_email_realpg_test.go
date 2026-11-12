//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	emailpkg "github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSessionSecurityEmail_RealPG(t *testing.T) {
	testSessionSecurityEmail(t, startCommodoreRealPG(t))
}
func TestSessionSecurityEmail_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_security_email", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testSessionSecurityEmail(t, db)
}

func testSessionSecurityEmail(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	tenant, user := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,verified,is_active,role) VALUES($1,$2,'security@example.test',true,true,'owner')`, user, tenant); err != nil {
		t.Fatal(err)
	}
	settings := refreshTokenTestSettings()
	settings.Branding = config.EmailBranding{WebAppURL: "https://app.example.test"}
	server := &CommodoreServer{db: db, logger: logrus.New(), runtimeSettings: func() RuntimeSettings { return settings }, tokenHasher: testTokenHasher(t)}
	queries := commodoredb.New(db)
	issue := func(label string) (*commodorepb.AuthResponse, error) {
		var response *commodorepb.AuthResponse
		err := server.withEventTx(ctx, func(tx *sql.Tx) error {
			var issueErr error
			response, issueErr = server.issueUserSessionTx(ctx, tx, user, tenant, SessionContext{ClientID: "cli", AuthMethod: "device_code", DeviceLabel: label, IP: "203.0.113.7"})
			return issueErr
		})
		return response, err
	}
	countEmails := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.account_email_outbox WHERE tenant_id=$1 AND user_id=$2 AND purpose LIKE 'security_%'`, tenant, user).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	first, err := issue("Original machine")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateInteractiveJWT(first.Token, settings.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if countEmails() != 1 {
		t.Fatal("unfamiliar sign-in did not queue one email")
	}
	// The old worker's pending-only claim cannot see or expire security alerts.
	var legacyClaims int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.account_email_outbox WHERE tenant_id=$1 AND user_id=$2 AND status='pending'`, tenant, user).Scan(&legacyClaims); err != nil || legacyClaims != 0 {
		t.Fatalf("legacy worker sees security alerts: count=%d err=%v", legacyClaims, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.account_email_outbox SET status='pending' WHERE tenant_id=$1 AND user_id=$2`, tenant, user); err == nil {
		t.Fatal("security email accepted the old worker's pending status")
	}
	if _, err = queries.RenameAuthSession(ctx, commodoredb.RenameAuthSessionParams{TenantID: tenant, UserID: user, ID: claims.SessionID, DeviceLabel: "Renamed display label"}); err != nil {
		t.Fatal(err)
	}
	if _, err = issue("Original machine"); err != nil {
		t.Fatal(err)
	}
	if countEmails() != 1 {
		t.Fatal("renaming a session changed its original device familiarity")
	}
	if _, err = issue("Another machine"); err != nil {
		t.Fatal(err)
	}
	if countEmails() != 2 {
		t.Fatal("new device did not queue its own email")
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.auth_session_devices SET last_seen_at=NOW()-INTERVAL '2184 hours' WHERE tenant_id=$1 AND user_id=$2 AND device_key=$3`, tenant, user, sessionDeviceKey("cli", "Original machine")); err != nil {
		t.Fatal(err)
	}
	if _, err = issue("Original machine"); err != nil {
		t.Fatal(err)
	}
	if countEmails() != 3 {
		t.Fatal("device unused for 90 days remained familiar")
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.auth_session_devices SET last_seen_at=NOW()-INTERVAL '2184 hours' WHERE tenant_id=$1 AND user_id=$2 AND device_key=$3`, tenant, user, sessionDeviceKey("cli", "Original machine")); err != nil {
		t.Fatal(err)
	}
	rotated, err := server.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: first.RefreshToken})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = issue("Original machine"); err != nil {
		t.Fatal(err)
	}
	if countEmails() != 3 {
		t.Fatal("active refresh did not maintain original device familiarity")
	}
	if _, err = server.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: rotated.RefreshToken}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET rotated_at=NOW()-INTERVAL '11 minutes' WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$3`, tenant, user, hashToken(first.RefreshToken)); err != nil {
		t.Fatal(err)
	}
	if _, err = server.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: first.RefreshToken}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("reused credential accepted: %v", err)
	}
	if countEmails() != 4 {
		t.Fatal("token theft did not queue its own mandatory email")
	}
	row, err := queries.GetAuthSession(ctx, commodoredb.GetAuthSessionParams{TenantID: tenant, UserID: user, ID: claims.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err = server.withEventTx(ctx, func(tx *sql.Tx) error { return enqueueSessionSecurityEmailTx(ctx, tx, row, "reuse") }); err != nil {
		t.Fatal(err)
	}
	if countEmails() != 4 {
		t.Fatal("same theft alert was queued twice")
	}
	// A queue outage cannot acknowledge a sign-in whose notification was lost.
	var sessionsBefore, sessionsAfter int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&sessionsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `ALTER TABLE commodore.account_email_outbox RENAME TO security_email_unavailable`); err != nil {
		t.Fatal(err)
	}
	_, issueErr := issue("Queue failure machine")
	if _, err = db.ExecContext(ctx, `ALTER TABLE commodore.security_email_unavailable RENAME TO account_email_outbox`); err != nil {
		t.Fatal(err)
	}
	if issueErr == nil {
		t.Fatal("sign-in succeeded without its email obligation")
	}
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&sessionsAfter); err != nil || sessionsAfter != sessionsBefore {
		t.Fatalf("partial sign-in committed: before=%d after=%d err=%v", sessionsBefore, sessionsAfter, err)
	}
	known, err := queries.KnownSessionDevice(ctx, commodoredb.KnownSessionDeviceParams{TenantID: tenant, UserID: user, DeviceKey: sessionDeviceKey("cli", "Queue failure machine")})
	if err != nil || known {
		t.Fatalf("failed notification committed device familiarity: known=%t err=%v", known, err)
	}
	// SMTP outages retry, including alerts older than the verification window.
	if _, err = db.ExecContext(ctx, `UPDATE commodore.account_email_outbox SET created_at=NOW()-INTERVAL '48 hours' WHERE tenant_id=$1 AND user_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	attempts, sent := 0, 0
	server.securityEmailSendFn = func(sendCtx context.Context, message emailpkg.Message) error {
		if _, ok := sendCtx.Deadline(); !ok {
			t.Error("SMTP delivery has no deadline")
		}
		if message.To != "security@example.test" || !strings.Contains(message.TextBody, "/account/security") {
			t.Error("incorrect security email recipient or action")
		}
		for _, secret := range []string{first.Token, first.RefreshToken, rotated.RefreshToken} {
			if strings.Contains(message.TextBody+message.HTMLBody, secret) {
				t.Error("security email contains a credential")
			}
		}
		attempts++
		if attempts == 1 {
			return errors.New("SMTP unavailable")
		}
		sent++
		return nil
	}
	worker := server.accountEmailOutboxWorker()
	worker.ProcessBatch(ctx)
	var retrying int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.account_email_outbox WHERE tenant_id=$1 AND user_id=$2 AND status='security_pending' AND attempts=1`, tenant, user).Scan(&retrying); err != nil || retrying != 1 {
		t.Fatalf("alert did not retain failed delivery: count=%d err=%v", retrying, err)
	}
	if _, err = db.ExecContext(ctx, `UPDATE commodore.account_email_outbox SET next_attempt_at=NOW() WHERE tenant_id=$1 AND user_id=$2`, tenant, user); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		worker.ProcessBatch(ctx)
	}
	if sent != 4 || attempts != 5 {
		t.Fatalf("mandatory mail delivery: attempts=%d sent=%d want=5,4", attempts, sent)
	}
	var completed int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.account_email_outbox WHERE tenant_id=$1 AND user_id=$2 AND status='completed'`, tenant, user).Scan(&completed); err != nil || completed != 4 {
		t.Fatalf("delivery settlement: count=%d err=%v", completed, err)
	}
	// A new owner in this tenant cannot inherit the first user's familiarity.
	other := uuid.NewString()
	if _, err = db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,verified,is_active) VALUES($1,$2,'other-security@example.test',true,true)`, other, tenant); err != nil {
		t.Fatal(err)
	}
	familiar, err := queries.KnownSessionDevice(ctx, commodoredb.KnownSessionDeviceParams{TenantID: tenant, UserID: other, DeviceKey: sessionDeviceKey("cli", "Original machine")})
	if err != nil || familiar {
		t.Fatalf("familiarity leaked between users: %t %v", familiar, err)
	}
	// Both discovery and point deletion remain bounded and owner fenced.
	for i := range 102 {
		key := fmt.Sprintf("expired-%03d", i)
		if _, err = db.ExecContext(ctx, `INSERT INTO commodore.auth_session_devices(tenant_id,user_id,device_key,last_seen_at) VALUES($1,$2,$3,NOW()-INTERVAL '2184 hours')`, tenant, user, key); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO commodore.account_email_outbox(tenant_id,user_id,purpose,status,payload,updated_at) VALUES($1,$2,$3,'abandoned','{}',NOW()-INTERVAL '744 hours')`, tenant, user, "security_sign_in:"+uuid.NewString()); err != nil {
			t.Fatal(err)
		}
	}
	if removed, deleteErr := queries.DeleteExpiredSessionDevice(ctx, commodoredb.DeleteExpiredSessionDeviceParams{TenantID: tenant, UserID: other, DeviceKey: "expired-000"}); deleteErr != nil || removed != 0 {
		t.Fatalf("foreign owner deletion: removed=%d err=%v", removed, deleteErr)
	}
	for _, want := range []int64{200, 4, 0} {
		if removed, sweepErr := server.sweepSessionSecurityRetention(ctx); sweepErr != nil || removed != want {
			t.Fatalf("bounded history cleanup: removed=%d want=%d err=%v", removed, want, sweepErr)
		}
	}
}
