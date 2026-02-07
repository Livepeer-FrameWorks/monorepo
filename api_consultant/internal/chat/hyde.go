package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"frameworks/pkg/llm"
)

const hydeTimeout = 15 * time.Second

const hydePrompt = `You are a video streaming documentation expert. Write a short, factual paragraph that would answer this question: %s

Write only the answer, no preamble.`

// HyDEGenerator produces Hypothetical Document Embeddings. It asks a utility
// LLM to generate a hypothetical answer to the user's query, then embeds that
// answer. The resulting vector is closer in embedding space to real answers
// than the original question would be.
type HyDEGenerator struct {
	llm      llm.Provider
	embedder KnowledgeEmbedder
}

// NewHyDEGenerator creates a HyDE generator backed by the given LLM and embedder.
func NewHyDEGenerator(provider llm.Provider, embedder KnowledgeEmbedder) *HyDEGenerator {
	return &HyDEGenerator{llm: provider, embedder: embedder}
}

// GenerateAndEmbed generates a hypothetical answer and returns its embedding.
// Returns (nil, err) on failure so the caller can fall back to a regular query embedding.
func (h *HyDEGenerator) GenerateAndEmbed(ctx context.Context, query string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, hydeTimeout)
	defer cancel()

	prompt := strings.Replace(hydePrompt, "%s", query, 1)
	stream, err := h.llm.Complete(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	var hypothetical strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		hypothetical.WriteString(chunk.Content)
	}

	hypoText := strings.TrimSpace(hypothetical.String())
	if hypoText == "" {
		return nil, nil
	}

	return h.embedder.EmbedQuery(ctx, hypoText)
}
