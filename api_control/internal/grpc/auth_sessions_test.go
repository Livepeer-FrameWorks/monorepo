package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

const authSessionTestID = "50000000-0000-4000-8000-000000000081"

var authSessionColumns = []string{"id", "tenant_id", "user_id", "auth_method", "client_id", "device_label", "client_version", "created_ip", "last_ip", "user_agent", "created_at", "last_seen_at", "idle_expires_at", "absolute_expires_at", "revoked_at", "revoke_reason", "device_key"}

func sessionValues(id string, revokedAt any) []driver.Value {
	var reason any
	if revokedAt != nil {
		reason = "user"
	}
	now := time.Now()
	return []driver.Value{id, testTenantID, sessionTestUserID, "password", "web", "Browser", "", "203.0.113.10", "203.0.113.10", "Browser/1", now.Add(-time.Hour), now, now.Add(time.Hour), now.Add(2 * time.Hour), revokedAt, reason, ""}
}

func sessionContext(id string) context.Context {
	ctx := ctxAs(sessionTestUserID, testTenantID, "owner")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
	return context.WithValue(ctx, ctxkeys.KeySessionID, id)
}

func expectSessionUserLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(`SELECT id FROM commodore.users WHERE tenant_id = \$1 AND id = \$2 FOR UPDATE`).
		WithArgs(testTenantID, sessionTestUserID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(sessionTestUserID))
}

func expectRefreshFamilyDeletion(mock sqlmock.Sqlmock, sessionID string) {
	mock.ExpectQuery(`SELECT id FROM commodore.refresh_tokens\s+WHERE tenant_id = \$1 AND user_id = \$2 AND session_id = \$3`).
		WithArgs(testTenantID, sessionTestUserID, sessionID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("refresh-root"))
	mock.ExpectExec(`WITH RECURSIVE family AS`).WithArgs(testTenantID, sessionTestUserID, sessionID).
		WillReturnResult(sqlmock.NewResult(0, 3))
}

type namedSessionPayload struct{ message internalv1.AuthSessionRevoked }

func (p *namedSessionPayload) Match(value driver.Value) bool {
	data, ok := value.([]byte)
	return ok && proto.Unmarshal(data, &p.message) == nil
}

func expectNamedSessionEvent(mock sqlmock.Sqlmock) *namedSessionPayload {
	payload := &namedSessionPayload{}
	args := []driver.Value{sqlmock.AnyArg(), "auth_session.revoked"}
	for range 10 {
		args = append(args, sqlmock.AnyArg())
	}
	args = append(args, payload)
	mock.ExpectExec(`INSERT INTO commodore.domain_event_outbox`).WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
	return payload
}

