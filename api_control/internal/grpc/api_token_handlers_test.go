package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
)

// captureArg records the bound value so a test can assert on it after the
// handler returns (sqlmock matches args during Exec, before the response is
// available).
type captureArg struct {
	got *string
}

func (c captureArg) Match(v driver.Value) bool {
	if s, ok := v.(string); ok {
		*c.got = s
		return true
	}
	return false
}

func TestCreateAPIToken(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.CreateAPIToken(context.Background(), &commodorepb.CreateAPITokenRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("happy_path_persists_hash_returns_plaintext_once", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		var storedToken string
		mock.ExpectBegin()
		mock.ExpectExec("INSERT INTO commodore.api_tokens").
			WithArgs(
				sqlmock.AnyArg(), // id
				testTenantID,     // tenant_id
				"u1",             // user_id
				captureArg{&storedToken},
				sqlmock.AnyArg(), // token_name
				sqlmock.AnyArg(), // permissions (pq.Array)
				sqlmock.AnyArg(), // expires_at
			).
			WillReturnResult(sqlmock.NewResult(0, 1))
		expectDualEventInsert(mock, "api_token.created", eventTokenCreated)
		mock.ExpectCommit()

		resp, err := s.CreateAPIToken(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateAPITokenRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// The caller receives the plaintext token exactly once.
		if !strings.HasPrefix(resp.GetTokenValue(), "fw_") {
			t.Errorf("token value %q is not an fw_ token", resp.GetTokenValue())
		}
		// What lands in the DB must be the HASH of that token, never the token.
		if storedToken == resp.GetTokenValue() {
			t.Fatal("plaintext token was persisted to the database")
		}
		if storedToken != hashToken(resp.GetTokenValue()) {
			t.Errorf("stored value %q is not hashToken(tokenValue)", storedToken)
		}
		// Default permission is useful and least-privilege.
		if len(resp.GetPermissions()) != 1 || resp.GetPermissions()[0] != "streams:read" {
			t.Errorf("default permissions = %v, want [streams:read]", resp.GetPermissions())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("delegated_api_token_cannot_mint_credentials", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		ctx := context.WithValue(ctxAs("u1", testTenantID, "owner"), ctxkeys.KeyAuthType, "api_token")
		_, err := s.CreateAPIToken(ctx, &commodorepb.CreateAPITokenRequest{Permissions: []string{"streams:write"}})
		wantCode(t, err, codes.PermissionDenied)
	})

	t.Run("wildcard_and_unknown_permissions_are_rejected", func(t *testing.T) {
		for _, permission := range []string{"*", "root:all"} {
			t.Run(permission, func(t *testing.T) {
				s, _, done := newMockServer(t)
				defer done()
				_, err := s.CreateAPIToken(ctxAs("u1", testTenantID, "owner"), &commodorepb.CreateAPITokenRequest{Permissions: []string{permission}})
				wantCode(t, err, codes.InvalidArgument)
			})
		}
	})
}

func TestNormalizeAPITokenPermissionsIncludesEveryMCPGrant(t *testing.T) {
	permissions := []string{
		"account:read", "analytics:read", "billing:read", "billing:write",
		"consultant:use", "developer:read", "developer:write",
		"infrastructure:read", "infrastructure:write", "mcp:high-risk",
		"placement:read", "placement:write",
		"security:read", "security:write", "settings:write",
		"streams:read", "streams:write", "support:read",
	}
	got, err := normalizeAPITokenPermissions(permissions)
	if err != nil {
		t.Fatalf("documented MCP grants rejected: %v", err)
	}
	if len(got) != len(permissions) {
		t.Fatalf("normalized grants = %v", got)
	}
}

func TestNormalizeAPITokenPermissionsRejectsCoarseScopes(t *testing.T) {
	for _, permission := range []string{"read", "write"} {
		if _, err := normalizeAPITokenPermissions([]string{permission}); err == nil {
			t.Fatalf("coarse permission %q was accepted", permission)
		}
	}
}

func TestListAPITokens(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.ListAPITokens(context.Background(), &commodorepb.ListAPITokensRequest{})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("happy_path_projects_tokens", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		mock.ExpectQuery("SELECT COUNT").
			WithArgs("u1", true, testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery("FROM commodore.api_tokens").
			WithArgs("u1", true, testTenantID, int32(51)).
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "token_name", "permissions", "status", "last_used_at", "expires_at", "created_at",
			}).AddRow("tok1", "ci", "{read,write}", "active", nil, nil, now))

		resp, err := s.ListAPITokens(ctxAs("u1", testTenantID, "owner"), &commodorepb.ListAPITokensRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.GetTokens()) != 1 {
			t.Fatalf("tokens = %d, want 1", len(resp.GetTokens()))
		}
		tok := resp.GetTokens()[0]
		if tok.GetStatus() != "active" {
			t.Errorf("status = %q, want active", tok.GetStatus())
		}
		if len(tok.GetPermissions()) != 2 {
			t.Errorf("permissions = %v, want 2 entries", tok.GetPermissions())
		}
		if resp.GetPagination().GetTotalCount() != 1 {
			t.Errorf("total = %d, want 1", resp.GetPagination().GetTotalCount())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}

func TestRevokeAPIToken(t *testing.T) {
	t.Run("unauthenticated", func(t *testing.T) {
		s, _, done := newMockServer(t)
		defer done()
		_, err := s.RevokeAPIToken(context.Background(), &commodorepb.RevokeAPITokenRequest{TokenId: "tok1"})
		wantCode(t, err, codes.Unauthenticated)
	})

	t.Run("not_found", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE commodore.api_tokens").
			WithArgs("tok1", "u1", false, testTenantID).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()
		_, err := s.RevokeAPIToken(ctxAs("u1", testTenantID, "member"), &commodorepb.RevokeAPITokenRequest{TokenId: "tok1"})
		wantCode(t, err, codes.NotFound)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("happy_path_deactivates_and_emits", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE commodore.api_tokens").
			WithArgs("tok1", "u1", false, testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"token_name", "was_active"}).AddRow("ci", true))
		expectDualEventInsert(mock, "api_token.revoked", eventTokenRevoked)
		mock.ExpectCommit()

		resp, err := s.RevokeAPIToken(ctxAs("u1", testTenantID, "member"), &commodorepb.RevokeAPITokenRequest{TokenId: "tok1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetTokenName() != "ci" || resp.GetTokenId() != "tok1" {
			t.Errorf("unexpected response: %+v", resp)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("tenant_manager_can_revoke_another_users_token", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE commodore.api_tokens").
			WithArgs("tok2", "owner1", true, testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"token_name", "was_active"}).AddRow("departed-user-token", true))
		expectDualEventInsert(mock, "api_token.revoked", eventTokenRevoked)
		mock.ExpectCommit()

		resp, err := s.RevokeAPIToken(ctxAs("owner1", testTenantID, "owner"), &commodorepb.RevokeAPITokenRequest{TokenId: "tok2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.GetTokenName() != "departed-user-token" {
			t.Fatalf("token name = %q", resp.GetTokenName())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})

	t.Run("repeated_revoke_records_no_event", func(t *testing.T) {
		s, mock, done := newMockServer(t)
		defer done()
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE commodore.api_tokens").
			WithArgs("tok1", "u1", false, testTenantID).
			WillReturnRows(sqlmock.NewRows([]string{"token_name", "was_active"}).AddRow("ci", false))
		mock.ExpectCommit()

		if _, err := s.RevokeAPIToken(ctxAs("u1", testTenantID, "member"), &commodorepb.RevokeAPITokenRequest{TokenId: "tok1"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("expectations: %v", err)
		}
	})
}
