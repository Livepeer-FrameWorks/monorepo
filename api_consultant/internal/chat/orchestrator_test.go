package chat

import (
	"context"
	"encoding/json"
	"testing"

	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
)

type capturingLLM struct {
	tools []llm.Tool
}

func (c *capturingLLM) Complete(_ context.Context, _ []llm.Message, tools []llm.Tool) (llm.Stream, error) {
	c.tools = tools
	return &singleChunkStream{content: ""}, nil
}

type fakeGatewayUnit struct {
	tools []llm.Tool
}

func (g *fakeGatewayUnit) AvailableTools() []llm.Tool { return g.tools }
func (g *fakeGatewayUnit) HasTool(name string) bool {
	for _, tool := range g.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
func (g *fakeGatewayUnit) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return "", nil
}

func TestDocsModeFiltersToolsBeforeLLM(t *testing.T) {
	gateway := &fakeGatewayUnit{
		tools: []llm.Tool{
			{Name: "create_stream"},
			{Name: "get_stream"},
		},
	}
	provider := &capturingLLM{}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Gateway:     gateway,
	})

	ctx := skipper.WithMode(context.Background(), "docs")
	_, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "hello"},
	}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if hasTool(provider.tools, "create_stream") {
		t.Fatalf("expected create_stream to be filtered in docs mode")
	}
	if !hasTool(provider.tools, "get_stream") {
		t.Fatalf("expected get_stream to remain available in docs mode")
	}
}

func hasTool(tools []llm.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func TestMergeToolCalls_DeduplicatesByID(t *testing.T) {
	existing := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"stream `},
	}
	incoming := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `latency"}`},
	}

	result := mergeToolCalls(existing, incoming)
	if len(result) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result))
	}
	if result[0].Arguments != `{"query":"stream latency"}` {
		t.Fatalf("expected merged arguments, got %q", result[0].Arguments)
	}
}

func TestMergeToolCalls_PreservesOrderWithOutOfOrderChunks(t *testing.T) {
	existing := []llm.ToolCall{
		{ID: "call-2", Name: "get_stream", Arguments: `{"stream_id":"a"`},
	}
	incoming := []llm.ToolCall{
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"srt"}`},
		{ID: "call-2", Name: "get_stream", Arguments: `,"tenant_id":"t"}`},
	}

	result := mergeToolCalls(existing, incoming)
	if len(result) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result))
	}
	if result[0].ID != "call-2" || result[1].ID != "call-1" {
		t.Fatalf("expected order preserved by first-seen ID, got %q then %q", result[0].ID, result[1].ID)
	}
	if result[0].Arguments != `{"stream_id":"a","tenant_id":"t"}` {
		t.Fatalf("expected merged arguments for call-2, got %q", result[0].Arguments)
	}
}
