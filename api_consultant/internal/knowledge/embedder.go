package knowledge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"frameworks/pkg/llm"
)

const (
	defaultTokenLimit   = 500
	defaultTokenOverlap = 50
)

type EmbedderOption func(*Embedder)

type Embedder struct {
	client       llm.EmbeddingClient
	tokenLimit   int
	tokenOverlap int
}

func NewEmbedder(client llm.EmbeddingClient, opts ...EmbedderOption) (*Embedder, error) {
	if client == nil {
		return nil, errors.New("embedding client is required")
	}
	embedder := &Embedder{
		client:       client,
		tokenLimit:   defaultTokenLimit,
		tokenOverlap: defaultTokenOverlap,
	}
	for _, opt := range opts {
		opt(embedder)
	}
	if embedder.tokenLimit <= 0 {
		return nil, errors.New("token limit must be positive")
	}
	if embedder.tokenOverlap < 0 {
		return nil, errors.New("token overlap must be non-negative")
	}
	if embedder.tokenOverlap >= embedder.tokenLimit {
		return nil, errors.New("token overlap must be less than token limit")
	}
	return embedder, nil
}

func WithTokenLimit(limit int) EmbedderOption {
	return func(e *Embedder) {
		e.tokenLimit = limit
	}
}

func WithTokenOverlap(overlap int) EmbedderOption {
	return func(e *Embedder) {
		e.tokenOverlap = overlap
	}
}

func (e *Embedder) EmbedDocument(ctx context.Context, url, title, content string) ([]Chunk, error) {
	if content == "" {
		return nil, errors.New("content is required")
	}
	chunks := e.chunkContent(content)
	if len(chunks) == 0 {
		return nil, errors.New("content produced no chunks")
	}

	vectors, err := e.client.Embed(ctx, chunks)
	if err != nil {
		return nil, fmt.Errorf("embed document: %w", err)
	}
	if len(vectors) != len(chunks) {
		return nil, fmt.Errorf("embedding mismatch: %d chunks, %d vectors", len(chunks), len(vectors))
	}

	result := make([]Chunk, 0, len(chunks))
	for i, chunkText := range chunks {
		metadata := map[string]any{}
		if title != "" {
			metadata["title"] = title
		}
		result = append(result, Chunk{
			SourceURL:   url,
			SourceTitle: title,
			Text:        chunkText,
			Index:       i,
			Embedding:   vectors[i],
			Metadata:    metadata,
		})
	}

	return result, nil
}

func (e *Embedder) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if query == "" {
		return nil, errors.New("query is required")
	}
	vectors, err := e.client.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) == 0 {
		return nil, errors.New("no embedding returned")
	}
	return vectors[0], nil
}

func (e *Embedder) chunkContent(content string) []string {
	blocks := splitBlocks(content)
	var chunks []string
	var current []string
	currentTokens := 0

	flushCurrent := func() {
		if currentTokens == 0 {
			return
		}
		chunkText := strings.Join(current, "\n\n")
		chunks = append(chunks, chunkText)
		current = nil
		currentTokens = 0
	}

	for _, block := range blocks {
		blockTokens := tokenize(block)
		if len(blockTokens) == 0 {
			continue
		}
		if len(blockTokens) > e.tokenLimit {
			flushCurrent()
			chunks = append(chunks, splitLargeBlock(blockTokens, e.tokenLimit, e.tokenOverlap)...)
			continue
		}

		if currentTokens+len(blockTokens) <= e.tokenLimit {
			current = append(current, block)
			currentTokens += len(blockTokens)
			continue
		}

		flushCurrent()
		overlapText := overlapTokens(chunks[len(chunks)-1], e.tokenOverlap)
		if overlapText != "" {
			current = append(current, overlapText)
			currentTokens = len(tokenize(overlapText))
		}
		if currentTokens+len(blockTokens) > e.tokenLimit {
			flushCurrent()
		}
		current = append(current, block)
		currentTokens += len(blockTokens)
	}

	flushCurrent()
	return chunks
}

func splitBlocks(content string) []string {
	raw := strings.Split(content, "\n")
	var blocks []string
	var current []string

	flush := func() {
		if len(current) == 0 {
			return
		}
		block := strings.TrimSpace(strings.Join(current, " "))
		if block != "" {
			blocks = append(blocks, block)
		}
		current = nil
	}

	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			flush()
			blocks = append(blocks, trimmed)
			continue
		}
		current = append(current, trimmed)
	}
	flush()

	return blocks
}

func tokenize(text string) []string {
	return strings.Fields(text)
}

func splitLargeBlock(tokens []string, limit, overlap int) []string {
	if limit <= 0 {
		return nil
	}
	if overlap >= limit {
		overlap = limit - 1
	}
	step := limit - overlap
	var chunks []string
	for start := 0; start < len(tokens); start += step {
		end := start + limit
		if end > len(tokens) {
			end = len(tokens)
		}
		chunk := strings.Join(tokens[start:end], " ")
		chunks = append(chunks, chunk)
		if end == len(tokens) {
			break
		}
	}
	return chunks
}

func overlapTokens(text string, overlap int) string {
	if overlap <= 0 {
		return ""
	}
	tokens := tokenize(text)
	if len(tokens) <= overlap {
		return text
	}
	return strings.Join(tokens[len(tokens)-overlap:], " ")
}
