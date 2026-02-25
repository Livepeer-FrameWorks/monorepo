package chat

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
	"frameworks/pkg/search"
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

func (s *inMemoryKnowledgeStore) Search(_ context.Context, tenantID string, _ []float32, limit int) ([]knowledge.Chunk, error) {
	return s.filter(tenantID, limit), nil
}

func (s *inMemoryKnowledgeStore) HybridSearch(_ context.Context, tenantID string, _ []float32, _ string, limit int) ([]knowledge.Chunk, error) {
	return s.filter(tenantID, limit), nil
}

func (s *inMemoryKnowledgeStore) filter(tenantID string, limit int) []knowledge.Chunk {
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
	return filtered
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
	if len(result.Blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Confidence != ConfidenceVerified {
		t.Fatalf("expected block confidence verified, got %s", result.Blocks[0].Confidence)
	}
}

func TestOrchestratorMultiBlockResponse(t *testing.T) {
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{
					Content: "[confidence:verified]\nStream setup is documented.\n[sources]\n- Setup Guide \u2014 https://example.com/setup\n[/sources]\n\n[confidence:best_guess]\nRecommended bitrate is 4500 kbps.\n[sources]\n[/sources]\n",
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
	if len(result.Blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(result.Blocks))
	}
	if result.Blocks[0].Confidence != ConfidenceVerified {
		t.Fatalf("expected first block verified, got %s", result.Blocks[0].Confidence)
	}
	if result.Blocks[1].Confidence != ConfidenceBestGuess {
		t.Fatalf("expected second block best_guess, got %s", result.Blocks[1].Confidence)
	}
	if len(result.Blocks[0].Sources) != 1 {
		t.Fatalf("expected 1 source in first block, got %d", len(result.Blocks[0].Sources))
	}
	if result.Blocks[0].Sources[0].URL != "https://example.com/setup" {
		t.Fatalf("unexpected first block source URL: %s", result.Blocks[0].Sources[0].URL)
	}
	// Summarized confidence should be highest rank (verified > best_guess).
	if result.Confidence != ConfidenceVerified {
		t.Fatalf("expected summarized confidence verified, got %s", result.Confidence)
	}
}

func TestBuildMetaMultiBlock(t *testing.T) {
	result := OrchestratorResult{
		Content:    "Block 1\n\nBlock 2",
		Confidence: ConfidenceVerified,
		Sources: []Source{
			{Title: "KB Doc", URL: "https://example.com/kb", Type: SourceTypeKnowledgeBase},
		},
		Blocks: []ConfidenceBlock{
			{
				Content:    "Block 1",
				Confidence: ConfidenceVerified,
				Sources:    []Source{{Title: "KB Doc", URL: "https://example.com/kb", Type: SourceTypeKnowledgeBase}},
			},
			{
				Content:    "Block 2",
				Confidence: ConfidenceBestGuess,
			},
		},
	}

	meta := buildMeta(result)
	if len(meta.Blocks) != 2 {
		t.Fatalf("expected 2 meta blocks, got %d", len(meta.Blocks))
	}
	if meta.Blocks[0].Confidence != "verified" {
		t.Fatalf("expected first meta block confidence verified, got %s", meta.Blocks[0].Confidence)
	}
	if meta.Blocks[1].Confidence != "best_guess" {
		t.Fatalf("expected second meta block confidence best_guess, got %s", meta.Blocks[1].Confidence)
	}
	if len(meta.Blocks[0].Sources) != 1 {
		t.Fatalf("expected 1 source in first meta block, got %d", len(meta.Blocks[0].Sources))
	}
}

func TestBuildMetaSingleBlockOmitsBlocks(t *testing.T) {
	result := OrchestratorResult{
		Content:    "Single answer",
		Confidence: ConfidenceVerified,
		Blocks: []ConfidenceBlock{
			{Content: "Single answer", Confidence: ConfidenceVerified},
		},
	}

	meta := buildMeta(result)
	if len(meta.Blocks) != 0 {
		t.Fatalf("expected no meta blocks for single-block response, got %d", len(meta.Blocks))
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

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
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
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com/kb", "Skipper KB", "To reset stream latency you should first check the ingest protocol settings then verify the encoder keyframe interval and finally restart the stream session to apply updated configuration values")
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

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{{Role: "user", Content: "search knowledge"}}, nil)
	if err != nil {
		t.Fatalf("run orchestrator: %v", err)
	}
	if len(result.Sources) == 0 || result.Sources[0].Type != SourceTypeKnowledgeBase {
		t.Fatalf("expected knowledge base sources, got %+v", result.Sources)
	}
}

// recordingRerankClient records calls and returns results in reverse order
// so tests can detect that reranking happened.
type recordingRerankClient struct {
	called bool
	query  string
	docs   []string
}

func (c *recordingRerankClient) Rerank(_ context.Context, query string, documents []string) ([]llm.RerankResult, error) {
	c.called = true
	c.query = query
	c.docs = documents
	results := make([]llm.RerankResult, len(documents))
	for i := range documents {
		results[i] = llm.RerankResult{
			Index:          len(documents) - 1 - i,
			RelevanceScore: float64(len(documents)-i) / float64(len(documents)),
		}
	}
	return results, nil
}

// recordingKnowledgeStore wraps inMemoryKnowledgeStore and records queries
// passed to HybridSearch so tests can verify query rewriting took effect.
type recordingKnowledgeStore struct {
	*inMemoryKnowledgeStore
	hybridQueries []string
}

func (s *recordingKnowledgeStore) Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error) {
	return s.inMemoryKnowledgeStore.Search(ctx, tenantID, embedding, limit)
}

