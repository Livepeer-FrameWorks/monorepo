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

type fakeGateway struct {
	tools []llm.Tool
}

func (g *fakeGateway) AvailableTools() []llm.Tool { return g.tools }
func (g *fakeGateway) HasTool(name string) bool {
	for _, tool := range g.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
func (g *fakeGateway) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	return "", nil
}

func TestDocsModeFiltersToolsBeforeLLM(t *testing.T) {
	gateway := &fakeGateway{
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
