package grpc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func securityActivityContext(kind, tenant, user string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, kind)
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	return context.WithValue(ctx, ctxkeys.KeyUserID, user)
}

type activityWindowBound struct {
	value *time.Time
	days  int
}

func (w activityWindowBound) Match(value driver.Value) bool {
	at, ok := value.(time.Time)
	if !ok {
		return false
	}
	delta := time.Since(at.Add(time.Duration(w.days) * 24 * time.Hour))
	if delta < -3*time.Second || delta > 3*time.Second {
		return false
	}
	if !w.value.IsZero() {
		return w.value.Equal(at)
	}
	*w.value = at
	return true
}

func TestSecurityActivityUsesSubjectOwnershipAndStableForwardPage(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	tenant, user := uuid.NewString(), uuid.NewString()
	var from, until time.Time
	start, end := activityWindowBound{&from, 90}, activityWindowBound{&until, 0}
	before := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	cursorID := uuid.NewString()
	mock.ExpectQuery(`SELECT count\(\).*FROM periscope.api_events.*WHERE tenant_id = toUUID\(\?\).*JSONExtractString\(details, 'user_id'\) = \?`).WithArgs(tenant, user, start, end).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	rows := sqlmock.NewRows([]string{"event_id", "event_type", "timestamp", "auth_type", "session_id", "reason"})
	for i, id := range ids {
		rows.AddRow(id, "auth_login_succeeded", before.Add(-time.Duration(i+1)*time.Minute), "password", "", "")
	}
	mock.ExpectQuery(`SELECT toString\(event_id\).*JSONExtractString\(details, 'user_id'\) = \?.*ORDER BY timestamp DESC, event_id DESC LIMIT \?`).WithArgs(tenant, user, start, end, before, cursorID, 3).WillReturnRows(rows)
	cursor := pagination.EncodeCursor(before, cursorID)
	response, err := server.GetSecurityActivity(securityActivityContext("jwt", tenant, strings.ToUpper(user)), &periscopepb.GetSecurityActivityRequest{Pagination: &commonpb.CursorPaginationRequest{First: 2, After: &cursor}})
	if err != nil || len(response.GetEvents()) != 2 || !response.GetPagination().GetHasNextPage() || !response.GetPagination().GetHasPreviousPage() || response.GetPagination().GetTotalCount() != 5 {
		t.Fatalf("own activity page: %+v err=%v", response, err)
	}
	if response.GetPagination().GetEndCursor() != pagination.EncodeCursor(before.Add(-2*time.Minute), ids[1]) || until.Sub(from) != 90*24*time.Hour {
		t.Fatal("page cursor or rolling window changed")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityActivityRejectsNonInteractiveAndInvalidPagesBeforeQuery(t *testing.T) {
	tenant, user := uuid.NewString(), uuid.NewString()
	backward := "opaque"
	badCursor := pagination.EncodeCursorWithSortKey(1, uuid.NewString())
	future := pagination.EncodeCursor(time.Now().Add(time.Hour), uuid.NewString())
	for _, tc := range []struct {
		name, kind, tenant, user string
		page                     *commonpb.CursorPaginationRequest
		code                     codes.Code
	}{
		{"service", "service", tenant, user, nil, codes.PermissionDenied},
		{"delegated token", "api_token", tenant, user, nil, codes.PermissionDenied},
		{"wallet headers", "wallet", tenant, user, nil, codes.PermissionDenied},
		{"anonymous", "", tenant, user, nil, codes.PermissionDenied},
		{"missing tenant", "jwt", "", user, nil, codes.Unauthenticated},
		{"missing user", "jwt", tenant, "", nil, codes.Unauthenticated},
		{"large page", "jwt", tenant, user, &commonpb.CursorPaginationRequest{First: 101}, codes.InvalidArgument},
		{"negative page", "jwt", tenant, user, &commonpb.CursorPaginationRequest{First: -1}, codes.InvalidArgument},
		{"backwards", "jwt", tenant, user, &commonpb.CursorPaginationRequest{Before: &backward}, codes.InvalidArgument},
		{"non-time cursor", "jwt", tenant, user, &commonpb.CursorPaginationRequest{After: &badCursor}, codes.InvalidArgument},
		{"future cursor", "jwt", tenant, user, &commonpb.CursorPaginationRequest{After: &future}, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, mock := newTenantActivityServer(t)
			_, err := server.GetSecurityActivity(securityActivityContext(tc.kind, tc.tenant, tc.user), &periscopepb.GetSecurityActivityRequest{Pagination: tc.page})
			if status.Code(err) != tc.code {
				t.Fatalf("refusal=%v want=%v", err, tc.code)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSecurityActivityOutageDoesNotExposeDatabaseError(t *testing.T) {
	server, mock := newTenantActivityServer(t)
	tenant, user := uuid.NewString(), uuid.NewString()
	mock.ExpectQuery(`SELECT count\(\)`).WillReturnError(errors.New("private database failure detail"))
	_, err := server.GetSecurityActivity(securityActivityContext("jwt", tenant, user), &periscopepb.GetSecurityActivityRequest{})
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatalf("outage contract: %v", err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
