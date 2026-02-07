package chat

import (
	"context"
	"errors"
	"io"
	"testing"

	"frameworks/pkg/llm"
)

type fakeHyDEEmbedder struct {
	vector []float32
	err    error
}

func (f *fakeHyDEEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return f.vector, f.err
}

func TestHyDE_GeneratesAndEmbeds(t *testing.T) {
	provider := &fakeRewriterLLM{response: "To reduce rebuffering, configure adaptive bitrate settings."}
	embedder := &fakeHyDEEmbedder{vector: []float32{0.1, 0.2, 0.3}}

	hyde := NewHyDEGenerator(provider, embedder)
	vec, err := hyde.GenerateAndEmbed(context.Background(), "how do I fix buffering?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dim vector, got %d", len(vec))
	}
	if vec[0] != 0.1 {
		t.Fatalf("unexpected vector: %v", vec)
	}
}

func TestHyDE_LLMError(t *testing.T) {
	provider := &fakeRewriterLLM{err: errors.New("llm down")}
	embedder := &fakeHyDEEmbedder{vector: []float32{0.1}}

	hyde := NewHyDEGenerator(provider, embedder)
	_, err := hyde.GenerateAndEmbed(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
}

func TestHyDE_EmbedderError(t *testing.T) {
	provider := &fakeRewriterLLM{response: "hypothetical answer"}
	embedder := &fakeHyDEEmbedder{err: errors.New("embed down")}

	hyde := NewHyDEGenerator(provider, embedder)
	_, err := hyde.GenerateAndEmbed(context.Background(), "query")
	if err == nil {
		t.Fatal("expected error when embedder fails")
	}
}

func TestHyDE_EmptyResponse(t *testing.T) {
	provider := &fakeRewriterLLM{response: "  "}
	embedder := &fakeHyDEEmbedder{vector: []float32{0.1}}

	hyde := NewHyDEGenerator(provider, embedder)
	vec, err := hyde.GenerateAndEmbed(context.Background(), "query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vec != nil {
		t.Fatalf("expected nil vector for empty response, got %v", vec)
	}
}

// Verify that the fakeRewriterLLM and singleChunkStream from query_rewriter_test.go
// work correctly for the HyDE tests (they share the same test file package).
func TestHyDE_StreamRecv(t *testing.T) {
	s := &singleChunkStream{content: "test"}
	chunk, err := s.Recv()
	if err != nil {
		t.Fatalf("first recv: %v", err)
	}
	if chunk.Content != "test" {
		t.Fatalf("unexpected content: %q", chunk.Content)
	}
	_, err = s.Recv()
	if err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// Reuse the singleChunkStream and fakeRewriterLLM from query_rewriter_test.go
// (same package, same test binary).
var _ llm.Provider = (*fakeRewriterLLM)(nil)
