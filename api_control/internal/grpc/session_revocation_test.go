package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/golang-jwt/jwt/v5"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const sessionTestUserID = "20000000-0000-4000-8000-0000000000b2"

type sessionAuthStream struct {
	grpcgo.ServerStream
	ctx context.Context
}

func (s sessionAuthStream) Context() context.Context { return s.ctx }

func TestCommodoreTransportRejectsRevokedSession(t *testing.T) {
	secret := refreshTokenTestSettings().JWTSecret
	token, err := auth.GenerateSessionJWT(sessionTestUserID, testTenantID, "user@example.com", "member", nil, time.Now(), secret)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "unary", true: "stream"}[stream], func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			s.runtimeSettings = refreshTokenTestSettings
			mock.ExpectQuery(`FROM commodore\.users\s+WHERE id = \$1 AND tenant_id = \$2`).
				WithArgs(sessionTestUserID, testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}).AddRow(true, time.Now()))
			cfg := commodoreAuthConfig(CommodoreServerConfig{JWTSecret: secret}, s)
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
			called := false
			if stream {
				err = middleware.GRPCStreamAuthInterceptor(cfg)(nil, sessionAuthStream{ctx: ctx}, &grpcgo.StreamServerInfo{FullMethod: "/commodore.UserService/GetMe"}, func(any, grpcgo.ServerStream) error {
					called = true
					return nil
				})
			} else {
				_, err = middleware.GRPCAuthInterceptor(cfg)(ctx, nil, &grpcgo.UnaryServerInfo{FullMethod: "/commodore.UserService/GetMe"}, func(context.Context, any) (any, error) {
					called = true
					return nil, nil
				})
			}
			if status.Code(err) != codes.Unauthenticated || called {
				t.Fatalf("revoked session: err=%v handler called=%v", err, called)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// sessionsRevokedPayload captures the payload of the user.sessions_revoked
// domain event insert.
type sessionsRevokedPayload struct {
	msg internalv1.UserSessionsRevoked
	ok  bool
}

func (p *sessionsRevokedPayload) Match(v driver.Value) bool {
	data, isBytes := v.([]byte)
	if !isBytes {
		return false
	}
	p.ok = proto.Unmarshal(data, &p.msg) == nil
	return p.ok
}

// expectSessionsRevoked expects the user's sessions_revoked_at update and the
// user.sessions_revoked domain event written in the same transaction, and
// returns the captured event payload.
func expectSessionsRevoked(mock sqlmock.Sqlmock, userID, tenantID string, revokedAt time.Time) *sessionsRevokedPayload {
	mock.ExpectQuery(`UPDATE commodore\.users\s+SET sessions_revoked_at = GREATEST`).
		WithArgs(userID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"sessions_revoked_at"}).AddRow(revokedAt))
	mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at`).
		WithArgs(tenantID, userID, "password_reset").WillReturnRows(sqlmock.NewRows(authSessionColumns))
	payload := &sessionsRevokedPayload{}
	args := []driver.Value{sqlmock.AnyArg(), "user.sessions_revoked"}
	for range 10 {
		args = append(args, sqlmock.AnyArg())
	}
	args = append(args, payload)
	mock.ExpectExec(`INSERT INTO commodore\.domain_event_outbox`).
		WithArgs(args...).
		WillReturnResult(sqlmock.NewResult(0, 1))
	return payload
}

func checkSessionsRevoked(t *testing.T, payload *sessionsRevokedPayload, userID string, revokedAt time.Time, reason internalv1.SessionRevocationReason) {
	t.Helper()
	if !payload.ok {
		t.Fatal("no user.sessions_revoked payload was written")
	}
	if payload.msg.GetUserId() != userID || !payload.msg.GetRevokedAt().AsTime().Equal(revokedAt) || payload.msg.GetReason() != reason {
		t.Fatalf("user.sessions_revoked = %+v, want user %s revoked at %s for %s", &payload.msg, userID, revokedAt, reason)
	}
}

func TestLogoutRevokesOnlyThePresentedSession(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectBegin()
	expectSessionUserLock(mock)
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).
		WithArgs(testTenantID, sessionTestUserID, authSessionTestID).WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, nil)...))
	mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at`).WithArgs(testTenantID, sessionTestUserID, authSessionTestID, "logout").
		WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, time.Now())...))
	expectRefreshFamilyDeletion(mock, authSessionTestID)
	payload := expectNamedSessionEvent(mock)
	mock.ExpectCommit()
	response, err := s.Logout(sessionContext(authSessionTestID), &commodorepb.LogoutRequest{})
	if err != nil || !response.GetSuccess() {
		t.Fatalf("logout: %+v %v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("logout did not revoke exactly its named session: %v", err)
	}
	if payload.message.GetSessionId() != authSessionTestID {
		t.Fatalf("logout event: %v", &payload.message)
	}
}

