package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
)

type fakeKnowledgeStore struct {
	results   map[string][]knowledge.Chunk
	errTenant string
	calls     []string
}

func (f *fakeKnowledgeStore) Search(ctx context.Context, tenantID string, _ []float32, _ int) ([]knowledge.Chunk, error) {
	return f.HybridSearch(ctx, tenantID, nil, "", 0)
}

func (f *fakeKnowledgeStore) HybridSearch(_ context.Context, tenantID string, _ []float32, _ string, _ int) ([]knowledge.Chunk, error) {
	f.calls = append(f.calls, tenantID)
	if tenantID == f.errTenant {
		return nil, context.DeadlineExceeded
	}
	return f.results[tenantID], nil
}

type fakeQueryEmbedder struct {
	calls int
}

func (f *fakeQueryEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	f.calls++
	return []float32{0.1, 0.2}, nil
}

func TestSearchKnowledgeFallsBackOnTenantFailure(t *testing.T) {
	store := &fakeKnowledgeStore{
		errTenant: "tenant-a",
		results: map[string][]knowledge.Chunk{
			"global": {
				{
					SourceURL:   "https://docs.example.com",
					SourceTitle: "Global Docs",
					Text:        "Latency tuning guide",
					Similarity:  0.98,
				},
			},
		},
	}
	embedder := &fakeQueryEmbedder{}

	orchestrator := &Orchestrator{
		knowledge:      store,
		embedder:       embedder,
		searchLimit:    2,
		globalTenantID: "global",
	}

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	outcome, err := orchestrator.searchKnowledge(ctx, `{"query":"latency","tenant_scope":"all"}`)
	if err != nil {
		t.Fatalf("expected fallback success, got error: %v", err)
	}
	if embedder.calls != 1 {
		t.Fatalf("expected embedder called once, got %d", embedder.calls)
	}
	if len(outcome.Sources) != 1 || outcome.Sources[0].URL != "https://docs.example.com" {
		t.Fatalf("expected global source, got %+v", outcome.Sources)
	}
	if !strings.Contains(outcome.Content, "Knowledge base results") {
		t.Fatalf("expected knowledge context, got %q", outcome.Content)
	}
}

type countingGateway struct {
	tools []llm.Tool
	calls int
}

func (g *countingGateway) AvailableTools() []llm.Tool { return g.tools }
func (g *countingGateway) HasTool(name string) bool {
	for _, tool := range g.tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
func (g *countingGateway) CallTool(_ context.Context, _ string, _ json.RawMessage) (string, error) {
	g.calls++
	return "called", nil
}

func TestDocsModeBlocksMutatingToolExecution(t *testing.T) {
	gateway := &countingGateway{
		tools: []llm.Tool{
			{Name: "delete_stream"},
		},
	}
	orchestrator := &Orchestrator{gateway: gateway}

	ctx := skipper.WithMode(context.Background(), "docs")
	outcome, err := orchestrator.executeTool(ctx, llm.ToolCall{
		Name:      "delete_stream",
		Arguments: "{}",
	})
	if err != nil {
		t.Fatalf("executeTool: %v", err)
	}
	if gateway.calls != 0 {
		t.Fatalf("expected gateway to be skipped, got %d calls", gateway.calls)
	}
	if !strings.Contains(outcome.Content, "not available in documentation mode") {
		t.Fatalf("expected docs mode denial message, got %q", outcome.Content)
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
		{ID: "call-1", Name: "search_knowledge", Arguments: `{"query":"stream latency"}`},
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
		{ID: "call-2", Name: "get_stream", Arguments: `{"stream_id":"a","tenant_id":"t"}`},
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
