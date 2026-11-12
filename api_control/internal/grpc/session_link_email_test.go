package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLinkEmailRequiresInteractiveNamedSession(t *testing.T) {
	for _, tc := range []struct {
		name, kind, sid, password string
		want                      codes.Code
	}{
		{"wallet bearer", "wallet", authSessionTestID, "new-password", codes.PermissionDenied},
		{"service", "service", authSessionTestID, "new-password", codes.PermissionDenied},
		{"API token", "api_token", authSessionTestID, "new-password", codes.PermissionDenied},
		{"legacy session", "jwt", "", "new-password", codes.FailedPrecondition},
		{"bcrypt limit", "jwt", authSessionTestID, strings.Repeat("x", 73), codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			ctx := context.WithValue(sessionContext(tc.sid), ctxkeys.KeyAuthType, tc.kind)
			_, err := s.LinkEmail(ctx, &commodorepb.LinkEmailRequest{Email: "linked@example.test", Password: tc.password})
			if status.Code(err) != tc.want {
				t.Fatalf("LinkEmail returned %v, want %s", err, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLinkEmailCommitsCredentialsRevocationAndDeliveryTogether(t *testing.T) {
	for _, failure := range []string{"", "revocation event", "email outbox"} {
		t.Run(failure, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			kicked := false
			s.accountEmailKickFn = func() { kicked = true }
			mock.ExpectBegin()
			expectSessionUserLock(mock)
			mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).
				WithArgs(testTenantID, sessionTestUserID, authSessionTestID).
				WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, nil)...))
			mock.ExpectQuery(`SELECT\s+COALESCE\(is_active, false\)::boolean AS is_active,\s+sessions_revoked_at`).
				WithArgs(sessionTestUserID, testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"is_active", "sessions_revoked_at"}).AddRow(true, nil))
			mock.ExpectQuery(`SELECT email\s+FROM commodore.users`).WithArgs(sessionTestUserID, testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"email"}).AddRow(nil))
			mock.ExpectQuery(`SELECT id\s+FROM commodore.users\s+WHERE LOWER\(email::text\)`).
				WithArgs("linked@example.test", sessionTestUserID).WillReturnError(sql.ErrNoRows)
			mock.ExpectExec(`UPDATE commodore.users\s+SET email`).WithArgs("linked@example.test", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sessionTestUserID, testTenantID).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE commodore.users\s+SET password_hash`).WithArgs(testTenantID, sessionTestUserID, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			otherID := "50000000-0000-4000-8000-000000000082"
			mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp\(\), revoke_reason = \$4`).
				WithArgs(testTenantID, sessionTestUserID, authSessionTestID, "password_change").
				WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(otherID, time.Now())...))
			expectRefreshFamilyDeletion(mock, otherID)
			if failure == "revocation event" {
				mock.ExpectExec(`INSERT INTO commodore.domain_event_outbox`).WillReturnError(errors.New("event persistence failed"))
			} else {
				expectNamedSessionEvent(mock)
				mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens\s+WHERE tenant_id = \$1 AND user_id = \$2 ORDER BY id FOR UPDATE`).
					WithArgs(testTenantID, sessionTestUserID).WillReturnRows(sqlmock.NewRows([]string{"id"}))
				mock.ExpectExec(`WITH RECURSIVE current_family AS`).WithArgs(testTenantID, sessionTestUserID, authSessionTestID).
					WillReturnResult(sqlmock.NewResult(0, 1))
				expectLegacyEventInsert(mock, "auth_password_changed")
				queue := mock.ExpectExec(`INSERT INTO commodore.account_email_outbox`).WithArgs(sessionTestUserID, testTenantID, accountEmailPurposeVerification)
				if failure == "email outbox" {
					queue.WillReturnError(errors.New("email persistence failed"))
				} else {
					queue.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			if failure != "" {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			response, err := s.LinkEmail(sessionContext(authSessionTestID), &commodorepb.LinkEmailRequest{Email: "Linked@Example.Test", Password: "new-password"})
			if failure != "" {
				if status.Code(err) != codes.Unavailable || response != nil || kicked {
					t.Fatalf("acknowledged partial credential change: response=%v err=%v mailKick=%t", response, err, kicked)
				}
			} else if err != nil || !response.GetSuccess() || !response.GetVerificationSent() || !kicked {
				t.Fatalf("committed link: response=%v err=%v mailKick=%t", response, err, kicked)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