func (s *recordingKnowledgeStore) HybridSearch(ctx context.Context, tenantID string, embedding []float32, query string, limit int) ([]knowledge.Chunk, error) {
	s.hybridQueries = append(s.hybridQueries, query)
	return s.inMemoryKnowledgeStore.HybridSearch(ctx, tenantID, embedding, query, limit)
}

// recordingEmbedder records queries passed to EmbedQuery so tests can verify
// HyDE embedding vs regular embedding.
type recordingEmbedder struct {
	queries []string
}

func (e *recordingEmbedder) EmbedQuery(_ context.Context, query string) ([]float32, error) {
	e.queries = append(e.queries, query)
	length := float32(len(query))
	return []float32{length, length / 2, 1}, nil
}

// recordingSearchProvider records queries passed to Search.
type recordingSearchProvider struct {
	queries []string
}

func (p *recordingSearchProvider) Search(_ context.Context, query string, _ search.SearchOptions) ([]search.Result, error) {
	p.queries = append(p.queries, query)
	return []search.Result{
		{Title: "Test Result", URL: "https://example.com/result", Content: "test content", Score: 0.9},
	}, nil
}

func TestOrchestratorSearchKnowledgeWithCrossEncoderReranker(t *testing.T) {
	embedder, err := knowledge.NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com/a", "Doc A", "Stream latency reduction involves checking the ingest protocol settings and verifying that the encoder keyframe interval is set correctly for low-latency broadcasting workflows with proper buffer management")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	chunks2, err := embedder.EmbedDocument(context.Background(), "https://example.com/b", "Doc B", "Rebuffering diagnostics require analyzing packet loss patterns across the delivery network and checking viewer buffer health metrics to identify bottlenecks in the content distribution pipeline")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	allChunks := append(chunks, chunks2...)
	for i := range allChunks {
		allChunks[i].TenantID = "tenant-a"
	}

	store := &inMemoryKnowledgeStore{chunks: allChunks}
	rerankClient := &recordingRerankClient{}
	reranker := knowledge.NewReranker(rerankClient, "test", "test-model")
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_knowledge",
					Arguments: `{"query":"stream latency reset","limit":3}`,
				}}},
			},
			{{Content: "[confidence:verified]\nReranked answer.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Knowledge:   store,
		Embedder:    embedder,
		Reranker:    reranker,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "search knowledge"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rerankClient.called {
		t.Fatal("expected cross-encoder reranker to be called")
	}
	if rerankClient.query != "stream latency reset" {
		t.Fatalf("expected reranker query %q, got %q", "stream latency reset", rerankClient.query)
	}
	if len(result.Sources) == 0 {
		t.Fatal("expected knowledge sources in result")
	}
}

func TestOrchestratorPreRetrieveWithCrossEncoderReranker(t *testing.T) {
	embedder, err := knowledge.NewEmbedder(fakeEmbeddingClient{})
	if err != nil {
		t.Fatalf("embedder: %v", err)
	}
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com/a", "Doc A", "Latency reduction techniques for live streaming include adjusting the keyframe interval to one or two seconds and configuring the encoder buffer management settings for optimal low-latency delivery performance")
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	for i := range chunks {
		chunks[i].TenantID = "tenant-a"
	}

	store := &inMemoryKnowledgeStore{chunks: chunks}
	rerankClient := &recordingRerankClient{}
	reranker := knowledge.NewReranker(rerankClient, "test", "test-model")
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			{{Content: "[confidence:verified]\nPre-retrieval worked.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Knowledge:   store,
		Embedder:    embedder,
		Reranker:    reranker,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	_, err = orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "how do I reduce stream latency"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rerankClient.called {
		t.Fatal("expected cross-encoder reranker to be called during pre-retrieval")
	}
	if rerankClient.query != "how do I reduce stream latency" {
		t.Fatalf("expected reranker query from user message, got %q", rerankClient.query)
	}
}

func TestOrchestratorSearchKnowledgeWithQueryRewriter(t *testing.T) {
	chunks := []knowledge.Chunk{
		{TenantID: "tenant-a", SourceURL: "https://example.com/kb", SourceTitle: "KB", Text: "Stream disconnection troubleshooting guide"},
	}
	store := &recordingKnowledgeStore{inMemoryKnowledgeStore: &inMemoryKnowledgeStore{chunks: chunks}}
	recEmbedder := &recordingEmbedder{}

	rewriterLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			// Pre-retrieval rewrite — not used because QueryRewriter is only for tool calls,
			// but the embedder is called during pre-retrieval so we need it here.
			{{Content: "stream disconnection troubleshooting"}},
		},
	}
	queryRewriter := NewQueryRewriter(rewriterLLM)

	mainLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_knowledge",
					Arguments: `{"query":"my stream keeps dying"}`,
				}}},
			},
			{{Content: "[confidence:sourced]\nHere is your answer.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider:   mainLLM,
		Knowledge:     store,
		Embedder:      recEmbedder,
		QueryRewriter: queryRewriter,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	_, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "my stream keeps dying"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The search_knowledge tool call should use the rewritten query for hybrid search.
	foundRewritten := false
	for _, q := range store.hybridQueries {
		if q == "stream disconnection troubleshooting" {
			foundRewritten = true
			break
		}
	}
	if !foundRewritten {
		t.Fatalf("expected rewritten query in hybrid search, got queries: %v", store.hybridQueries)
	}
}

func TestOrchestratorSearchKnowledgeWithHyDE(t *testing.T) {
	chunks := []knowledge.Chunk{
		{TenantID: "tenant-a", SourceURL: "https://example.com/kb", SourceTitle: "KB", Text: "SRT encryption configuration guide for MistServer"},
	}
	store := &inMemoryKnowledgeStore{chunks: chunks}
	recEmbedder := &recordingEmbedder{}

	hydeLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{{Content: "To configure SRT encryption in MistServer, set the passphrase parameter in the stream settings."}},
		},
	}
	hyde := NewHyDEGenerator(hydeLLM, recEmbedder)

	mainLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_knowledge",
					Arguments: `{"query":"how to encrypt SRT streams"}`,
				}}},
			},
			{{Content: "[confidence:verified]\nSRT encryption answer.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: mainLLM,
		Knowledge:   store,
		Embedder:    recEmbedder,
		HyDE:        hyde,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	_, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "how to encrypt SRT streams"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The HyDE generator should have embedded the hypothetical answer, not the
	// original query. The recording embedder will show both the pre-retrieval
	// embed (original query) and the HyDE embed (hypothetical answer).
	foundHypothetical := false
	for _, q := range recEmbedder.queries {
		if q == "To configure SRT encryption in MistServer, set the passphrase parameter in the stream settings." {
			foundHypothetical = true
			break
		}
	}
	if !foundHypothetical {
		t.Fatalf("expected HyDE hypothetical answer to be embedded, got queries: %v", recEmbedder.queries)
	}
}

func TestOrchestratorSearchWebWithQueryRewriter(t *testing.T) {
	recSearch := &recordingSearchProvider{}
	searchWeb := NewSearchWebTool(recSearch)

	rewriterLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{{Content: "OBS Studio stream dropping frames troubleshooting"}},
		},
	}
	queryRewriter := NewQueryRewriter(rewriterLLM)

	mainLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_web",
					Arguments: `{"query":"OBS keeps dropping frames help"}`,
				}}},
			},
			{{Content: "[confidence:sourced]\nWeb search answer.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider:   mainLLM,
		SearchWeb:     searchWeb,
		QueryRewriter: queryRewriter,
	})

	_, err := orchestrator.Run(context.Background(), []llm.Message{
		{Role: "user", Content: "OBS keeps dropping frames help"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(recSearch.queries) == 0 {
		t.Fatal("expected search provider to be called")
	}
	if recSearch.queries[0] != "OBS Studio stream dropping frames troubleshooting" {
		t.Fatalf("expected rewritten query to reach search provider, got %q", recSearch.queries[0])
	}
}

func TestOrchestratorSearchKnowledgeWithAllEnhancements(t *testing.T) {
	chunks := []knowledge.Chunk{
		{TenantID: "tenant-a", SourceURL: "https://example.com/a", SourceTitle: "Doc A", Text: "Stream ingest latency reduction techniques"},
		{TenantID: "tenant-a", SourceURL: "https://example.com/b", SourceTitle: "Doc B", Text: "Encoder keyframe interval configuration"},
	}
	store := &recordingKnowledgeStore{inMemoryKnowledgeStore: &inMemoryKnowledgeStore{chunks: chunks}}
	recEmbedder := &recordingEmbedder{}

	rerankClient := &recordingRerankClient{}
	reranker := knowledge.NewReranker(rerankClient, "test", "test-model")

	rewriterLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{{Content: "stream latency optimization encoder settings"}},
		},
	}
	queryRewriter := NewQueryRewriter(rewriterLLM)

	hydeLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{{Content: "To reduce stream latency, configure the keyframe interval to 1-2 seconds and use low-latency mode."}},
		},
	}
	hyde := NewHyDEGenerator(hydeLLM, recEmbedder)

	mainLLM := &fakeProvider{
		sequences: [][]llm.Chunk{
			{
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_knowledge",
					Arguments: `{"query":"my stream has too much delay"}`,
				}}},
			},
			{{Content: "[confidence:verified]\nCombined answer.\n[sources]\n[/sources]\n"}},
		},
	}

	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider:   mainLLM,
		Knowledge:     store,
		Embedder:      recEmbedder,
		Reranker:      reranker,
		QueryRewriter: queryRewriter,
		HyDE:          hyde,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "my stream has too much delay"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Query rewriter should have transformed the query.
	foundRewritten := false
	for _, q := range store.hybridQueries {
		if q == "stream latency optimization encoder settings" {
			foundRewritten = true
			break
		}
	}
	if !foundRewritten {
		t.Fatalf("expected rewritten query in hybrid search, got: %v", store.hybridQueries)
	}

	// HyDE should have embedded the hypothetical answer.
	foundHyDE := false
	for _, q := range recEmbedder.queries {
		if q == "To reduce stream latency, configure the keyframe interval to 1-2 seconds and use low-latency mode." {
			foundHyDE = true
			break
		}
	}
	if !foundHyDE {
		t.Fatalf("expected HyDE hypothetical in embedder queries, got: %v", recEmbedder.queries)
	}

	// Cross-encoder reranker should have been called.
	if !rerankClient.called {
		t.Fatal("expected cross-encoder reranker to be called")
	}

	if len(result.Sources) == 0 {
		t.Fatal("expected sources in result")
	}
}

