package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SkipperCaller forwards tool calls to the Skipper spoke MCP server.
type SkipperCaller interface {
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

// RegisterSkipperTools registers the ask_consultant proxy tool on the Gateway
// MCP. The call is forwarded to Skipper's spoke endpoint. The skipper client
// may connect lazily, so the tool is always registered; calls return an error
// if the spoke is unreachable.
func RegisterSkipperTools(server *mcp.Server, skipper SkipperCaller, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "ask_consultant",
			Description: "Ask the AI video streaming consultant a question. Returns a complete answer with confidence tagging and source citations, powered by the full Skipper pipeline (knowledge retrieval, web search, multi-step reasoning).",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AskConsultantInput) (*mcp.CallToolResult, any, error) {
			return proxyToSkipper(ctx, skipper, "ask_consultant", args, logger)
		},
	)
}

// AskConsultantInput is the schema for the proxied ask_consultant tool.
type AskConsultantInput struct {
	Question string `json:"question" jsonschema:"required" jsonschema_description:"Question for the AI video streaming consultant"`
	Mode     string `json:"mode,omitempty" jsonschema_description:"Set to docs for read-only mode (default full)"`
}

func proxyToSkipper(ctx context.Context, skipper SkipperCaller, toolName string, args any, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required to use " + toolName)
	}

	// Inject tenant_id into the arguments before forwarding.
	raw, err := json.Marshal(args)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to marshal arguments: %v", err))
	}
	var payload map[string]any
	if unmarshalErr := json.Unmarshal(raw, &payload); unmarshalErr != nil {
		return toolError(fmt.Sprintf("Failed to parse arguments: %v", unmarshalErr))
	}
	payload["tenant_id"] = tenantID
	enriched, err := json.Marshal(payload)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to re-marshal arguments: %v", err))
	}

	result, err := skipper.CallTool(ctx, toolName, enriched)
	if err != nil {
		if logger != nil {
			logger.WithError(err).WithField("tool", toolName).Warn("Skipper proxy call failed")
		}
		return toolError(fmt.Sprintf("Skipper %s failed: %v", toolName, err))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: result}},
	}, nil, nil
}
