package middleware

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/99designs/gqlgen/graphql"
	"github.com/vektah/gqlparser/v2/ast"
)

func wsMutationContext(tenantID, fieldName string) context.Context {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, tenantID)
	return graphql.WithOperationContext(ctx, &graphql.OperationContext{
		Operation: &ast.OperationDefinition{
			Operation:    ast.Mutation,
			SelectionSet: ast.SelectionSet{&ast.Field{Name: fieldName}},
		},
	})
}

func runAccessOperation(middleware graphql.OperationMiddleware, ctx context.Context) (bool, *graphql.Response) {
	reached := false
	handler := middleware(ctx, func(context.Context) graphql.ResponseHandler {
		reached = true
		return func(context.Context) *graphql.Response { return &graphql.Response{} }
	})
	return reached, handler(ctx)
}

func TestGraphQLOperationAccessAllowsUnfundedControlAndBlocksRatedMutation(t *testing.T) {
	middleware := GraphQLOperationAccess(fakeBillingChecker{billingModel: "prepaid", isBalanceNegative: true})

	if reached, response := runAccessOperation(middleware, wsMutationContext("tenant-1", "createStream")); !reached || len(response.Errors) != 0 {
		t.Fatalf("unfunded control mutation denied: reached=%v response=%+v", reached, response)
	}
	if reached, response := runAccessOperation(middleware, wsMutationContext("tenant-1", "createClip")); reached || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "INSUFFICIENT_BALANCE" {
		t.Fatalf("unfunded rated mutation not denied correctly: reached=%v response=%+v", reached, response)
	}
}

func TestGraphQLOperationAccessFailsClosedForRatedWorkOnStatusOutage(t *testing.T) {
	middleware := GraphQLOperationAccess(fakeBillingChecker{err: context.DeadlineExceeded})

	if reached, _ := runAccessOperation(middleware, wsMutationContext("tenant-1", "createStream")); !reached {
		t.Fatal("control mutation should remain available during a billing-status outage")
	}
	if reached, response := runAccessOperation(middleware, wsMutationContext("tenant-1", "createClip")); reached || len(response.Errors) != 1 || response.Errors[0].Extensions["code"] != "BILLING_STATUS_UNAVAILABLE" {
		t.Fatalf("rated mutation did not fail retryably: reached=%v response=%+v", reached, response)
	}
}
