package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// testSpokeServer creates a minimal MCP server that echoes the tool name and
// arguments back as JSON text. Useful for verifying end-to-end connectivity.
func testSpokeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-spoke", Version: "1.0.0"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{
			Name:        "search_knowledge",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"string"},"query":{"type":"string"}},"required":["tenant_id","query"]}`),
		},
		func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			data, _ := json.Marshal(req.Params.Arguments)
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: string(data)}},
			}, nil
		},
	)
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func TestSkipperClient_CallTool(t *testing.T) {
	ts := testSpokeServer(t)
	sc, err := NewSkipperClient(context.Background(), SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	})
	if err != nil {
		t.Fatalf("NewSkipperClient: %v", err)
	}
	defer func() { _ = sc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "test"})
	result, err := sc.CallTool(context.Background(), "search_knowledge", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("result not JSON: %v", err)
	}
	if parsed["query"] != "test" {
		t.Fatalf("expected query=test, got %v", parsed["query"])
	}
}

func TestSkipperClient_EmptySpokeURL(t *testing.T) {
	_, err := NewSkipperClient(context.Background(), SkipperClientConfig{})
	if err == nil {
		t.Fatal("expected error for empty SpokeURL")
	}
}

func TestLazySkipperClient_ConnectsOnFirstCall(t *testing.T) {
	ts := testSpokeServer(t)
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "lazy"})
	result, err := lc.CallTool(context.Background(), "search_knowledge", args)
	if err != nil {
		t.Fatalf("first CallTool: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
}

func TestLazySkipperClient_ReusesSession(t *testing.T) {
	var connects atomic.Int32
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-spoke", Version: "1.0.0"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{
			Name:        "search_knowledge",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"tenant_id":{"type":"string"},"query":{"type":"string"}},"required":["tenant_id","query"]}`),
		},
		func(_ context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"ok":true}`}},
			}, nil
		},
	)
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *sdkmcp.Server {
			connects.Add(1)
			return srv
		},
		&sdkmcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "q"})
	for i := 0; i < 3; i++ {
		if _, err := lc.CallTool(context.Background(), "search_knowledge", args); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	// Stateless mode processes each request independently, but the MCP client
	// session is established once via the initialize handshake. The factory
	// function fires per-request, but NewSkipperClient (which calls Connect)
	// should only happen once thanks to the lazy cache.
	lc.mu.Lock()
	hasSession := lc.session != nil
	lc.mu.Unlock()
	if !hasSession {
		t.Fatal("expected cached session after multiple calls")
	}
}

func TestLazySkipperClient_InvalidatesOnError(t *testing.T) {
	// Start a spoke, get a connection, then shut it down to simulate restart.
	ts := testSpokeServer(t)
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "q"})
	if _, err := lc.CallTool(context.Background(), "search_knowledge", args); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Session is cached.
	lc.mu.Lock()
	hasBefore := lc.session != nil
	lc.mu.Unlock()
	if !hasBefore {
		t.Fatal("expected session after successful call")
	}

	// Shut down the spoke to simulate restart.
	ts.Close()

	// Next call should fail and invalidate the session.
	_, err := lc.CallTool(context.Background(), "search_knowledge", args)
	if err == nil {
		t.Fatal("expected error after spoke shutdown")
	}

	lc.mu.Lock()
	hasAfter := lc.session != nil
	lc.mu.Unlock()
	if hasAfter {
		t.Fatal("expected session to be invalidated after error")
	}
}

func TestLazySkipperClient_ReconnectsAfterInvalidation(t *testing.T) {
	ts1 := testSpokeServer(t)
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     ts1.URL,
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "q"})
	if _, err := lc.CallTool(context.Background(), "search_knowledge", args); err != nil {
		t.Fatalf("call to first spoke: %v", err)
	}

	// Shut down and start a new spoke on the same URL.
	ts1.Close()
	_, _ = lc.CallTool(context.Background(), "search_knowledge", args) // Fails + invalidates

	// Start new spoke.
	ts2 := testSpokeServer(t)
	defer ts2.Close()

	// Point to the new spoke.
	lc.mu.Lock()
	lc.cfg.SpokeURL = ts2.URL
	lc.mu.Unlock()

	// Should reconnect.
	result, err := lc.CallTool(context.Background(), "search_knowledge", args)
	if err != nil {
		t.Fatalf("reconnect call: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result after reconnect")
	}
}

func TestLazySkipperClient_CloseIdempotent(t *testing.T) {
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     "http://localhost:1",
		ServiceToken: "test-token",
	}, nil)
	// Close without ever connecting.
	if err := lc.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := lc.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestLazySkipperClient_ConcurrentCalls(t *testing.T) {
	ts := testSpokeServer(t)
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "concurrent"})
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := lc.CallTool(context.Background(), "search_knowledge", args); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call error: %v", err)
	}
}

func TestLazySkipperClient_UnavailableSpoke(t *testing.T) {
	lc := NewLazySkipperClient(SkipperClientConfig{
		SpokeURL:     "http://localhost:1",
		ServiceToken: "test-token",
	}, nil)
	defer func() { _ = lc.Close() }()

	args, _ := json.Marshal(map[string]any{"tenant_id": "t1", "query": "q"})
	_, err := lc.CallTool(context.Background(), "search_knowledge", args)
	if err == nil {
		t.Fatal("expected error for unavailable spoke")
	}
}

func TestSkipperClient_CallTool_InvalidJSONArguments(t *testing.T) {
	ts := testSpokeServer(t)
	sc, err := NewSkipperClient(context.Background(), SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	})
	if err != nil {
		t.Fatalf("NewSkipperClient: %v", err)
	}
	defer func() { _ = sc.Close() }()

	_, err = sc.CallTool(context.Background(), "search_knowledge", json.RawMessage(`{"tenant_id":`))
	if err == nil {
		t.Fatal("expected error for malformed JSON arguments")
	}
	if !strings.Contains(err.Error(), "mcp: unmarshal skipper args for search_knowledge") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSkipperClient_CallTool_NonObjectArguments(t *testing.T) {
	ts := testSpokeServer(t)
	sc, err := NewSkipperClient(context.Background(), SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	})
	if err != nil {
		t.Fatalf("NewSkipperClient: %v", err)
	}
	defer func() { _ = sc.Close() }()

	_, err = sc.CallTool(context.Background(), "search_knowledge", json.RawMessage(`"not-an-object"`))
	if err == nil {
		t.Fatal("expected error for non-object arguments")
	}
	if !strings.Contains(err.Error(), "mcp: unmarshal skipper args for search_knowledge") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSkipperClient_CallTool_ToolErrorIncludesText(t *testing.T) {
	srv := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-spoke", Version: "1.0.0"}, nil)
	srv.AddTool(
		&sdkmcp.Tool{
			Name:        "search_knowledge",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		func(_ context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			return &sdkmcp.CallToolResult{
				IsError: true,
				Content: []sdkmcp.Content{
					&sdkmcp.TextContent{Text: "skipper downstream rejected"},
				},
			}, nil
		},
	)
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *sdkmcp.Server { return srv },
		&sdkmcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	sc, err := NewSkipperClient(context.Background(), SkipperClientConfig{
		SpokeURL:     ts.URL,
		ServiceToken: "test-token",
	})
	if err != nil {
		t.Fatalf("NewSkipperClient: %v", err)
	}
	defer func() { _ = sc.Close() }()

	_, err = sc.CallTool(context.Background(), "search_knowledge", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected tool error")
	}
	if !strings.Contains(err.Error(), "mcp: skipper tool search_knowledge error: skipper downstream rejected") {
		t.Fatalf("unexpected error: %v", err)
	}
}
