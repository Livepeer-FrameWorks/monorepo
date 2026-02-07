package mcpspoke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/pkg/search"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeKnowledgeStore struct {
	chunks []knowledge.Chunk
}

func (s *fakeKnowledgeStore) Search(_ context.Context, tenantID string, _ []float32, limit int) ([]knowledge.Chunk, error) {
	return s.search(tenantID, limit)
}

func (s *fakeKnowledgeStore) HybridSearch(_ context.Context, tenantID string, _ []float32, _ string, limit int) ([]knowledge.Chunk, error) {
	return s.search(tenantID, limit)
}

func (s *fakeKnowledgeStore) search(tenantID string, limit int) ([]knowledge.Chunk, error) {
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

type fakeEmbedder struct{}

func (f fakeEmbedder) EmbedQuery(_ context.Context, query string) ([]float32, error) {
	length := float32(len(query))
	return []float32{length, length / 2, 1}, nil
}

type fakeSearchProvider struct {
	results []search.Result
}

func (p *fakeSearchProvider) Search(_ context.Context, _ string, _ search.SearchOptions) ([]search.Result, error) {
	return p.results, nil
}

func spokeTestServer(t *testing.T, cfg Config) *httptest.Server {
	t.Helper()
	srv := NewServer(cfg)
	handler := mcp.NewStreamableHTTPHandler(
		func(_ *http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func spokeClient(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestSpoke_ListTools(t *testing.T) {
	ts := spokeTestServer(t, Config{})
	session := spokeClient(t, ts.URL)

	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make(map[string]bool)
	for _, tool := range result.Tools {
		names[tool.Name] = true
	}
	if !names["search_knowledge"] {
		t.Fatal("expected search_knowledge tool")
	}
	if !names["search_web"] {
		t.Fatal("expected search_web tool")
	}
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}
}

func TestSpoke_SearchKnowledge(t *testing.T) {
	store := &fakeKnowledgeStore{
		chunks: []knowledge.Chunk{
			{
				TenantID:    "tenant-a",
				SourceURL:   "https://example.com/doc",
				SourceTitle: "Test Doc",
				Text:        "How to configure stream latency settings",
			},
		},
	}

	ts := spokeTestServer(t, Config{
		Knowledge: store,
		Embedder:  fakeEmbedder{},
	})
	session := spokeClient(t, ts.URL)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_knowledge",
		Arguments: map[string]any{
			"tenant_id": "tenant-a",
			"query":     "latency",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result.Content)
	}

	text := extractText(result)
	var resp searchKnowledgeResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Test Doc" {
		t.Fatalf("expected title 'Test Doc', got %q", resp.Results[0].Title)
	}
}

func TestSpoke_SearchKnowledge_MissingTenantID(t *testing.T) {
	ts := spokeTestServer(t, Config{
		Knowledge: &fakeKnowledgeStore{},
		Embedder:  fakeEmbedder{},
	})
	session := spokeClient(t, ts.URL)

	// The SDK validates required fields before the handler runs,
	// so a missing tenant_id returns a protocol-level error.
	_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_knowledge",
		Arguments: map[string]any{
			"query": "test",
		},
	})
	if err == nil {
		t.Fatal("expected error for missing required tenant_id")
	}
}

func TestSpoke_SearchWeb(t *testing.T) {
	ts := spokeTestServer(t, Config{
		SearchProvider: &fakeSearchProvider{
			results: []search.Result{
				{Title: "Docs", URL: "https://docs.example.com", Content: "Streaming guide", Score: 0.9},
			},
		},
	})
	session := spokeClient(t, ts.URL)

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search_web",
		Arguments: map[string]any{
			"query": "streaming guide",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result.Content)
	}

	text := extractText(result)
	var resp searchWebResponse
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if resp.Results[0].Title != "Docs" {
		t.Fatalf("expected title 'Docs', got %q", resp.Results[0].Title)
	}
}

func extractText(result *mcp.CallToolResult) string {
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
