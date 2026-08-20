package middleware

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// GraphQLOperationAccess applies billing/access classification to WebSocket-
// borne operations. HTTP operations already passed through EvaluateAccess and
// are deliberately skipped to avoid duplicate status checks.
func GraphQLOperationAccess(billingChecker BillingChecker) graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
			return next(ctx)
		}
		if billingChecker == nil || !graphql.HasOperationContext(ctx) || ctxkeys.GetTenantID(ctx) == "" {
			return next(ctx)
		}

		opCtx := graphql.GetOperationContext(ctx)
		if opCtx == nil || opCtx.Operation == nil {
			return next(ctx)
		}
		fields := graphql.CollectFields(opCtx, opCtx.Operation.SelectionSet, nil)
		names := make([]string, 0, len(fields))
		for _, field := range fields {
			names = append(names, field.Name)
		}
		req := AccessRequest{
			TenantID:       ctxkeys.GetTenantID(ctx),
			OperationName:  opCtx.Operation.Name,
			OperationNames: names,
			OperationType:  string(opCtx.Operation.Operation),
			Path:           "/graphql/ws",
		}
		unfundedAllowed, classified := requestAllowedWithoutFunds(req)
		status, err := billingChecker.GetBillingAccessStatus(req.TenantID)
		if err != nil {
			if !classified || !unfundedAllowed {
				return graphqlAccessDenied("billing status is temporarily unavailable", "BILLING_STATUS_UNAVAILABLE")
			}
			return next(ctx)
		}
		if status.IsSuspended && !suspendedRequestAllowed(req) {
			return graphqlAccessDenied("account is suspended", "ACCOUNT_SUSPENDED")
		}
		if status.BillingModel == "prepaid" && status.IsBalanceNegative {
			if !classified {
				return graphqlAccessDenied("operation access policy is unavailable", "ACCESS_POLICY_UNCLASSIFIED")
			}
			if !unfundedAllowed {
				return graphqlAccessDenied("payment required for rated work", "INSUFFICIENT_BALANCE")
			}
		}
		if status.BillingModel == "postpaid" && !unfundedAllowed {
			if status.TierName == "" {
				return graphqlAccessDenied("billing status is temporarily unavailable", "BILLING_STATUS_UNAVAILABLE")
			}
			if !strings.EqualFold(status.TierName, "free") && !status.CollectionReady {
				return graphqlAccessDenied("confirmed payment-provider setup is required for this paid tier", "PAYMENT_SETUP_REQUIRED")
			}
		}
		return next(ctx)
	}
}

func graphqlAccessDenied(message, code string) graphql.ResponseHandler {
	return func(context.Context) *graphql.Response {
		return &graphql.Response{Errors: gqlerror.List{&gqlerror.Error{
			Message:    message,
			Extensions: map[string]any{"code": code},
		}}}
	}
}
