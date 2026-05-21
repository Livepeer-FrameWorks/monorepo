package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultConnectTimeout = 5 * time.Second

// GatewayClient communicates with the Gateway MCP server to invoke platform
// tools (diagnostics, stream management, billing, etc.) on behalf of the
// chat orchestrator.
type GatewayClient struct {
	client        *mcp.Client
	session       *mcp.ClientSession
	logger        logging.Logger
	cfg           Config
	endpointIndex int

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
	// GatewayURLs is the ordered Gateway MCP endpoint failover set.
	GatewayURLs []string
	// ConnectTimeout bounds endpoint dial/TLS setup and tools/list discovery.
	ConnectTimeout time.Duration
	// ToolAllowlist restricts which Gateway tools are exposed to the LLM.
	// An empty list means all discovered tools are exposed.
	ToolAllowlist []string
	// ToolDenylist excludes specific tools by name. Takes precedence over
	// the allowlist. Use this to suppress Gateway tools that the local
	// orchestrator already implements (e.g. ask_consultant).
	ToolDenylist []string
	Logger       logging.Logger
}

// New creates a GatewayClient and connects to the Gateway MCP server.
// Session establishment is unauthenticated (initialize and tools/list are
// publicly allowed). Each CallTool request injects the calling user's JWT
// via context so the Gateway scopes operations to the correct tenant.
func New(ctx context.Context, cfg Config) (*GatewayClient, error) {
	cfg.GatewayURLs = normalizeGatewayURLs(cfg)
	if len(cfg.GatewayURLs) == 0 {
		return nil, fmt.Errorf("mcpclient: GatewayURL is required")
	}
	cfg.GatewayURL = cfg.GatewayURLs[0]

	allowlist := make(map[string]struct{}, len(cfg.ToolAllowlist))
	for _, name := range cfg.ToolAllowlist {
		allowlist[name] = struct{}{}
	}
	denylist := make(map[string]struct{}, len(cfg.ToolDenylist))
	for _, name := range cfg.ToolDenylist {
		denylist[name] = struct{}{}
	}

	impl := &mcp.Implementation{
		Name:    "skipper",
		Version: "1.0.0",
	}
	client := mcp.NewClient(impl, nil)

	gc := &GatewayClient{
		client:    client,
		logger:    cfg.Logger,
		cfg:       cfg,
		allowlist: allowlist,
		denylist:  denylist,
	}

	if err := gc.connect(ctx, 0); err != nil {
		return nil, err
	}

	return gc, nil
}

func normalizeGatewayURLs(cfg Config) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		u := strings.TrimRight(strings.TrimSpace(raw), "/")
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, u := range cfg.GatewayURLs {
		add(u)
	}
	add(cfg.GatewayURL)
	return out
}

// AvailableTools returns the Gateway tools converted to llm.Tool format,
// filtered by the allowlist if one was configured.
func (gc *GatewayClient) AvailableTools() []llm.Tool {
	if gc == nil {
		return nil
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	return gc.tools
}

// HasTool reports whether the named tool is available from the Gateway.
func (gc *GatewayClient) HasTool(name string) bool {
	if gc == nil {
		return false
	}
	gc.mu.RLock()
	defer gc.mu.RUnlock()
	_, ok := gc.toolIndex[name]
	return ok
}

// CallTool invokes a Gateway MCP tool. The user's JWT is extracted from
// ctx (via ctxkeys.KeyJWTToken) and injected into the HTTP request so the
// Gateway authenticates the call for the correct tenant.
func (gc *GatewayClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	if gc == nil {
		return "", fmt.Errorf("mcpclient: gateway client is not connected")
	}
	gc.mu.RLock()
	_, allowed := gc.toolIndex[name]
	gc.mu.RUnlock()
	if !allowed {
		return "", fmt.Errorf("tool %q is not available", name)
	}

	var args map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("mcpclient: unmarshal arguments for %s: %w", name, err)
		}
	}

	gc.mu.RLock()
	session := gc.session
	gc.mu.RUnlock()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		if reconnErr := gc.tryReconnect(ctx); reconnErr == nil {
			gc.mu.RLock()
			session = gc.session
			gc.mu.RUnlock()
			result, err = session.CallTool(ctx, &mcp.CallToolParams{
				Name:      name,
				Arguments: args,
			})
		}
		if err != nil {
			return "", fmt.Errorf("mcpclient: call %s: %w", name, err)
		}
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

