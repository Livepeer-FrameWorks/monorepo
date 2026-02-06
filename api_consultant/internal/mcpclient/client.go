package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GatewayClient communicates with the Gateway MCP server to invoke platform
// tools (diagnostics, stream management, billing, etc.) on behalf of the
// chat orchestrator.
type GatewayClient struct {
	client  *mcp.Client
	session *mcp.ClientSession
	logger  logging.Logger

	mu        sync.RWMutex
	tools     []llm.Tool
	toolIndex map[string]struct{}
	allowlist map[string]struct{}
	denylist  map[string]struct{}
}

// Config configures the Gateway MCP client.
type Config struct {
	// GatewayURL is the base URL for the Gateway MCP endpoint.
	GatewayURL string
	// ServiceToken authenticates the long-lived session.
	ServiceToken string
	// ToolAllowlist restricts which Gateway tools are exposed to the LLM.
	// An empty list means all discovered tools are exposed.
	ToolAllowlist []string
	// ToolDenylist excludes specific tools by name. Takes precedence over
	// the allowlist. Use this to suppress Gateway tools that the local
	// orchestrator already implements (e.g. search_knowledge, search_web).
	ToolDenylist []string
	Logger       logging.Logger
}

// New creates a GatewayClient and connects to the Gateway MCP server.
// The connection uses a service token for session establishment, but each
// CallTool request injects the calling user's JWT via context so the
// Gateway scopes operations to the correct tenant.
func New(ctx context.Context, cfg Config) (*GatewayClient, error) {
	if cfg.GatewayURL == "" {
		return nil, fmt.Errorf("mcpclient: GatewayURL is required")
	}

	allowlist := make(map[string]struct{}, len(cfg.ToolAllowlist))
	for _, name := range cfg.ToolAllowlist {
		allowlist[name] = struct{}{}
	}
	denylist := make(map[string]struct{}, len(cfg.ToolDenylist))
	for _, name := range cfg.ToolDenylist {
		denylist[name] = struct{}{}
	}

	transport := &mcp.StreamableClientTransport{
		Endpoint: cfg.GatewayURL,
		HTTPClient: &http.Client{
			Transport: &authTransport{
				base:         http.DefaultTransport,
				serviceToken: cfg.ServiceToken,
			},
		},
	}

	impl := &mcp.Implementation{
		Name:    "skipper",
		Version: "1.0.0",
	}
	client := mcp.NewClient(impl, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: connect to gateway MCP: %w", err)
	}

	gc := &GatewayClient{
		client:    client,
		session:   session,
		logger:    cfg.Logger,
		allowlist: allowlist,
		denylist:  denylist,
	}

	if err := gc.refreshTools(ctx); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("mcpclient: discover tools: %w", err)
	}

	return gc, nil
}

// AvailableTools returns the Gateway tools converted to llm.Tool format,
// filtered by the allowlist if one was configured.
func (gc *GatewayClient) AvailableTools() []llm.Tool {
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.tools
}

// HasTool reports whether the named tool is available from the Gateway.
func (gc *GatewayClient) HasTool(name string) bool {
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	_, ok := gc.toolIndex[name]
	return ok
}

// CallTool invokes a Gateway MCP tool. The user's JWT is extracted from
// ctx (via ctxkeys.KeyJWTToken) and injected into the HTTP request so the
// Gateway authenticates the call for the correct tenant.
func (gc *GatewayClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	var args map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("mcpclient: unmarshal arguments for %s: %w", name, err)
		}
	}

	result, err := gc.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("mcpclient: call %s: %w", name, err)
	}

	if result.IsError {
		text := extractTextContent(result)
		if text != "" {
			return "", fmt.Errorf("mcpclient: tool %s returned error: %s", name, text)
		}
		return "", fmt.Errorf("mcpclient: tool %s returned error", name)
	}

	return extractTextContent(result), nil
}

// Close shuts down the MCP client session.
func (gc *GatewayClient) Close() error {
	if gc.session != nil {
		return gc.session.Close()
	}
	return nil
}

// refreshTools fetches the tool list from the Gateway and builds the
// internal index, applying the allowlist filter.
func (gc *GatewayClient) refreshTools(ctx context.Context) error {
	result, err := gc.session.ListTools(ctx, nil)
	if err != nil {
		return err
	}

	var tools []llm.Tool
	toolIndex := make(map[string]struct{}, len(result.Tools))

	for _, t := range result.Tools {
		if _, denied := gc.denylist[t.Name]; denied {
			continue
		}
		if len(gc.allowlist) > 0 {
			if _, ok := gc.allowlist[t.Name]; !ok {
				continue
			}
		}

		params := convertInputSchema(t.InputSchema)
		tools = append(tools, llm.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
		toolIndex[t.Name] = struct{}{}
	}

	gc.mu.Lock()
	gc.tools = tools
	gc.toolIndex = toolIndex
	gc.mu.Unlock()

	if gc.logger != nil {
		gc.logger.WithField("count", len(tools)).Info("Discovered Gateway MCP tools")
	}
	return nil
}

// convertInputSchema converts the MCP SDK's InputSchema (any) to the
// map[string]interface{} format used by llm.Tool.Parameters.
func convertInputSchema(schema any) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{
			"type":       "object",
			"properties": map[string]any{},
		}
	}
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	// Fallback: round-trip through JSON for unexpected types.
	data, err := json.Marshal(schema)
	if err != nil {
		return map[string]interface{}{"type": "object", "properties": map[string]any{}}
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]interface{}{"type": "object", "properties": map[string]any{}}
	}
	return m
}

// extractTextContent joins all TextContent entries from a CallToolResult.
func extractTextContent(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// authTransport injects authentication headers into each HTTP request.
// It reads the user's JWT from the request context (set by the chat
// handler) and falls back to the service token when no JWT is available.
type authTransport struct {
	base         http.RoundTripper
	serviceToken string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())

	if token := ctxkeys.GetJWTToken(req.Context()); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if t.serviceToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.serviceToken)
	}

	return t.base.RoundTrip(req)
}
