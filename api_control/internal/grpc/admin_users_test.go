package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

func TestAdminLookupUserByEmailAuthz(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"unauthenticated", context.Background()},
		{"tenant owner JWT", context.WithValue(ctxAs("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "jwt")},
		{"operator API token", context.WithValue(ctxAsOperator("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "api_token")},
		{"service token", context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			_, err := s.AdminLookupUserByEmail(tc.ctx, &commodorepb.AdminLookupUserByEmailRequest{Email: "owner@example.com"})
			wantCode(t, err, codes.PermissionDenied)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("denied caller reached the database: %v", err)
			}
		})
	}
}

// The interceptor refuses every JWT on a service-only method, which would lock
// the operator out of an RPC that admits nothing else.
func TestAdminLookupUserByEmailIsNotServiceOnly(t *testing.T) {
	for _, method := range commodoreServiceOnlyMethods() {
		if method == commodorepb.UserService_AdminLookupUserByEmail_FullMethodName {
			t.Fatal("AdminLookupUserByEmail is service-only; operator JWTs cannot reach it")
		}
	}
}

func TestAdminLookupUserByEmailRejectsMalformedEmail(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	for _, email := range []string{"", "   ", "not-an-email"} {
		_, err := s.AdminLookupUserByEmail(operatorJWTCtx(), &commodorepb.AdminLookupUserByEmailRequest{Email: email})
		wantCode(t, err, codes.InvalidArgument)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("malformed email reached the database: %v", err)
	}
}

func TestAdminLookupUserByEmailResolvesTenant(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery(`FROM commodore\.users\s+WHERE lower\(email::text\) = lower\(\$1::text\)`).
		WithArgs("owner@example.com").
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "email", "role", "is_active", "verified"}).
			AddRow("user-1", testTenantID, "owner@example.com", "owner", true, true))
	resp, err := s.AdminLookupUserByEmail(operatorJWTCtx(), &commodorepb.AdminLookupUserByEmailRequest{Email: "  Owner@Example.com "})
	if err != nil {
		t.Fatalf("AdminLookupUserByEmail: %v", err)
	}
	if resp.GetTenantId() != testTenantID || resp.GetUserId() != "user-1" || resp.GetRole() != "owner" || !resp.GetIsActive() || !resp.GetVerified() {
		t.Fatalf("response = %+v", resp)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminLookupUserByEmailReportsUnknownEmail(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	mock.ExpectQuery(`FROM commodore\.users`).WithArgs("nobody@example.com").WillReturnError(sql.ErrNoRows)
	_, err := s.AdminLookupUserByEmail(operatorJWTCtx(), &commodorepb.AdminLookupUserByEmailRequest{Email: "nobody@example.com"})
	wantCode(t, err, codes.NotFound)
}
