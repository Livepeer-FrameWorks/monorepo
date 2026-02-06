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

// RegisterSkipperTools registers Skipper proxy tools (search_knowledge, search_web)
// on the Gateway MCP. Calls are forwarded to Skipper's spoke endpoint.
// The skipper client may connect lazily, so tools are always registered;
// individual calls return an error if the spoke is unreachable.
func RegisterSkipperTools(server *mcp.Server, skipper SkipperCaller, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_knowledge",
			Description: "Search the curated knowledge base for platform-specific guidance and verified documentation.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args SearchKnowledgeInput) (*mcp.CallToolResult, any, error) {
			return proxyToSkipper(ctx, skipper, "search_knowledge", args, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_web",
			Description: "Search the public web for documentation or references when the knowledge base is insufficient.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args SearchWebInput) (*mcp.CallToolResult, any, error) {
			return proxyToSkipper(ctx, skipper, "search_web", args, logger)
		},
	)
}

// SearchKnowledgeInput is the schema for the proxied search_knowledge tool.
type SearchKnowledgeInput struct {
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the knowledge base"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 5)"`
	TenantScope string `json:"tenant_scope,omitempty" jsonschema_description:"Scope to search: tenant, global, or all (default all)"`
}

// SearchWebInput is the schema for the proxied search_web tool.
type SearchWebInput struct {
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the web"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 5)"`
	SearchDepth string `json:"search_depth,omitempty" jsonschema_description:"Search depth: basic or advanced (default basic)"`
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