// recordingProvider wraps fakeProvider and records the messages passed to each
// Complete call so tests can inspect what the orchestrator sent.
type recordingProvider struct {
	sequences [][]llm.Chunk
	calls     [][]llm.Message
	call      int
}

func (p *recordingProvider) Complete(_ context.Context, messages []llm.Message, _ []llm.Tool) (llm.Stream, error) {
	cp := make([]llm.Message, len(messages))
	copy(cp, messages)
	p.calls = append(p.calls, cp)
	if p.call >= len(p.sequences) {
		return &fakeStream{}, nil
	}
	stream := &fakeStream{chunks: p.sequences[p.call]}
	p.call++
	return stream, nil
}

func TestOrchestratorPreservesTextAcrossToolRounds(t *testing.T) {
	store := &inMemoryKnowledgeStore{
		chunks: []knowledge.Chunk{
			{TenantID: "tenant-a", SourceURL: "https://example.com", SourceTitle: "Doc", Text: "SRT setup guide", Similarity: 0.9},
		},
	}
	embedder := &fakeQueryEmbedder{}
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			// Round 0: confidence-tagged text + tool call
			{
				{Content: "[confidence:best_guess]\nSRT ingest uses listener mode on port 9000.\n[sources]\n[/sources]\n"},
				{ToolCalls: []llm.ToolCall{{
					ID:        "call-1",
					Name:      "search_knowledge",
					Arguments: `{"query":"SRT ingest"}`,
				}}},
			},
			// Round 1: more confidence-tagged text, no tool call
			{
				{Content: "[confidence:verified]\nHere is the SRT setup guide.\n[sources]\n- Doc \u2014 https://example.com\n[/sources]\n"},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		Knowledge:   store,
		Embedder:    embedder,
	})

	ctx := skipper.WithTenantID(context.Background(), "tenant-a")
	result, err := orchestrator.Run(ctx, []llm.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "How do I set up SRT ingest?"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Content, "listener mode on port 9000") {
		t.Fatalf("expected round-0 text preserved, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "Here is the SRT setup guide") {
		t.Fatalf("expected round-1 text preserved, got %q", result.Content)
	}
	if result.Confidence != ConfidenceVerified {
		t.Fatalf("expected verified confidence (highest across blocks), got %s", result.Confidence)
	}
}

