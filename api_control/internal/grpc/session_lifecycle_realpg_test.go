//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthSessionLifecycle_RealPG(t *testing.T) {
	testAuthSessionLifecycle(t, startCommodoreRealPG(t))
}

func TestAuthSessionLifecycle_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_sessions", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthSessionLifecycle(t, db)
}

func testAuthSessionLifecycle(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db.SetMaxOpenConns(5)
	userID, tenantID := uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,role,is_active,verified)
VALUES($1,$2,'sessions@example.test','unused','owner',true,true)`, userID, tenantID); err != nil {
		t.Fatal(err)
	}
	s := &CommodoreServer{db: db, logger: logrus.New(), runtimeSettings: refreshTokenTestSettings, tokenHasher: testTokenHasher(t)}
	issue := func(client, method string) *commodorepb.AuthResponse {
		t.Helper()
		var response *commodorepb.AuthResponse
		err := s.withEventTx(ctx, func(tx *sql.Tx) error {
			var issueErr error
			response, issueErr = s.issueUserSessionTx(ctx, tx, userID, tenantID, SessionContext{ClientID: client, AuthMethod: method, DeviceLabel: client, passwordHash: "unused"})
			return issueErr
		})
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	claims := func(response *commodorepb.AuthResponse) *auth.Claims {
		t.Helper()
		c, err := auth.ValidateInteractiveJWT(response.GetToken(), s.settings().JWTSecret)
		if err != nil || c.SessionID == "" {
			t.Fatalf("session token: %+v %v", c, err)
		}
		return c
	}
	browser, cli, tray := issue("web", "password"), issue("cli", "device_code"), issue("tray-mac", "pkce")
	for _, entry := range []struct {
		response *commodorepb.AuthResponse
		days     int
	}{{browser, 90}, {cli, 180}, {tray, 180}} {
		c := claims(entry.response)
		var age, idle float64
		if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM absolute_expires_at-created_at), EXTRACT(EPOCH FROM idle_expires_at-created_at)
FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, c.SessionID).Scan(&age, &idle); err != nil {
			t.Fatal(err)
		}
		if delta := age - float64(entry.days*86400); delta < -2 || delta > 2 || idle > 30*86400+2 {
			t.Fatalf("lifetime=%f idle=%f client days=%d", age, idle, entry.days)
		}
	}
	browserID, otherID := claims(browser).SessionID, claims(cli).SessionID
	originalAuth := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `UPDATE commodore.auth_sessions SET created_at=$4 WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, userID, browserID, originalAuth); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	start := make(chan struct{})
	responses, failures := make([]*commodorepb.AuthResponse, 2), make([]error, 2)
	for i := range 2 {
		group.Go(func() {
			<-start
			responses[i], failures[i] = s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: browser.GetRefreshToken()})
		})
	}
	close(start)
	group.Wait()
	var winner *commodorepb.AuthResponse
	wins, retries := 0, 0
	for i := range 2 {
		if failures[i] == nil {
			winner = responses[i]
			wins++
		} else if status.Code(failures[i]) == codes.Unavailable {
			retries++
		} else {
			t.Fatalf("concurrent refresh: %v", failures[i])
		}
	}
	if wins != 1 || retries != 1 {
		t.Fatalf("refresh wins=%d retries=%d", wins, retries)
	}
	c := claims(winner)
	if c.SessionID != browserID || c.AuthTime != originalAuth.Unix() {
		t.Fatalf("refresh changed identity: %+v", c)
	}
	var heads int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND session_id=$3 AND NOT revoked`, tenantID, userID, browserID).Scan(&heads); err != nil || heads != 1 {
		t.Fatalf("live heads=%d err=%v", heads, err)
	}
	if _, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: winner.GetRefreshToken()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET rotated_at=NOW()-INTERVAL '11 minutes' WHERE tenant_id=$1 AND user_id=$2 AND token_hash=$3`, tenantID, userID, hashToken(browser.GetRefreshToken())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: browser.GetRefreshToken()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("theft accepted: %v", err)
	}
	if valid, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: winner.GetToken()}); err != nil || valid.GetValid() {
		t.Fatalf("stolen family JWT: %+v %v", valid, err)
	}
	if valid, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: cli.GetToken()}); err != nil || !valid.GetValid() {
		t.Fatalf("theft ended other device %s: %+v %v", otherID, valid, err)
	}
	logoutCtx := context.WithValue(ctx, ctxkeys.KeyUserID, userID)
	logoutCtx = context.WithValue(logoutCtx, ctxkeys.KeyTenantID, tenantID)
	logoutCtx = context.WithValue(logoutCtx, ctxkeys.KeyAuthType, "jwt")
	logoutCtx = context.WithValue(logoutCtx, ctxkeys.KeySessionID, otherID)
	if _, err := s.Logout(logoutCtx, &commodorepb.LogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: cli.GetRefreshToken()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("signed-out CLI still refreshes: %v", err)
	}
	if valid, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: tray.GetToken()}); err != nil || !valid.GetValid() {
		t.Fatalf("CLI logout ended tray: %+v %v", valid, err)
	}

	legacyUser := uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,password_hash,is_active,verified) VALUES($1,$2,'legacy-sessions@example.test','unused',true,true)`, legacyUser, tenantID); err != nil {
		t.Fatal(err)
	}
	rootID, headID := uuid.NewString(), uuid.NewString()
	legacyCreated := time.Now().UTC().Add(-60 * 24 * time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,expires_at,created_at,revoked,replaced_by,rotated_at)
VALUES($1,$2,$3,'legacy-old-hash',NOW()+INTERVAL '1 hour',$4,true,$5,NOW()-INTERVAL '1 hour'),
($5,$2,$3,$6,NOW()+INTERVAL '720 hours',NOW(),false,NULL,NULL)`, rootID, tenantID, legacyUser, legacyCreated, headID, hashToken("legacy-head")); err != nil {
		t.Fatal(err)
	}
	adopted, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: "legacy-head"})
	if err != nil {
		t.Fatalf("legacy adoption: %v", err)
	}
	adoptedClaims := claims(adopted)
	if adoptedClaims.AuthTime != legacyCreated.Unix() {
		t.Fatalf("legacy session rejuvenated: %d want %d", adoptedClaims.AuthTime, legacyCreated.Unix())
	}
	var maxAge float64
	if err := db.QueryRowContext(ctx, `SELECT EXTRACT(EPOCH FROM absolute_expires_at-created_at) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, legacyUser, adoptedClaims.SessionID).Scan(&maxAge); err != nil || maxAge != 90*86400 {
		t.Fatalf("legacy maximum age=%f %v", maxAge, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.auth_sessions SET idle_expires_at=NOW()+INTERVAL '20 seconds',absolute_expires_at=NOW()+INTERVAL '20 seconds' WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, legacyUser, adoptedClaims.SessionID); err != nil {
		t.Fatal(err)
	}
	bounded, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: adopted.GetRefreshToken()})
	if err != nil || time.Until(claims(bounded).ExpiresAt.Time) > 20*time.Second {
		t.Fatalf("access escaped deadline: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.auth_sessions SET idle_expires_at=NOW()-INTERVAL '1 second',absolute_expires_at=NOW()-INTERVAL '1 second' WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenantID, legacyUser, adoptedClaims.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: bounded.GetRefreshToken()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expired session refreshed: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,device_label,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'password','web','cap fixture',NOW()-INTERVAL '1 hour',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours' FROM generate_series(1,100)`, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	issued := issue("web", "password")
	var active int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>NOW() AND absolute_expires_at>NOW()`, tenantID, userID).Scan(&active); err != nil || active != maxLiveAuthSessions {
		t.Fatalf("session cap active=%d %v", active, err)
	}
	if valid, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: issued.GetToken()}); err != nil || !valid.GetValid() {
		t.Fatalf("cap evicted new session: %+v %v", valid, err)
	}
	concurrentResponses := make([]*commodorepb.AuthResponse, 4)
	concurrentErrors := make([]error, len(concurrentResponses))
	capStart := make(chan struct{})
	var capIssuers sync.WaitGroup
	for i := range concurrentResponses {
		capIssuers.Go(func() {
			<-capStart
			concurrentErrors[i] = s.withEventTx(ctx, func(tx *sql.Tx) error {
				var issueErr error
				concurrentResponses[i], issueErr = s.issueUserSessionTx(ctx, tx, userID, tenantID, SessionContext{ClientID: "cli", AuthMethod: "device_code", DeviceLabel: "Concurrent CLI"})
				return issueErr
			})
		})
	}
	close(capStart)
	capIssuers.Wait()
	for i, response := range concurrentResponses {
		if concurrentErrors[i] != nil {
			t.Fatalf("concurrent cap issuer %d: %v", i, concurrentErrors[i])
		}
		if valid, validateErr := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: response.GetToken()}); validateErr != nil || !valid.GetValid() {
			t.Fatalf("concurrent cap evicted a new session: %+v %v", valid, validateErr)
		}
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>NOW() AND absolute_expires_at>NOW()`, tenantID, userID).Scan(&active); err != nil || active != maxLiveAuthSessions {
		t.Fatalf("concurrent issuance escaped cap: active=%d err=%v", active, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.users SET reset_token=$3,reset_token_expires=NOW()+INTERVAL '1 hour' WHERE tenant_id=$1 AND id=$2`, tenantID, userID, hashToken("lifecycle-reset")); err != nil {
		t.Fatal(err)
	}
	reset, err := s.ResetPassword(ctx, &commodorepb.ResetPasswordRequest{Token: "lifecycle-reset", Password: "new-secure-password"})
	if err != nil || !reset.GetSuccess() {
		t.Fatalf("password reset: %+v %v", reset, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL`, tenantID, userID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("reset left %d sessions active: %v", active, err)
	}
	if valid, err := s.ValidateSession(ctx, &commodorepb.ValidateSessionRequest{Token: issued.GetToken()}); err != nil || valid.GetValid() {
		t.Fatalf("reset JWT accepted: %+v %v", valid, err)
	}
	if _, err := s.RefreshToken(ctx, &commodorepb.RefreshTokenRequest{RefreshToken: issued.GetRefreshToken()}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("reset refresh accepted: %v", err)
	}
}
