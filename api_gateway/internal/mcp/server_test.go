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

func TestGetMcpArgString(t *testing.T) {
	tests := []struct {
		name   string
		params mcp.Params
		keys   []string
		want   string
	}{
		{
			name: "returns first matching string key",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"streamId":"s1","stream_id":"s2"}`),
			},
			keys: []string{"stream_id", "streamId"},
			want: "s2",
		},
		{
			name: "returns empty on malformed json",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"stream_id":`),
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name: "returns empty on non-string value",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"stream_id":123}`),
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name: "returns empty on missing args",
			params: &mcp.CallToolParamsRaw{
				Name: "update_stream",
			},
			keys: []string{"stream_id"},
			want: "",
		},
		{
			name:   "returns empty for wrong params type",
			params: &mcp.ListToolsParams{},
			keys:   []string{"stream_id"},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := getMcpArgString(tc.params, tc.keys...)
			if got != tc.want {
				t.Fatalf("getMcpArgString() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractPlaybackContentID(t *testing.T) {
	tests := []struct {
		name   string
		params mcp.Params
		want   string
	}{
		{
			name: "extracts content_id",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":"abc123"}`),
			},
			want: "abc123",
		},
		{
			name: "extracts camelCase fallback",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"contentId":"abc456"}`),
			},
			want: "abc456",
		},
		{
			name: "returns empty for wrong tool",
			params: &mcp.CallToolParamsRaw{
				Name:      "update_stream",
				Arguments: json.RawMessage(`{"content_id":"abc123"}`),
			},
			want: "",
		},
		{
			name: "returns empty on malformed json",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":`),
			},
			want: "",
		},
		{
			name: "returns empty on non-string value",
			params: &mcp.CallToolParamsRaw{
				Name:      "resolve_playback_endpoint",
				Arguments: json.RawMessage(`{"content_id":123}`),
			},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPlaybackContentID(tc.params)
			if got != tc.want {
				t.Fatalf("extractPlaybackContentID() = %q, want %q", got, tc.want)
			}
		})
	}
}
