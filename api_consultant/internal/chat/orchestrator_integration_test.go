package chat

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
)

type fakeStream struct {
	chunks []llm.Chunk
	index  int
}

func (s *fakeStream) Recv() (llm.Chunk, error) {
	if s.index >= len(s.chunks) {
		return llm.Chunk{}, io.EOF
	}
	chunk := s.chunks[s.index]
	s.index++
	return chunk, nil
}

func (s *fakeStream) Close() error {
	return nil
}

type fakeProvider struct {
	sequences [][]llm.Chunk
	call      int
}

func (p *fakeProvider) Complete(ctx context.Context, messages []llm.Message, tools []llm.Tool) (llm.Stream, error) {
	_ = ctx
	_ = messages
	_ = tools
	if p.call >= len(p.sequences) {
		return &fakeStream{}, nil
	}
	stream := &fakeStream{chunks: p.sequences[p.call]}
	p.call++
	return stream, nil
}

type fakeGateway struct {
	tools     []llm.Tool
	toolIndex map[string]struct{}
	calls     []gatewayCall
}

type gatewayCall struct {
	Name      string
	Arguments json.RawMessage
}

func newFakeGateway(toolNames ...string) *fakeGateway {
	tools := make([]llm.Tool, 0, len(toolNames))
	idx := make(map[string]struct{}, len(toolNames))
	for _, name := range toolNames {
		tools = append(tools, llm.Tool{
			Name:        name,
			Description: "fake " + name,
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		})
		idx[name] = struct{}{}
	}
	return &fakeGateway{tools: tools, toolIndex: idx}
}

func (g *fakeGateway) AvailableTools() []llm.Tool { return g.tools }

func (g *fakeGateway) HasTool(name string) bool {
	_, ok := g.toolIndex[name]
	return ok
}

func (g *fakeGateway) CallTool(_ context.Context, name string, arguments json.RawMessage) (string, error) {
	g.calls = append(g.calls, gatewayCall{Name: name, Arguments: arguments})
	return `{"status":"ok","summary":"diagnostics complete"}`, nil
}

type fakeEmbeddingClient struct{}

func (f fakeEmbeddingClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	_ = ctx
	vectors := make([][]float32, 0, len(inputs))
	for _, input := range inputs {
		length := float32(len(input))
		vectors = append(vectors, []float32{length, length / 2, 1})
	}
	return vectors, nil
}

type inMemoryKnowledgeStore struct {
	chunks []knowledge.Chunk
}

func (s *inMemoryKnowledgeStore) Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error) {
	_ = ctx
	_ = embedding
	if limit <= 0 {
		limit = 5
	}
	var filtered []knowledge.Chunk
	for _, chunk := range s.chunks {
		if chunk.TenantID == tenantID {
			chunk.Similarity = 0.91
			filtered = append(filtered, chunk)
		}
	}
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

func TestOrchestratorGroundingConfidenceAndSources(t *testing.T) {
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{
					Content: "[confidence:verified]\nUse the Skipper runbook.\n[sources]\n- Skipper KB \u2014 https://example.com/docs\n[/sources]\n",
				},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
	})

	result, err := orchestrator.Run(context.Background(), []llm.Message{{Role: "user", Content: "help"}}, nil)
	if err != nil {
		t.Fatalf("run orchestrator: %v", err)
	}
	if result.Confidence != ConfidenceVerified {
		t.Fatalf("expected verified confidence, got %s", result.Confidence)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(result.Sources))
	}
	if result.Sources[0].URL != "https://example.com/docs" {
		t.Fatalf("unexpected source URL: %s", result.Sources[0].URL)
	}
}

func TestOrchestratorDiagnosticsUsesGatewayTool(t *testing.T) {
	gateway := newFakeGateway("diagnose_rebuffering")
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{
					ToolCalls: []llm.ToolCall{{
						ID:        "call-1",
						Name:      "diagnose_rebuffering",
						Arguments: `{"stream_id":"stream-123","time_range":"last_1h"}`,
					}},
				},
			},
			{
				{
					Content: "[confidence:best_guess]\nDiagnostics complete.\n[sources]\n[/sources]\n",
				},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Gateway:     gateway,
	})

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{{Role: "user", Content: "diagnose"}}, nil)
	if err != nil {
		t.Fatalf("run orchestrator: %v", err)
	}
	if len(gateway.calls) != 1 {
		t.Fatalf("expected 1 gateway call, got %d", len(gateway.calls))
	}
	if gateway.calls[0].Name != "diagnose_rebuffering" {
		t.Fatalf("expected gateway call to diagnose_rebuffering, got %s", gateway.calls[0].Name)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "diagnose_rebuffering" {
		t.Fatalf("expected diagnose_rebuffering tool call, got %+v", result.ToolCalls)
	}
	if len(result.Details) == 0 {
		t.Fatalf("expected tool details")
	}
}

func TestOrchestratorSearchKnowledgeWithEmbeddings(t *testing.T) {
	embedder, err := knowledge.NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com/kb", "Skipper KB", "Reset stream latency checklist")
	if err != nil {
		t.Fatalf("embed document: %v", err)
	}
	for i := range chunks {
		chunks[i].TenantID = "tenant-a"
	}

	store := &inMemoryKnowledgeStore{chunks: chunks}
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{
					ToolCalls: []llm.ToolCall{{
						ID:        "call-1",
						Name:      "search_knowledge",
						Arguments: `{"query":"reset stream latency","limit":3}`,
					}},
				},
			},
			{
				{
					Content: "[confidence:sourced]\nAnswer ready.\n[sources]\n[/sources]\n",
				},
			},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Knowledge:   store,
		Embedder:    embedder,
	})

	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{{Role: "user", Content: "search knowledge"}}, nil)
	if err != nil {
		t.Fatalf("run orchestrator: %v", err)
	}
	if len(result.Sources) == 0 || result.Sources[0].Type != SourceTypeKnowledgeBase {
		t.Fatalf("expected knowledge base sources, got %+v", result.Sources)
	}
}

func TestLLMProviderSwitching(t *testing.T) {
	cases := []struct {
		name     string
		provider string
	}{
		{name: "openai", provider: "openai"},
		{name: "anthropic", provider: "anthropic"},
		{name: "ollama", provider: "ollama"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			provider, err := llm.NewProvider(llm.Config{Provider: tc.provider})
			if err != nil {
				t.Fatalf("provider %s: %v", tc.provider, err)
			}
			if provider == nil {
				t.Fatalf("provider %s returned nil", tc.provider)
			}
		})
	}
}