// tryReconnect attempts a single reconnect to the Gateway MCP server.
func (gc *GatewayClient) tryReconnect(ctx context.Context) error {
	if gc.logger != nil {
		gc.logger.Info("MCP session error; attempting Gateway MCP reconnect")
	}
	gc.mu.RLock()
	start := gc.endpointIndex + 1
	gc.mu.RUnlock()
	if err := gc.connect(ctx, start); err != nil {
		return err
	}
	return nil
}

func (gc *GatewayClient) connect(ctx context.Context, start int) error {
	if len(gc.cfg.GatewayURLs) == 0 {
		return fmt.Errorf("mcpclient: GatewayURL is required")
	}
	connectTimeout := gc.cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}
	var errs []string
	for offset := 0; offset < len(gc.cfg.GatewayURLs); offset++ {
		idx := (start + offset) % len(gc.cfg.GatewayURLs)
		endpoint := gc.cfg.GatewayURLs[idx]
		session, err := gc.client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: endpoint,
			HTTPClient: &http.Client{
				Transport: &authTransport{base: gatewayHTTPTransport(connectTimeout)},
			},
		}, nil)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", endpoint, err))
			continue
		}

		toolsCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		tools, toolIndex, err := gc.fetchTools(toolsCtx, session)
		cancel()
		if err != nil {
			_ = session.Close()
			errs = append(errs, fmt.Sprintf("%s: discover tools: %v", endpoint, err))
			continue
		}

		gc.mu.Lock()
		old := gc.session
		gc.session = session
		gc.endpointIndex = idx
		gc.cfg.GatewayURL = endpoint
		gc.tools = tools
		gc.toolIndex = toolIndex
		gc.mu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		if gc.logger != nil {
			gc.logger.WithField("count", len(tools)).Info("Discovered Gateway MCP tools")
			gc.logger.WithField("url", endpoint).Info("Connected to Gateway MCP")
		}
		return nil
	}
	return fmt.Errorf("mcpclient: connect to gateway MCP: %s", strings.Join(errs, "; "))
}

func gatewayHTTPTransport(connectTimeout time.Duration) http.RoundTripper {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: connectTimeout,
		TLSHandshakeTimeout:   connectTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// Close shuts down the MCP client session.
func (gc *GatewayClient) Close() error {
	if gc == nil {
		return nil
	}
	if gc.session != nil {
		return gc.session.Close()
	}
	return nil
}

// refreshTools fetches the tool list from the Gateway and builds the
// internal index, applying the allowlist filter.
func (gc *GatewayClient) refreshTools(ctx context.Context) error {
	gc.mu.RLock()
	session := gc.session
	gc.mu.RUnlock()
	tools, toolIndex, err := gc.fetchTools(ctx, session)
	if err != nil {
		return err
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

func (gc *GatewayClient) fetchTools(ctx context.Context, session *mcp.ClientSession) ([]llm.Tool, map[string]struct{}, error) {
	if session == nil {
		return nil, nil, fmt.Errorf("gateway session is not connected")
	}
	result, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, nil, err
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
	return tools, toolIndex, nil
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

// authTransport injects the calling user's auth token into each HTTP request.
// Session establishment requests (initialize, tools/list) go unauthenticated;
// tool calls carry the user's JWT or API token from the chat handler context.
type authTransport struct {
	base http.RoundTripper
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())

	if token := ctxkeys.GetJWTToken(req.Context()); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	} else if token := ctxkeys.GetAPIToken(req.Context()); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return t.base.RoundTrip(req)
}
