package chat

import (
	"context"
	"strings"
	"testing"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/pkg/skipper"
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