func TestLogoutWithoutNamedSessionNeverFallsBackToGlobalRevocation(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	_, err := s.Logout(sessionContext(""), &commodorepb.LogoutRequest{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy logout: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A password reset ends every existing session of the user, so a session
// opened with the old password stops refreshing and stops holding sockets.
func TestResetPasswordRevokesEverySessionOfTheUser(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	wakes := 0
	s.domainEventsCommitted = func() { wakes++ }
	revokedAt := time.Unix(1_800_000_100, 0).UTC()

	mock.ExpectQuery("SELECT id FROM commodore.users WHERE reset_token").
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(sessionTestUserID))
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE commodore\.users.*reset_token =.*RETURNING tenant_id`).
		WithArgs(sqlmock.AnyArg(), sessionTestUserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(testTenantID))
	mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens\s+WHERE tenant_id = \$1 AND user_id = \$2 ORDER BY id FOR UPDATE`).
		WithArgs(testTenantID, sessionTestUserID).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec(`DELETE FROM commodore\.refresh_tokens`).
		WithArgs(sessionTestUserID, testTenantID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	payload := expectSessionsRevoked(mock, sessionTestUserID, testTenantID, revokedAt)
	mock.ExpectCommit()

	resp, err := s.ResetPassword(context.Background(), &commodorepb.ResetPasswordRequest{
		Token:    "reset-token",
		Password: "new password with enough entropy",
	})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("ResetPassword = %+v, %v", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	checkSessionsRevoked(t, payload, sessionTestUserID, revokedAt, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_PASSWORD_RESET)
	if wakes != 1 {
		t.Fatalf("password reset woke the domain event relay %d times, want 1", wakes)
	}
}

func TestRefreshTokenReuseRevokesOnlyTheAffectedSession(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	wakes := 0
	s.domainEventsCommitted = func() { wakes++ }
	expectRefreshOwner(mock, "raw-refresh", sessionTestUserID, testTenantID)
	expectRefreshLock(mock, "raw-refresh", sessionTestUserID, testTenantID, true, time.Now().Add(-time.Hour), "rt-2")
	mock.ExpectQuery(`FROM commodore.refresh_tokens WHERE tenant_id`).WithArgs(testTenantID, sessionTestUserID, "rt-2").
		WillReturnRows(sqlmock.NewRows([]string{"id", "revoked", "replaced_by"}).AddRow("rt-2", true, "rt-3"))
	mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at`).WithArgs(testTenantID, sessionTestUserID, authSessionTestID, "reuse_detected").
		WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, time.Now())...))
	expectRefreshFamilyDeletion(mock, authSessionTestID)
	payload := expectNamedSessionEvent(mock)
	mock.ExpectCommit()
	if _, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: "raw-refresh"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("reuse: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if payload.message.GetSessionId() != authSessionTestID || payload.message.GetReason() != internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_REFRESH_TOKEN_REUSE || wakes != 1 {
		t.Fatalf("reuse revocation event=%v wakes=%d", &payload.message, wakes)
	}
}

// ValidateSession accepts a session access token only while its user is
// active and the user's sessions were not revoked at or after the token's
// issue second.
func TestValidateSession(t *testing.T) {
	secret := refreshTokenTestSettings().JWTSecret
	token, err := auth.GenerateSessionJWT(sessionTestUserID, testTenantID, "user@example.com", "owner", nil, time.Now(), secret)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := auth.ValidateJWT(token, secret)
	if err != nil {
		t.Fatal(err)
	}
	issuedAt := claims.IssuedAt.Time

	stateRows := func(active bool, revokedAt any) *sqlmock.Rows {
		return sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}).AddRow(active, revokedAt)
	}
	cases := []struct {
		name        string
		token       string
		rows        *sqlmock.Rows
		want        bool
		deactivated bool
	}{
		{"never_revoked", token, stateRows(true, nil), true, false},
		{"revoked_before_issue", token, stateRows(true, issuedAt.Add(-time.Second)), true, false},
		{"revoked_in_issue_second", token, stateRows(true, issuedAt.Add(500*time.Millisecond)), false, false},
		{"revoked_after_issue", token, stateRows(true, issuedAt.Add(time.Minute)), false, false},
		{"inactive_user", token, stateRows(false, issuedAt.Add(time.Minute)), false, true},
		{"unknown_user", token, sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}), false, false},
		{"not_a_session_token", "not-a-jwt", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			s.runtimeSettings = refreshTokenTestSettings
			if tc.rows != nil {
				mock.ExpectQuery(`FROM commodore\.users\s+WHERE id = \$1 AND tenant_id = \$2`).
					WithArgs(sessionTestUserID, testTenantID).
					WillReturnRows(tc.rows)
			}
			resp, err := s.ValidateSession(context.Background(), &commodorepb.ValidateSessionRequest{Token: tc.token})
			if err != nil {
				t.Fatalf("ValidateSession error: %v", err)
			}
			if resp.GetValid() != tc.want || resp.GetAccountDeactivated() != tc.deactivated {
				t.Fatalf("valid = %v deactivated = %v, want %v %v", resp.GetValid(), resp.GetAccountDeactivated(), tc.want, tc.deactivated)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}

// ListRecentSessionRevocations returns every tenant's recent revocations and
// never looks back further than a session token can live.
func TestListRecentSessionRevocationsClampsItsLookback(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	revokedAt := time.Now().Add(-time.Minute).UTC()
	since := &capturedTime{}
	mock.ExpectQuery(`FROM commodore\.users\s+WHERE sessions_revoked_at > \$1`).
		WithArgs(since).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "sessions_revoked_at", "is_active"}).
			AddRow(sessionTestUserID, testTenantID, revokedAt, true).
			AddRow("30000000-0000-4000-8000-0000000000c3", "40000000-0000-4000-8000-0000000000d4", revokedAt, false))
	mock.ExpectQuery(`FROM commodore\.auth_sessions WHERE revoked_at > \$1`).
		WithArgs(since).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "user_id", "id", "revoked_at", "revoke_reason"}).
			AddRow(testTenantID, sessionTestUserID, "50000000-0000-4000-8000-000000000081", revokedAt, "logout"))
	start := time.Now()
	resp, err := s.ListRecentSessionRevocations(context.Background(), &commodorepb.ListRecentSessionRevocationsRequest{
		Since:               timestamppb.New(start.Add(-24 * time.Hour)),
		IncludeAuthSessions: true,
	})
	if err != nil {
		t.Fatalf("ListRecentSessionRevocations: %v", err)
	}
	if !resp.GetAuthSessionsIncluded() {
		t.Fatal("named snapshot omitted capability proof")
	}
	if len(resp.GetRevocations()) != 3 || resp.GetRevocations()[0].GetUserId() != sessionTestUserID || resp.GetRevocations()[1].GetTenantId() != "40000000-0000-4000-8000-0000000000d4" || resp.GetRevocations()[2].GetSessionId() != "50000000-0000-4000-8000-000000000081" {
		t.Fatalf("revocations = %v", resp.GetRevocations())
	}
	// A deactivated user's revocation says so, for replicas that missed the event.
	if resp.GetRevocations()[0].GetAccountDeactivated() || !resp.GetRevocations()[1].GetAccountDeactivated() {
		t.Fatalf("account_deactivated = %v, %v; want false, true", resp.GetRevocations()[0].GetAccountDeactivated(), resp.GetRevocations()[1].GetAccountDeactivated())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	if lookback := start.Sub(since.at); lookback < auth.SessionTokenTTL || lookback > maxSessionRevocationLookback+time.Second {
		t.Fatalf("a day-old since read back %s, want between the session lifetime and %s", lookback, maxSessionRevocationLookback)
	}
}

func TestLegacyRevocationBackfillNeverReceivesNamedSessions(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery(`FROM commodore\.users\s+WHERE sessions_revoked_at > \$1`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "sessions_revoked_at", "is_active"}))
	response, err := s.ListRecentSessionRevocations(context.Background(), &commodorepb.ListRecentSessionRevocationsRequest{})
	if err != nil || len(response.GetRevocations()) != 0 || response.GetAuthSessionsIncluded() {
		t.Fatalf("legacy backfill queried named sessions: response=%v err=%v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNamedSessionRejectsRevocationAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name           string
		revoked        bool
		idle, absolute time.Duration
		valid          bool
	}{
		{"live", false, time.Hour, 2 * time.Hour, true},
		{"revoked", true, time.Hour, 2 * time.Hour, false},
		{"idle expired", false, -time.Hour, time.Hour, false},
		{"absolute expired", false, time.Hour, -time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			s.runtimeSettings = refreshTokenTestSettings
			now := time.Now()
			const sessionID = "50000000-0000-4000-8000-000000000081"
			claims := &auth.Claims{TenantID: testTenantID, UserID: sessionTestUserID, SessionID: sessionID,
				RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(auth.SessionTokenTTL))},
			}
			token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.settings().JWTSecret)
			if err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(`FROM commodore\.users\s+WHERE id = \$1 AND tenant_id = \$2`).WithArgs(sessionTestUserID, testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}).AddRow(true, nil))
			var revokedAt any
			if tc.revoked {
				revokedAt = now
			}
			mock.ExpectQuery(`FROM commodore\.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3`).WithArgs(testTenantID, sessionTestUserID, sessionID).
				WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "user_id", "auth_method", "client_id", "device_label", "client_version", "created_ip", "last_ip", "user_agent", "created_at", "last_seen_at", "idle_expires_at", "absolute_expires_at", "revoked_at", "revoke_reason"}).
					AddRow(sessionID, testTenantID, sessionTestUserID, "password", "web", "Browser", "", "", "", "", now, now, now.Add(tc.idle), now.Add(tc.absolute), revokedAt, nil))
			response, err := s.ValidateSession(context.Background(), &commodorepb.ValidateSessionRequest{Token: token})
			if err != nil || response.GetValid() != tc.valid {
				t.Fatalf("valid=%v err=%v", response.GetValid(), err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// capturedTime matches any time argument and records it.
type capturedTime struct{ at time.Time }

func (c *capturedTime) Match(v driver.Value) bool {
	at, ok := v.(time.Time)
	c.at = at
	return ok
}
