package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// SkipperClient connects to Skipper's spoke MCP endpoint for proxying
// Skipper tools (ask_consultant) through the Gateway hub.
type SkipperClient struct {
	session *sdkmcp.ClientSession
	logger  logging.Logger
}

// SkipperClientConfig configures the connection to Skipper's spoke.
type SkipperClientConfig struct {
	SpokeURL     string
	ServiceToken string
	Logger       logging.Logger
}

// NewSkipperClient establishes an MCP session to Skipper's spoke endpoint.
func NewSkipperClient(ctx context.Context, cfg SkipperClientConfig) (*SkipperClient, error) {
	if cfg.SpokeURL == "" {
		return nil, fmt.Errorf("mcp: SkipperClient requires SpokeURL")
	}

	transport := &sdkmcp.StreamableClientTransport{
		Endpoint: cfg.SpokeURL,
		HTTPClient: &http.Client{
			Transport: &serviceTokenTransport{
				base:  http.DefaultTransport,
				token: cfg.ServiceToken,
			},
		},
	}

	client := sdkmcp.NewClient(&sdkmcp.Implementation{
		Name:    "bridge",
		Version: "1.0.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect to Skipper spoke: %w", err)
	}

	if cfg.Logger != nil {
		cfg.Logger.WithField("url", cfg.SpokeURL).Info("Connected to Skipper spoke MCP")
	}

	return &SkipperClient{session: session, logger: cfg.Logger}, nil
}

// CallTool invokes a tool on Skipper's spoke MCP server.
func (sc *SkipperClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	var args map[string]any
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return "", fmt.Errorf("mcp: unmarshal skipper args for %s: %w", name, err)
		}
	}

	result, err := sc.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return "", fmt.Errorf("mcp: skipper call %s: %w", name, err)
	}

	if result.IsError {
		text := extractSkipperText(result)
		if text != "" {
			return "", fmt.Errorf("mcp: skipper tool %s error: %s", name, text)
		}
		return "", fmt.Errorf("mcp: skipper tool %s returned error", name)
	}

	return extractSkipperText(result), nil
}

// Close shuts down the MCP session to Skipper.
func (sc *SkipperClient) Close() error {
	if sc.session != nil {
		return sc.session.Close()
	}
	return nil
}

func extractSkipperText(result *sdkmcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// LazySkipperClient defers the MCP connection to Skipper's spoke until the
// first CallTool invocation. This avoids startup failures when bridge boots
// before skipper and retries on each call until the spoke is reachable.
type LazySkipperClient struct {
	mu      sync.Mutex
	session *SkipperClient
	cfg     SkipperClientConfig
	logger  logging.Logger
}

const (
	skipperConnectTimeout      = 5 * time.Second
	skipperAvailabilityTimeout = 1 * time.Second
)

// NewLazySkipperClient creates a lazy-connecting proxy that satisfies
// SkipperCaller. The actual MCP session is established on first use.
func NewLazySkipperClient(cfg SkipperClientConfig, logger logging.Logger) *LazySkipperClient {
	return &LazySkipperClient{cfg: cfg, logger: logger}
}

// CallTool connects to Skipper on first call and caches the session.
// If a call fails, the session is invalidated so the next call retries.
func (l *LazySkipperClient) CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error) {
	s, err := l.getSession(ctx)
	if err != nil {
		return "", err
	}
	result, err := s.CallTool(ctx, name, arguments)
	if err != nil {
		l.invalidateSession(s)
		return "", err
	}
	return result, nil
}

func (l *LazySkipperClient) getSession(ctx context.Context) (*SkipperClient, error) {
	return l.getSessionWithTimeout(ctx, skipperConnectTimeout)
}

func (l *LazySkipperClient) getSessionWithTimeout(ctx context.Context, timeout time.Duration) (*SkipperClient, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != nil {
		return l.session, nil
	}
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sc, err := NewSkipperClient(connectCtx, l.cfg)
	if err != nil {
		return nil, fmt.Errorf("skipper spoke unavailable: %w", err)
	}
	l.session = sc
	if l.logger != nil {
		l.logger.Info("Connected to Skipper spoke MCP")
	}
	return sc, nil
}

// ToolsAvailable checks whether the Skipper spoke is reachable, using a short timeout.
func (l *LazySkipperClient) ToolsAvailable(ctx context.Context) bool {
	if l == nil {
		return false
	}
	_, err := l.getSessionWithTimeout(ctx, skipperAvailabilityTimeout)
	return err == nil
}

// invalidateSession clears the cached session if it matches the one that
// failed, so the next CallTool retries the connection.
func (l *LazySkipperClient) invalidateSession(failed *SkipperClient) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session == failed {
		_ = l.session.Close()
		l.session = nil
		if l.logger != nil {
			l.logger.Warn("Skipper spoke session invalidated; will reconnect on next call")
		}
	}
}

// Close shuts down the cached session, if any.
func (l *LazySkipperClient) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.session != nil {
		err := l.session.Close()
		l.session = nil
		return err
	}
	return nil
}

// serviceTokenTransport injects a service token for spoke-to-spoke auth.
type serviceTokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *serviceTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(req)
}