func TestSessionManagementRequiresInteractiveIdentity(t *testing.T) {
	for _, authType := range []string{"", "api_token", "wallet", "service"} {
		t.Run(authType, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			ctx := context.WithValue(sessionContext(authSessionTestID), ctxkeys.KeyAuthType, authType)
			_, listErr := s.ListSessions(ctx, &commodorepb.ListSessionsRequest{})
			_, renameErr := s.RenameSession(ctx, &commodorepb.RenameSessionRequest{SessionId: authSessionTestID, Label: "Laptop"})
			_, revokeErr := s.RevokeSession(ctx, &commodorepb.RevokeSessionRequest{SessionId: authSessionTestID})
			_, othersErr := s.RevokeOtherSessions(ctx, &emptypb.Empty{})
			for _, err := range []error{listErr, renameErr, revokeErr, othersErr} {
				if status.Code(err) != codes.PermissionDenied {
					t.Fatalf("%s credential managed sessions: %v", authType, err)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListSessionsStaysSelfScopedForPlatformOperator(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	ctx := context.WithValue(sessionContext(authSessionTestID), ctxkeys.KeyPlatformOperator, true)
	mock.ExpectQuery(`SELECT COUNT\(\*\)::INTEGER FROM commodore.auth_sessions`).WithArgs(testTenantID, sessionTestUserID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2`).
		WithArgs(testTenantID, sessionTestUserID, nil, nil, int32(2)).
		WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, nil)...).
			AddRow(sessionValues("50000000-0000-4000-8000-000000000082", nil)...))
	response, err := s.ListSessions(ctx, &commodorepb.ListSessionsRequest{Pagination: &commonpb.CursorPaginationRequest{First: 1}})
	if err != nil || len(response.GetSessions()) != 1 || !response.Sessions[0].Current || !response.Pagination.HasNextPage || response.Pagination.TotalCount != 2 {
		t.Fatalf("self-scoped page: response=%+v err=%v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeSessionWritesOnlyNamedRevocation(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	wakes := 0
	s.domainEventsCommitted = func() { wakes++ }
	at := time.Now().UTC()
	mock.ExpectBegin()
	expectSessionUserLock(mock)
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).
		WithArgs(testTenantID, sessionTestUserID, authSessionTestID).WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, nil)...))
	mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp\(\), revoke_reason = \$4`).
		WithArgs(testTenantID, sessionTestUserID, authSessionTestID, "user").WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, at)...))
	expectRefreshFamilyDeletion(mock, authSessionTestID)
	payload := expectNamedSessionEvent(mock)
	mock.ExpectCommit()
	response, err := s.RevokeSession(sessionContext(authSessionTestID), &commodorepb.RevokeSessionRequest{SessionId: authSessionTestID})
	if err != nil || !response.GetSuccess() || !response.GetCurrent() {
		t.Fatalf("revoke own session: response=%+v err=%v", response, err)
	}
	if payload.message.GetUserId() != sessionTestUserID || payload.message.GetSessionId() != authSessionTestID || !payload.message.GetRevokedAt().AsTime().Equal(at) || wakes != 1 {
		t.Fatalf("named revocation: %+v, relay wakes=%d", &payload.message, wakes)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeSessionHidesOtherOwnersAndIsIdempotent(t *testing.T) {
	for _, alreadyRevoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "other owner", true: "already signed out"}[alreadyRevoked], func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			mock.ExpectBegin()
			expectSessionUserLock(mock)
			expectation := mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).WithArgs(testTenantID, sessionTestUserID, authSessionTestID)
			if alreadyRevoked {
				expectation.WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(authSessionTestID, time.Now())...))
				mock.ExpectCommit()
			} else {
				expectation.WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()
			}
			response, err := s.RevokeSession(sessionContext(""), &commodorepb.RevokeSessionRequest{SessionId: authSessionTestID})
			if alreadyRevoked && (err != nil || !response.GetSuccess()) {
				t.Fatalf("repeat revoke: response=%+v err=%v", response, err)
			}
			if !alreadyRevoked && status.Code(err) != codes.NotFound {
				t.Fatalf("other owner's session was visible: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRevokeSessionRecognizesEquivalentCurrentUUID(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	id := "50000000-0000-4000-8000-0000000000ab"
	mock.ExpectBegin()
	expectSessionUserLock(mock)
	mock.ExpectQuery(`FROM commodore.auth_sessions\s+WHERE tenant_id = \$1 AND user_id = \$2 AND id = \$3 FOR UPDATE`).
		WithArgs(testTenantID, sessionTestUserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues(id, time.Now())...))
	mock.ExpectCommit()
	response, err := s.RevokeSession(sessionContext(id), &commodorepb.RevokeSessionRequest{SessionId: strings.ToUpper(id)})
	if err != nil || !response.GetSuccess() || !response.GetCurrent() {
		t.Fatalf("equivalent UUID must identify the current session: response=%+v err=%v", response, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRevocationRollsBackWhenEventCannotPersist(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectBegin()
	expectSessionUserLock(mock)
	mock.ExpectQuery(`UPDATE commodore.auth_sessions SET revoked_at = clock_timestamp\(\), revoke_reason = \$4`).
		WithArgs(testTenantID, sessionTestUserID, authSessionTestID, "user").
		WillReturnRows(sqlmock.NewRows(authSessionColumns).AddRow(sessionValues("50000000-0000-4000-8000-000000000082", time.Now())...))
	expectRefreshFamilyDeletion(mock, "50000000-0000-4000-8000-000000000082")
	mock.ExpectExec(`INSERT INTO commodore.domain_event_outbox`).WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()
	_, err := s.RevokeOtherSessions(sessionContext(authSessionTestID), &emptypb.Empty{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("revocation without its event was acknowledged: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeOtherSessionsRequiresNamedCurrentSession(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	_, err := s.RevokeOtherSessions(sessionContext(""), &emptypb.Empty{})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy session ended every device: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRenameSessionValidatesLabelAndHidesOtherOwners(t *testing.T) {
	for _, label := range []string{"", strings.Repeat("x", 201), "Laptop\x00", "Two\nlines"} {
		s, mock, done := newMockServer(t)
		_, err := s.RenameSession(sessionContext(authSessionTestID), &commodorepb.RenameSessionRequest{SessionId: authSessionTestID, Label: label})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid label accepted: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		done()
	}
	s, mock, done := newMockServer(t)
	defer done()
	label := strings.Repeat("猫", 200)
	mock.ExpectExec(`UPDATE commodore.auth_sessions SET device_label = \$4`).
		WithArgs(testTenantID, sessionTestUserID, authSessionTestID, label).WillReturnResult(sqlmock.NewResult(0, 0))
	_, err := s.RenameSession(sessionContext(authSessionTestID), &commodorepb.RenameSessionRequest{SessionId: authSessionTestID, Label: label})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("other owner's session was renamed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
