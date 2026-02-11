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
			Name: "ask_consultant",
			Description: `Ask the AI video streaming consultant a question.

Returns a structured answer with confidence tagging and source citations, powered by the full Skipper pipeline (knowledge retrieval, query rewriting, reranking, optional web search, multi-step reasoning).

Knowledge domains: FrameWorks platform, MistServer, FFmpeg, OBS, SRT, HLS, nginx-rtmp, Livepeer, WebRTC, DASH, and related streaming ecosystem tools. Read knowledge://sources for the full indexed source list.

Query tips: Be specific — include the protocol, codec, or tool name. "How do I reduce HLS latency with LL-HLS segment tuning?" yields better results than "how to reduce latency?". For platform-specific questions, mention FrameWorks explicitly.

Confidence tags in the response:
- verified: grounded in indexed documentation with citations
- sourced: found via web search with URL references
- best_guess: inferred from adjacent knowledge, treat as advisory
- unknown: no strong evidence found, verify independently

Set mode to "docs" for indexed knowledge base only (faster, no web search). Default "full" enables web search and multi-step reasoning.

For raw diagnostic data, use the QoE tools (diagnose_rebuffering, diagnose_buffer_health, diagnose_packet_loss, diagnose_routing, get_stream_health_summary, get_anomaly_report) directly, then pass results to ask_consultant for interpretation and recommendations.`,
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args AskConsultantInput) (*mcp.CallToolResult, any, error) {
			return proxyToSkipper(ctx, skipper, "ask_consultant", args, logger)
		},
	)
}

// AskConsultantInput is the schema for the proxied ask_consultant tool.
type AskConsultantInput struct {
	Question string `json:"question" jsonschema:"required" jsonschema_description:"Your question. Be specific: include protocol, codec, or tool names for better results. Example: 'How do I configure SRT latency in MistServer for a 500ms target?'"`
	Mode     string `json:"mode,omitempty" jsonschema_description:"Pipeline mode: 'full' (default) uses knowledge base + web search + reasoning. 'docs' restricts to indexed knowledge base only (faster, cheaper, no web)."`
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
