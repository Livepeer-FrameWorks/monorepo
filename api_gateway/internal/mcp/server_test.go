package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeSkipperAvailability struct {
	available bool
}

func (f fakeSkipperAvailability) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return "", nil
}

func (f fakeSkipperAvailability) ToolsAvailable(_ context.Context) bool {
	return f.available
}

func TestFilterSkipperToolsWhenUnavailable(t *testing.T) {
	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{
			{Name: "search_knowledge"},
			{Name: "search_web"},
			{Name: "get_stream"},
		},
	}
	filtered := filterSkipperTools(context.Background(), result, fakeSkipperAvailability{available: false})
	listResult, ok := filtered.(*mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", filtered)
	}
	if len(listResult.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(listResult.Tools))
	}
	if listResult.Tools[0].Name != "get_stream" {
		t.Fatalf("unexpected remaining tool: %s", listResult.Tools[0].Name)
	}
}

func TestFilterSkipperToolsWhenAvailable(t *testing.T) {
	result := &mcp.ListToolsResult{
		Tools: []*mcp.Tool{
			{Name: "search_knowledge"},
			{Name: "search_web"},
			{Name: "get_stream"},
		},
	}
	filtered := filterSkipperTools(context.Background(), result, fakeSkipperAvailability{available: true})
	listResult, ok := filtered.(*mcp.ListToolsResult)
	if !ok {
		t.Fatalf("expected ListToolsResult, got %T", filtered)
	}
	if len(listResult.Tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(listResult.Tools))
	}
}
