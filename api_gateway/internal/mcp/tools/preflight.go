package tools

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/mcp/preflight"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func requirePositiveBalance(ctx context.Context, checker *preflight.Checker) (*mcp.CallToolResult, any, error) {
	if checker == nil {
		return nil, nil, nil
	}
	if err := checker.RequireBalance(ctx); err != nil {
		if pfe, ok := preflight.IsPreflightError(err); ok {
			return toolErrorWithResolution(pfe.Blocker)
		}
		return toolError(fmt.Sprintf("failed to check balance: %v", err))
	}
	return nil, nil, nil
}

func requireConfirmation(got, want string) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(got) != want {
		return toolError(fmt.Sprintf("confirmation required: set confirm to %q", want))
	}
	return nil, nil, nil
}