func TestOrchestratorMaxRoundsPreservesContent(t *testing.T) {
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			// Round 0: text + tool call
			{
				{Content: "Based on my search, here is the answer.\n"},
				{ToolCalls: []llm.ToolCall{{
					ID: "call-1", Name: "search_web",
					Arguments: `{"query":"test"}`,
				}}},
			},
			// Round 1 (last with MaxRounds=2): only tool call, no text → hits max
			{
				{ToolCalls: []llm.ToolCall{{
					ID: "call-2", Name: "search_web",
					Arguments: `{"query":"test again"}`,
				}}},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		SearchWeb:   NewSearchWebTool(&recordingSearchProvider{}),
		MaxRounds:   2,
	})

	result, err := orchestrator.Run(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Content, "Based on my search, here is the answer") {
		t.Fatalf("expected round-0 text preserved after max rounds, got %q", result.Content)
	}
}

func TestOrchestratorMaxRoundsEmptyContentGetsFallback(t *testing.T) {
	provider := &fakeProvider{
		sequences: [][]llm.Chunk{
			// Round 0: only tool call, no text
			{
				{ToolCalls: []llm.ToolCall{{
					ID: "call-1", Name: "search_web",
					Arguments: `{"query":"test"}`,
				}}},
			},
			// Round 1 (last): only tool call again
			{
				{ToolCalls: []llm.ToolCall{{
					ID: "call-2", Name: "search_web",
					Arguments: `{"query":"test again"}`,
				}}},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		SearchWeb:   NewSearchWebTool(&recordingSearchProvider{}),
		MaxRounds:   2,
	})

	result, err := orchestrator.Run(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result.Content, "Reached maximum tool iterations") {
		t.Fatalf("expected fallback message, got %q", result.Content)
	}
	if result.Confidence != ConfidenceUnknown {
		t.Fatalf("expected unknown confidence, got %s", result.Confidence)
	}
}

func TestOrchestratorConvergenceNudge(t *testing.T) {
	provider := &recordingProvider{
		sequences: [][]llm.Chunk{
			// Round 0: tool call
			{
				{ToolCalls: []llm.ToolCall{{
					ID: "call-1", Name: "search_web",
					Arguments: `{"query":"test"}`,
				}}},
			},
			// Round 1 (maxRounds-2 = 1): tool call → nudge injected after this
			{
				{ToolCalls: []llm.ToolCall{{
					ID: "call-2", Name: "search_web",
					Arguments: `{"query":"test2"}`,
				}}},
			},
			// Round 2 (last): should see nudge in messages, produce final answer
			{
				{Content: "[confidence:sourced]\nFinal answer.\n[sources]\n[/sources]\n"},
			},
		},
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		LLMProvider: provider,
		SearchWeb:   NewSearchWebTool(&recordingSearchProvider{}),
		MaxRounds:   3,
	})

	result, err := orchestrator.Run(context.Background(), []llm.Message{
		{Role: "user", Content: "test"},
	}, nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Content != "Final answer." {
		t.Fatalf("expected final answer, got %q", result.Content)
	}

	// The third Complete call (index 2) should have the nudge message.
	if len(provider.calls) < 3 {
		t.Fatalf("expected 3 LLM calls, got %d", len(provider.calls))
	}
	lastCallMessages := provider.calls[2]
	foundNudge := false
	for _, msg := range lastCallMessages {
		if msg.Role == "user" && strings.Contains(msg.Content, "one remaining tool round") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Fatal("expected convergence nudge in last LLM call messages")
	}
}

func TestStripConfidenceTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		out  string
	}{
		{"start of line", "[confidence:verified]Some text", "Some text"},
		{"end of line", "Some text[confidence:best_guess]", "Some text"},
		{"middle of line", "Before [confidence:sourced] after", "Before  after"},
		{"standalone", "[confidence:unknown]", ""},
		{"multiple", "[confidence:verified]A[confidence:sourced]B", "AB"},
		{"no tag", "Just regular text", "Just regular text"},
		{"unclosed tag", "[confidence:verified text", "[confidence:verified text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripConfidenceTags(tc.in)
			if got != tc.out {
				t.Fatalf("stripConfidenceTags(%q) = %q, want %q", tc.in, got, tc.out)
			}
		})
	}
}

func TestConfidenceStreamFilterInlineTag(t *testing.T) {
	var tokens []string
	streamer := tokenFunc(func(tok string) error {
		tokens = append(tokens, tok)
		return nil
	})
	filter := newConfidenceStreamFilter(streamer)
	// LLM produces a confidence tag inline with content.
	input := "Here is the answer [confidence:verified] with details.\n"
	if err := filter.Write(input); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := filter.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	joined := strings.Join(tokens, "")
	if strings.Contains(joined, "[confidence:") {
		t.Fatalf("confidence tag leaked into streamed output: %q", joined)
	}
	if !strings.Contains(joined, "Here is the answer") {
		t.Fatalf("expected content preserved, got %q", joined)
	}
	if !strings.Contains(joined, "with details.") {
		t.Fatalf("expected content after tag preserved, got %q", joined)
	}
}

// tokenFunc adapts a function into a TokenStreamer for testing.
type tokenFunc func(string) error

func (f tokenFunc) SendToken(tok string) error { return f(tok) }

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
