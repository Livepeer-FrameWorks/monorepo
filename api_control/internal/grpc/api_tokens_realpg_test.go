//go:build schema_verify

package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAPITokenRepository_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	server := &CommodoreServer{db: db, logger: logrus.New()}
	ctx := context.Background()
	const (
		tenantID = "10000000-0000-4000-8000-000000000031"
		otherID  = "10000000-0000-4000-8000-000000000032"
		userID   = "20000000-0000-4000-8000-000000000031"
	)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.users (id, tenant_id, email, role, platform_operator)
		VALUES ($1::uuid, $2::uuid, 'api-token@example.com', 'admin', true)
	`, userID, tenantID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	userCtx = context.WithValue(userCtx, ctxkeys.KeyTenantID, tenantID)
	otherCtx := context.WithValue(context.Background(), ctxkeys.KeyUserID, userID)
	otherCtx = context.WithValue(otherCtx, ctxkeys.KeyTenantID, otherID)

	first, err := server.CreateAPIToken(userCtx, &commodorepb.CreateAPITokenRequest{
		TokenName: "first", Permissions: []string{"read"},
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := server.CreateAPIToken(userCtx, &commodorepb.CreateAPITokenRequest{
		TokenName: "second", Permissions: []string{"read", "write"},
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	if first.GetTokenValue() == "" || second.GetTokenValue() == "" || first.GetTokenValue() == second.GetTokenValue() {
		t.Fatalf("create responses do not contain distinct one-time tokens: first=%#v second=%#v", first, second)
	}
	var storedHash string
	if err := db.QueryRowContext(ctx, `SELECT token_value FROM commodore.api_tokens WHERE id = $1::uuid`, first.GetId()).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != hashToken(first.GetTokenValue()) || storedHash == first.GetTokenValue() {
		t.Fatalf("stored token value is not the expected hash: %q", storedHash)
	}

	base := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Microsecond)
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.api_tokens
		SET created_at = CASE id WHEN $1::uuid THEN $3::timestamp WHEN $2::uuid THEN $4::timestamp END
		WHERE id IN ($1::uuid, $2::uuid)
	`, first.GetId(), second.GetId(), base, base.Add(time.Hour)); err != nil {
		t.Fatalf("order token fixtures: %v", err)
	}

	page1, err := server.ListAPITokens(userCtx, &commodorepb.ListAPITokensRequest{
		Pagination: &commonpb.CursorPaginationRequest{First: 1},
	})
	if err != nil || len(page1.GetTokens()) != 1 || page1.GetTokens()[0].GetId() != second.GetId() || !page1.GetPagination().GetHasNextPage() {
		t.Fatalf("forward page 1=%#v err=%v", page1, err)
	}
	after := page1.GetPagination().GetEndCursor()
	page2, err := server.ListAPITokens(userCtx, &commodorepb.ListAPITokensRequest{
		Pagination: &commonpb.CursorPaginationRequest{First: 1, After: &after},
	})
	if err != nil || len(page2.GetTokens()) != 1 || page2.GetTokens()[0].GetId() != first.GetId() {
		t.Fatalf("forward-after ids=%v want=[%s] cursor=%q err=%v", apiTokenIDs(page2), first.GetId(), after, err)
	}
	back1, err := server.ListAPITokens(userCtx, &commodorepb.ListAPITokensRequest{
		Pagination: &commonpb.CursorPaginationRequest{Last: 1},
	})
	if err != nil || len(back1.GetTokens()) != 1 || back1.GetTokens()[0].GetId() != first.GetId() || !back1.GetPagination().GetHasPreviousPage() {
		t.Fatalf("backward page 1=%#v err=%v", back1, err)
	}
	before := back1.GetPagination().GetStartCursor()
	back2, err := server.ListAPITokens(userCtx, &commodorepb.ListAPITokensRequest{
		Pagination: &commonpb.CursorPaginationRequest{Last: 1, Before: &before},
	})
	if err != nil || len(back2.GetTokens()) != 1 || back2.GetTokens()[0].GetId() != second.GetId() {
		t.Fatalf("backward-before page=%#v err=%v", back2, err)
	}

	valid, err := server.ValidateAPIToken(ctx, &commodorepb.ValidateAPITokenRequest{Token: first.GetTokenValue()})
	if err != nil || !valid.GetValid() || valid.GetUserId() != userID || valid.GetTenantId() != tenantID ||
		valid.GetEmail() != "api-token@example.com" || valid.GetRole() != "admin" || !valid.GetPlatformOperator() {
		t.Fatalf("validate response=%#v err=%v", valid, err)
	}
	var touched bool
	if err := db.QueryRowContext(ctx, `SELECT last_used_at IS NOT NULL FROM commodore.api_tokens WHERE id = $1::uuid`, first.GetId()).Scan(&touched); err != nil || !touched {
		t.Fatalf("last-used touch=%v err=%v", touched, err)
	}

	if _, err := server.RevokeAPIToken(otherCtx, &commodorepb.RevokeAPITokenRequest{TokenId: first.GetId()}); status.Code(err) != codes.NotFound {
		t.Fatalf("cross-tenant revoke code=%v err=%v", status.Code(err), err)
	}
	if _, err := server.RevokeAPIToken(userCtx, &commodorepb.RevokeAPITokenRequest{TokenId: first.GetId()}); err != nil {
		t.Fatalf("revoke own token: %v", err)
	}
	invalid, err := server.ValidateAPIToken(ctx, &commodorepb.ValidateAPITokenRequest{Token: first.GetTokenValue()})
	if err != nil || invalid.GetValid() {
		t.Fatalf("revoked token validation=%#v err=%v", invalid, err)
	}

	expired, err := server.CreateAPIToken(userCtx, &commodorepb.CreateAPITokenRequest{
		TokenName: "expired", ExpiresAt: timestamppb.New(time.Now().Add(-time.Minute)),
	})
	if err != nil {
		t.Fatalf("create expired fixture: %v", err)
	}
	expiredResult, err := server.ValidateAPIToken(ctx, &commodorepb.ValidateAPITokenRequest{Token: expired.GetTokenValue()})
	if err != nil || expiredResult.GetValid() {
		t.Fatalf("expired token validation=%#v err=%v", expiredResult, err)
	}
}

func apiTokenIDs(response *commodorepb.ListAPITokensResponse) []string {
	if response == nil {
		return nil
	}
	ids := make([]string, 0, len(response.GetTokens()))
	for _, token := range response.GetTokens() {
		ids = append(ids, token.GetId())
	}
	return ids
}
