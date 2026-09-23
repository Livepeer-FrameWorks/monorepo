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
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/lib/pq"
	"google.golang.org/grpc/codes"
)

func operatorJWTCtx() context.Context {
	ctx := ctxAsOperator("op-user", "ops-tenant", "member")
	return context.WithValue(ctx, ctxkeys.KeyAuthType, "jwt")
}

var adminTokenColumns = []string{"id", "tenant_id", "token_name", "permissions", "status", "last_used_at", "expires_at", "created_at"}

// sqlNoTokenValue fails the match when a query reads the credential hash.
type sqlNoTokenValue struct{}

func (sqlNoTokenValue) Match(expectedSQL, actualSQL string) error {
	if strings.Contains(actualSQL, "token_value") {
		return errors.New("operator token listing selected token_value")
	}
	return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
}

func TestAdminListAPITokensAuthz(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"unauthenticated", context.Background()},
		{"tenant owner JWT", context.WithValue(ctxAs("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "jwt")},
		{"operator API token", context.WithValue(ctxAsOperator("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "api_token")},
		{"service token", context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")},
		{"service token naming an operator", context.WithValue(ctxAsOperator("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "service")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()
			_, err := s.AdminListAPITokens(tc.ctx, &commodorepb.AdminListAPITokensRequest{})
			wantCode(t, err, codes.PermissionDenied)
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("denied caller reached the database: %v", err)
			}
		})
	}
}

// The interceptor refuses every JWT on a service-only method, which would lock
// the operator out of an RPC that admits nothing else.
func TestAdminListAPITokensAcceptsJWTAtInterceptor(t *testing.T) {
	for _, method := range commodoreServiceOnlyMethods() {
		if method == commodorepb.DeveloperService_AdminListAPITokens_FullMethodName {
			t.Fatal("AdminListAPITokens is service-only; operator JWTs cannot reach it")
		}
	}
}

func TestAdminListAPITokensRejectsMalformedTenant(t *testing.T) {
	s, _, done := newMockServer(t)
	defer done()
	_, err := s.AdminListAPITokens(operatorJWTCtx(), &commodorepb.AdminListAPITokensRequest{TenantId: "not-a-uuid"})
	wantCode(t, err, codes.InvalidArgument)
}

type acceptedSetArg struct{}

// The bound accepted set must be exactly the set CreateAPIToken accepts.
func (acceptedSetArg) Match(v driver.Value) bool {
	want, err := pq.Array(acceptedAPITokenPermissions()).Value()
	return err == nil && v == want
}

func TestAdminListAPITokensListsAcrossTenantsWithUnsupportedScopes(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlNoTokenValue{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	s, _, done := newMockServer(t)
	done()
	s.db = db

	t1 := time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT COUNT").
		WithArgs("", true, acceptedSetArg{}).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("FROM commodore.api_tokens").
		WithArgs("", true, acceptedSetArg{}, int32(3)).
		WillReturnRows(sqlmock.NewRows(adminTokenColumns).
			AddRow("tok-a", "tenant-a", "legacy", "{read,streams:read}", "active", nil, nil, t1).
			AddRow("tok-b", "tenant-b", "old", "{write}", "inactive", t2, t2, t2).
			AddRow("tok-c", "tenant-b", "extra", "{read}", "active", nil, nil, t3))

	resp, err := s.AdminListAPITokens(operatorJWTCtx(), &commodorepb.AdminListAPITokensRequest{
		UnsupportedScopesOnly: true,
		Pagination:            &commonpb.CursorPaginationRequest{First: 2},
	})
	if err != nil {
		t.Fatalf("AdminListAPITokens: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetTokens()) != 2 {
		t.Fatalf("tokens = %d, want page of 2", len(resp.GetTokens()))
	}
	first := resp.GetTokens()[0]
	if first.GetTenantId() != "tenant-a" || strings.Join(first.GetUnsupportedPermissions(), ",") != "read" {
		t.Fatalf("first token = %+v", first)
	}
	second := resp.GetTokens()[1]
	if second.GetTenantId() != "tenant-b" || second.GetLastUsedAt() == nil || second.GetExpiresAt() == nil {
		t.Fatalf("second token = %+v", second)
	}
	p := resp.GetPagination()
	if !p.GetHasNextPage() || p.GetTotalCount() != 3 || p.GetEndCursor() == "" {
		t.Fatalf("pagination = %+v", p)
	}
}

func TestAdminListAPITokensFollowsCursorAndTenantFilter(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()
	const tenant = "11111111-1111-1111-1111-111111111111"
	cursorTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	after := pagination.EncodeCursor(cursorTime, "22222222-2222-2222-2222-222222222222")
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(tenant, false, acceptedSetArg{}).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery(`\(created_at, id\) < \(\$5::timestamp, \$6::uuid\)`).
		WithArgs(tenant, false, acceptedSetArg{}, int32(51), cursorTime, "22222222-2222-2222-2222-222222222222").
		WillReturnRows(sqlmock.NewRows(adminTokenColumns).
			AddRow("tok-z", tenant, "ci", "{streams:read}", "active", nil, nil, cursorTime.Add(-time.Hour)))

	resp, err := s.AdminListAPITokens(operatorJWTCtx(), &commodorepb.AdminListAPITokensRequest{
		TenantId:   tenant,
		Pagination: &commonpb.CursorPaginationRequest{After: &after},
	})
	if err != nil {
		t.Fatalf("AdminListAPITokens: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if len(resp.GetTokens()) != 1 || resp.GetTokens()[0].GetUnsupportedPermissions() != nil || resp.GetPagination().GetHasNextPage() {
		t.Fatalf("resp = %+v", resp)
	}
}
