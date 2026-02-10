package knowledge

import (
	"context"
	"errors"
	"testing"

	"frameworks/pkg/llm"
)

func TestRRFRerankPreservesOrder(t *testing.T) {
	chunks := []Chunk{
		{Text: "stream encoder settings", Similarity: 0.5},
		{Text: "configure stream latency", Similarity: 0.9},
	}
	result := rrfRerank("stream latency", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].Text != "configure stream latency" {
		t.Fatalf("expected 'configure stream latency' first, got %q", result[0].Text)
	}
}

func TestRRFRerankEmptyChunks(t *testing.T) {
	result := rrfRerank("query", nil)
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

func TestRRFRerankEmptyQuery(t *testing.T) {
	chunks := []Chunk{{Text: "abc", Similarity: 0.5}}
	result := rrfRerank("", chunks)
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
}

func TestRRFRerankDivergentSignals(t *testing.T) {
	// Chunk A: high vector similarity, low keyword overlap
	// Chunk B: low vector similarity, high keyword overlap
	// RRF should produce closer scores than weighted average.
	chunks := []Chunk{
		{Text: "unrelated content about video encoding", Similarity: 0.95},
		{Text: "stream latency configuration guide", Similarity: 0.3},
	}
	result := rrfRerank("stream latency", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	// Both chunks get rank 1 in one dimension and rank 2 in the other,
	// so RRF scores should be equal — order preserved by SliceStable.
	scoreDiff := result[0].Similarity - result[1].Similarity
	if scoreDiff < 0 {
		scoreDiff = -scoreDiff
	}
	if scoreDiff > 0.001 {
		t.Fatalf("expected near-equal RRF scores for divergent signals, got diff %f", scoreDiff)
	}
}

type mockRerankClient struct {
	results []llm.RerankResult
	err     error
}

func (m *mockRerankClient) Rerank(_ context.Context, _ string, _ []string) ([]llm.RerankResult, error) {
	return m.results, m.err
}

func TestRerankerCrossEncoder(t *testing.T) {
	client := &mockRerankClient{
		results: []llm.RerankResult{
			{Index: 1, RelevanceScore: 0.95},
			{Index: 0, RelevanceScore: 0.30},
		},
	}
	r := NewReranker(client)
	chunks := []Chunk{
		{Text: "low relevance doc", Similarity: 0.9},
		{Text: "high relevance doc", Similarity: 0.3},
	}
	result := r.Rerank(context.Background(), "query", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	// Cross-encoder says index 1 is most relevant
	if result[0].Text != "high relevance doc" {
		t.Fatalf("expected cross-encoder ordering, got %q first", result[0].Text)
	}
	if result[0].Similarity != 0.95 {
		t.Fatalf("expected similarity 0.95, got %f", result[0].Similarity)
	}
}

func TestRerankerCrossEncoderError_FallsBackToRRF(t *testing.T) {
	client := &mockRerankClient{err: errors.New("service down")}
	r := NewReranker(client)
	chunks := []Chunk{
		{Text: "stream encoder settings", Similarity: 0.5},
		{Text: "configure stream latency", Similarity: 0.9},
	}
	result := r.Rerank(context.Background(), "stream latency", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	// Should fall back to RRF rerank
	if result[0].Text != "configure stream latency" {
		t.Fatalf("expected RRF fallback ordering, got %q first", result[0].Text)
	}
}

func TestRerankerNilClient_FallsBackToRRF(t *testing.T) {
	r := NewReranker(nil)
	chunks := []Chunk{
		{Text: "low relevance", Similarity: 0.3},
		{Text: "high relevance streaming", Similarity: 0.9},
	}
	result := r.Rerank(context.Background(), "streaming", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].Text != "high relevance streaming" {
		t.Fatalf("expected keyword fallback ordering, got %q first", result[0].Text)
	}
}

func TestRerankerNilReceiver_FallsBackToRRF(t *testing.T) {
	var r *Reranker
	result := r.Rerank(context.Background(), "test", []Chunk{{Text: "a", Similarity: 0.5}})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
}

func TestRerankerEmptyChunks(t *testing.T) {
	r := NewReranker(nil)
	result := r.Rerank(context.Background(), "query", nil)
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

func TestPackageLevelRerank_BackwardCompat(t *testing.T) {
	chunks := []Chunk{
		{Text: "stream encoder settings", Similarity: 0.5},
		{Text: "configure stream latency", Similarity: 0.9},
	}
	result := Rerank("stream latency", chunks)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
}
