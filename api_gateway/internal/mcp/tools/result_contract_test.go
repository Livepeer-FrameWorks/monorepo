package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolSuccessUsesSDKStructuredJSON(t *testing.T) {
	type output struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "result-contract", Version: "test"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "result_contract", Description: "Exercise the shared result helper."},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, output, error) {
			result, raw, err := toolSuccess(output{ID: "example", Enabled: true})
			return result, raw.(output), err
		})

	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "result_contract"})
	if err != nil {
		t.Fatalf("call tool: %v", err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(result.Content))
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content type = %T, want *mcp.TextContent", result.Content[0])
	}
	var decoded output
	if err := json.Unmarshal([]byte(text.Text), &decoded); err != nil {
		t.Fatalf("text fallback is not JSON: %v", err)
	}
	if decoded.ID != "example" || !decoded.Enabled {
		t.Fatalf("decoded output = %#v", decoded)
	}
	if result.StructuredContent == nil {
		t.Fatal("structuredContent is missing")
	}
}
