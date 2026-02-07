package knowledge

import (
	"context"
	"testing"
)

type fakeEmbeddingClient struct {
	vectors [][]float32
}

func (f fakeEmbeddingClient) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	if len(f.vectors) == len(inputs) {
		return f.vectors, nil
	}
	vectors := make([][]float32, 0, len(inputs))
	for i := range inputs {
		vectors = append(vectors, []float32{float32(i)})
	}
	return vectors, nil
}

func TestEmbedderChunksDocument(t *testing.T) {
	embedder, err := NewEmbedder(fakeEmbeddingClient{}, WithTokenLimit(7), WithTokenOverlap(3))
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	content := "one two three four five six seven eight nine ten eleven twelve"
	chunks, err := embedder.EmbedDocument(context.Background(), "https://example.com", "Title", content)
	if err != nil {
		t.Fatalf("embed document: %v", err)
	}
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if chunk.Index != i {
			t.Fatalf("expected chunk index %d, got %d", i, chunk.Index)
		}
		if chunk.Text == "" {
			t.Fatalf("expected chunk text")
		}
	}
}

func TestEmbedderQuery(t *testing.T) {
	client := fakeEmbeddingClient{vectors: [][]float32{{0.5}}}
	embedder, err := NewEmbedder(client)
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}

	vector, err := embedder.EmbedQuery(context.Background(), "hello")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	if len(vector) != 1 || vector[0] != 0.5 {
		t.Fatalf("unexpected vector: %v", vector)
	}
}
