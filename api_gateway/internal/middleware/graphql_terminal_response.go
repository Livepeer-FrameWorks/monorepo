package middleware

import (
	"context"

	"github.com/99designs/gqlgen/graphql"
)

// TerminalGraphQLError finishes after one response. WebSocket transports call
// response handlers repeatedly until nil, including when their socket closes.
func TerminalGraphQLError(ctx context.Context, message, code string) graphql.ResponseHandler {
	response := graphql.ErrorResponse(ctx, "%s", message)
	if code != "" {
		response.Errors[0].Extensions = map[string]any{"code": code}
	}
	return graphql.OneShot(response)
}
