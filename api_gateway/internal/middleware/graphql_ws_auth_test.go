package middleware

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

func wsAuthOperationContext(query, operationName string) context.Context {
	return graphql.WithOperationContext(context.Background(), &graphql.OperationContext{
		RawQuery:      query,
		OperationName: operationName,
		Operation: &ast.OperationDefinition{
			Operation: ast.Query,
		},
	})
}

func runAuthOperation(mw graphql.OperationMiddleware, ctx context.Context) (reached, public bool) {
	handler := mw(ctx, func(nextCtx context.Context) graphql.ResponseHandler {
		reached = true
		public = ctxkeys.IsPublicAllowlisted(nextCtx)
		return func(context.Context) *graphql.Response { return &graphql.Response{} }
	})
	handler(ctx)
	return reached, public
}

func TestGraphQLOperationAuthRejectsAnonymousProtectedWebSocketQuery(t *testing.T) {
	ctx := wsAuthOperationContext(`query { validateStreamKey(streamKey: "sk_secret") { status } }`, "")
	if reached, _ := runAuthOperation(GraphQLOperationAuth(), ctx); reached {
		t.Fatal("anonymous WebSocket operation bypassed the protected-query boundary")
	}
}

func TestGraphQLOperationAuthAllowsOnlyPublicWebSocketQuery(t *testing.T) {
	ctx := wsAuthOperationContext(`query { resolveIngestEndpoint(streamKey: "sk_secret") { primary { whipUrl } } }`, "")
	reached, public := runAuthOperation(GraphQLOperationAuth(), ctx)
	if !reached {
		t.Fatal("public WebSocket operation was rejected")
	}
	if !public {
		t.Fatal("public WebSocket operation did not receive the allowlisted context marker")
	}
}

func TestGraphQLOperationAuthAllowsAuthenticatedWebSocketQuery(t *testing.T) {
	ctx := wsAuthOperationContext(`query { validateStreamKey(streamKey: "sk_secret") { status } }`, "")
	ctx = context.WithValue(ctx, ctxkeys.KeyUser, &UserContext{UserID: "user-1", TenantID: "tenant-1"})
	if reached, _ := runAuthOperation(GraphQLOperationAuth(), ctx); !reached {
		t.Fatal("authenticated WebSocket operation was rejected")
	}
}
