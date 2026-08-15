package middleware

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gin-gonic/gin"
)

// GraphQLOperationAuth applies the same public-operation boundary to WebSocket
// operations that PublicOrJWTAuth applies to HTTP requests. The upgrade itself
// cannot be authorized from connectionParams; identity is resolved by gqlgen's
// InitFunc after the HTTP middleware has passed the request through.
func GraphQLOperationAuth() graphql.OperationMiddleware {
	return func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		if ginCtx, ok := ctx.Value(ctxkeys.KeyGinContext).(*gin.Context); ok && ginCtx != nil {
			return next(ctx)
		}
		if !graphql.HasOperationContext(ctx) {
			return next(ctx)
		}
		if GetUserFromContext(ctx) != nil {
			return next(ctx)
		}

		opCtx := graphql.GetOperationContext(ctx)
		if opCtx != nil && isAllowlistedOperation(opCtx.RawQuery, opCtx.OperationName) {
			return next(context.WithValue(ctx, ctxkeys.KeyPublicAllowlisted, true))
		}

		return func(ctx context.Context) *graphql.Response {
			return graphql.ErrorResponse(ctx, "authentication required")
		}
	}
}
