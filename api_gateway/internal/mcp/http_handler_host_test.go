package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Staging: nginx proxies the public MCP endpoint to Bridge on 127.0.0.1 with
// the public Host. The SDK's localhost guard answered every such request with
// 403 "invalid Host header".
func TestHTTPHandlerAcceptsProxiedHostOnLoopback(t *testing.T) {
	s := &Server{
		mcpServer: mcpsdk.NewServer(&mcpsdk.Implementation{Name: "test", Version: "0"}, nil),
		logger:    logging.NewLogger(),
	}
	srv := httptest.NewServer(s.HTTPHandler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "bridge.staging.frameworks.network"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusForbidden || strings.Contains(string(got), "invalid Host header") {
		t.Fatalf("proxied request refused: %d %s", resp.StatusCode, got)
	}
}
