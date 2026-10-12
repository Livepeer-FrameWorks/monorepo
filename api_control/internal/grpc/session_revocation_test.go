package grpc

import (
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/protobuf/proto"
)

const sessionTestUserID = "20000000-0000-4000-8000-0000000000b2"

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
	mock.ExpectQuery(`UPDATE commodore\.users\s+SET sessions_revoked_at = NOW\(\)`).
		WithArgs(userID, tenantID).
		WillReturnRows(sqlmock.NewRows([]string{"sessions_revoked_at"}).AddRow(revokedAt))
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

// Logout ends every session of the user: it deletes the refresh tokens,
// records the revocation time that ValidateSession compares access tokens
// against, and emits user.sessions_revoked in the same transaction, waking
// the relay so every Bridge replica closes the user's sockets within seconds.
func TestLogoutRevokesEverySessionOfTheUser(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	wakes := 0
	s.domainEventsCommitted = func() { wakes++ }
	revokedAt := time.Unix(1_800_000_000, 0).UTC()

	mock.ExpectBegin()
	mock.ExpectExec(`DELETE FROM commodore\.refresh_tokens`).
		WithArgs(sessionTestUserID, testTenantID).
		WillReturnResult(sqlmock.NewResult(0, 2))
	payload := expectSessionsRevoked(mock, sessionTestUserID, testTenantID, revokedAt)
	mock.ExpectCommit()

	resp, err := s.Logout(ctxAs(sessionTestUserID, testTenantID, "owner"), &commodorepb.LogoutRequest{})
	if err != nil || !resp.GetSuccess() {
		t.Fatalf("Logout = %+v, %v", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	checkSessionsRevoked(t, payload, sessionTestUserID, revokedAt, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_LOGOUT)
	if wakes != 1 {
		t.Fatalf("logout woke the domain event relay %d times, want 1", wakes)
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

// Refresh-token reuse revokes the whole session family, including the access
// tokens the other party already holds.
func TestRefreshTokenReuseRevokesEverySessionOfTheUser(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	s.runtimeSettings = refreshTokenTestSettings
	wakes := 0
	s.domainEventsCommitted = func() { wakes++ }
	revokedAt := time.Unix(1_800_000_200, 0).UTC()
	refreshToken := "refresh-token-raw"

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM commodore\.refresh_tokens\s+WHERE token_hash`).
		WithArgs(hashToken(refreshToken)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "revoked", "rotated_at", "replaced_by"}).
			AddRow("rt-1", sessionTestUserID, testTenantID, true, time.Now().Add(-time.Hour), "rt-2"))
	mock.ExpectQuery(`FROM commodore\.refresh_tokens\s+WHERE id`).
		WithArgs("rt-2").
		WillReturnRows(sqlmock.NewRows([]string{"revoked"}).AddRow(true))
	mock.ExpectExec(`UPDATE commodore\.refresh_tokens SET revoked = true\s+WHERE user_id`).
		WithArgs(sessionTestUserID, testTenantID).
		WillReturnResult(sqlmock.NewResult(0, 3))
	payload := expectSessionsRevoked(mock, sessionTestUserID, testTenantID, revokedAt)
	mock.ExpectCommit()

	if _, err := s.RefreshToken(context.Background(), &commodorepb.RefreshTokenRequest{RefreshToken: refreshToken}); err == nil {
		t.Fatal("refresh token reuse was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
	checkSessionsRevoked(t, payload, sessionTestUserID, revokedAt, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_REFRESH_TOKEN_REUSE)
	if wakes != 1 {
		t.Fatalf("refresh token reuse woke the domain event relay %d times, want 1", wakes)
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
		name  string
		token string
		rows  *sqlmock.Rows
		want  bool
	}{
		{"never_revoked", token, stateRows(true, nil), true},
		{"revoked_before_issue", token, stateRows(true, issuedAt.Add(-time.Second)), true},
		{"revoked_in_issue_second", token, stateRows(true, issuedAt.Add(500*time.Millisecond)), false},
		{"revoked_after_issue", token, stateRows(true, issuedAt.Add(time.Minute)), false},
		{"inactive_user", token, stateRows(false, nil), false},
		{"unknown_user", token, sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}), false},
		{"not_a_session_token", "not-a-jwt", nil, false},
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
			if resp.GetValid() != tc.want {
				t.Fatalf("valid = %v, want %v", resp.GetValid(), tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("expectations: %v", err)
			}
		})
	}
}
