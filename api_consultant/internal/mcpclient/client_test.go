package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"frameworks/pkg/ctxkeys"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConvertInputSchema_Nil(t *testing.T) {
	result := convertInputSchema(nil)
	if result["type"] != "object" {
		t.Fatalf("expected type=object, got %v", result["type"])
	}
	if _, ok := result["properties"]; !ok {
		t.Fatal("expected properties key")
	}
}

func TestConvertInputSchema_Map(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"stream_id": map[string]any{"type": "string"},
		},
		"required": []string{"stream_id"},
	}
	result := convertInputSchema(input)
	if result["type"] != "object" {
		t.Fatalf("expected type=object, got %v", result["type"])
	}
	props, ok := result["properties"].(map[string]any)
	if !ok {
		t.Fatal("expected properties to be map[string]any")
	}
	if _, ok := props["stream_id"]; !ok {
		t.Fatal("expected stream_id property")
	}
}

func TestConvertInputSchema_Struct(t *testing.T) {
	type schema struct {
		Type string `json:"type"`
	}
	result := convertInputSchema(schema{Type: "object"})
	if result["type"] != "object" {
		t.Fatalf("expected type=object, got %v", result["type"])
	}
}

func TestExtractTextContent_Nil(t *testing.T) {
	if got := extractTextContent(nil); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestExtractTextContent_SingleText(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "hello"},
		},
	}
	if got := extractTextContent(result); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestExtractTextContent_MultipleTexts(t *testing.T) {
	result := &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "line1"},
			&mcp.TextContent{Text: "line2"},
		},
	}
	if got := extractTextContent(result); got != "line1\nline2" {
		t.Fatalf("expected 'line1\\nline2', got %q", got)
	}
}

func TestAuthTransport_UsesJWTFromContext(t *testing.T) {
	var capturedAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	transport := &authTransport{base: http.DefaultTransport}

	ctx := context.WithValue(context.Background(), ctxkeys.KeyJWTToken, "user-jwt-123")
	req, _ := http.NewRequestWithContext(ctx, "POST", backend.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if capturedAuth != "Bearer user-jwt-123" {
		t.Fatalf("expected Bearer user-jwt-123, got %q", capturedAuth)
	}
}

func TestAuthTransport_NoAuthWithoutJWT(t *testing.T) {
	var capturedAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	transport := &authTransport{base: http.DefaultTransport}

	req, _ := http.NewRequestWithContext(context.Background(), "POST", backend.URL, nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if capturedAuth != "" {
		t.Fatalf("expected no auth header, got %q", capturedAuth)
	}
}

func TestNewGatewayClient_EmptyURL(t *testing.T) {
	_, err := New(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func testMCPServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-gateway",
		Version: "1.0.0",
	}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "diagnose_rebuffering",
		Description: "Diagnose stream rebuffering issues",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"stream_id":{"type":"string"}},"required":["stream_id"]}`),
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			StreamID string `json:"stream_id"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: `{"status":"ok","stream_id":"` + args.StreamID + `"}`},
			},
		}, nil
	})

	server.AddTool(&mcp.Tool{
		Name:        "delete_stream",
		Description: "Delete a stream (destructive)",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "deleted"}},
		}, nil
	})

	handler := mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestGatewayClient_EndToEnd(t *testing.T) {
	ts := testMCPServer(t)

	gc, err := New(context.Background(), Config{
		GatewayURL: ts.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gc.Close() }()

	tools := gc.AvailableTools()
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}

	if !gc.HasTool("diagnose_rebuffering") {
		t.Fatal("expected diagnose_rebuffering to be available")
	}
	if gc.HasTool("nonexistent") {
		t.Fatal("expected nonexistent to not be available")
	}

	result, err := gc.CallTool(context.Background(), "diagnose_rebuffering", json.RawMessage(`{"stream_id":"abc"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if parsed["stream_id"] != "abc" {
		t.Fatalf("expected stream_id=abc, got %s", parsed["stream_id"])
	}
}

func TestGatewayClient_Allowlist(t *testing.T) {
	ts := testMCPServer(t)

	gc, err := New(context.Background(), Config{
		GatewayURL:    ts.URL,
		ToolAllowlist: []string{"diagnose_rebuffering"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gc.Close() }()

	tools := gc.AvailableTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool (allowlisted), got %d", len(tools))
	}
	if tools[0].Name != "diagnose_rebuffering" {
		t.Fatalf("expected diagnose_rebuffering, got %s", tools[0].Name)
	}
	if gc.HasTool("delete_stream") {
		t.Fatal("expected delete_stream to be filtered out")
	}
}

func TestGatewayClient_ReconnectRefreshesTools(t *testing.T) {
	var version atomic.Int32
	version.Store(1)

	handler := mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server {
			server := mcp.NewServer(&mcp.Implementation{
				Name:    "dynamic-gateway",
				Version: "1.0.0",
			}, nil)

			if version.Load() == 1 {
				server.AddTool(&mcp.Tool{
					Name:        "tool_a",
					Description: "Tool A",
					InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
				}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					version.Store(2)
					return nil, context.Canceled
				})
				return server
			}

			server.AddTool(&mcp.Tool{
				Name:        "tool_a",
				Description: "Tool A",
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
				}, nil
			})
			server.AddTool(&mcp.Tool{
				Name:        "tool_b",
				Description: "Tool B",
				InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
			}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "ok"}},
				}, nil
			})
			return server
		},
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	gc, err := New(context.Background(), Config{
		GatewayURL: ts.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = gc.Close() }()

	result, err := gc.CallTool(context.Background(), "tool_a", nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result != "ok" {
		t.Fatalf("expected ok response after reconnect, got %q", result)
	}
	if !gc.HasTool("tool_b") {
		t.Fatal("expected tool_b to be registered after reconnect")
	}
}
