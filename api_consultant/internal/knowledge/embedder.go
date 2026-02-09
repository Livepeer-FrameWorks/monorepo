package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"frameworks/pkg/llm"
)

// ErrNoChunks is returned when content extraction produces text that is
// entirely filtered out (too short, navigation-only, or all duplicates).
var ErrNoChunks = errors.New("content produced no chunks")

const (
	// Token limits are expressed in approximate BPE tokens.
	// estimateBPETokens applies a 1.3x multiplier to word count.
	defaultTokenLimit   = 500
	defaultTokenOverlap = 50
	maxEmbedBatchSize   = 2048
	minChunkTokens      = 20
	bpeMultiplier       = 1.3

	// maxChunkChars is a hard character-count safety cap that catches cases
	// where the BPE word-based estimator underestimates (base64 blobs,
	// minified JS, SVG paths). OpenAI averages ~4 chars/BPE token; with an
	// 8192-token limit this gives ~32K chars max. We use 24000 (~6000 tokens)
	// to leave room for contextual prefixes.
	maxChunkChars = 24000
)

type EmbedderOption func(*Embedder)

type Embedder struct {
	client       llm.EmbeddingClient
	tokenLimit   int
	tokenOverlap int
	summarizer   ContextualSummarizer
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

func WithContextualRetrieval(s ContextualSummarizer) EmbedderOption {
	return func(e *Embedder) {
		e.summarizer = s
	}
}

func (e *Embedder) EmbedDocument(ctx context.Context, url, title, content string) ([]Chunk, error) {
	if content == "" {
		return nil, errors.New("content is required")
	}
	allChunks := e.chunkContent(content)
	// Skip boilerplate chunks, but only when token limit is large enough
	minTokens := minChunkTokens
	if e.tokenLimit < minTokens {
		minTokens = 1
	}
	var chunks []string
	seen := make(map[string]bool)
	for _, chunk := range allChunks {
		if estimateBPETokens(chunk) < minTokens {
			chunksFilteredTotal.WithLabelValues("below_min_tokens").Inc()
			continue
		}
		if isNavigationChunk(chunk) {
			chunksFilteredTotal.WithLabelValues("navigation").Inc()
			continue
		}
		normalized := normalizeForDedup(chunk)
		if seen[normalized] {
			chunksFilteredTotal.WithLabelValues("duplicate").Inc()
			continue
		}
		seen[normalized] = true
		chunks = append(chunks, chunk)
	}
	if len(chunks) == 0 {
		return nil, ErrNoChunks
	}

	// Contextual retrieval: prepend LLM-generated context to each chunk
	// before embedding, but store the original chunk text for display.
	embedTexts := chunks
	if e.summarizer != nil {
		docPrefix := truncateWords(content, 300)
		contexts, sumErr := e.summarizer.SummarizeChunks(ctx, title, docPrefix, chunks)
		if sumErr == nil && len(contexts) == len(chunks) {
			embedTexts = make([]string, len(chunks))
			for i := range chunks {
				if contexts[i] != "" {
					embedTexts[i] = "Context: " + contexts[i] + "\n\n" + chunks[i]
				} else {
					embedTexts[i] = chunks[i]
				}
			}
		}
		// On error or length mismatch, fall through to embed chunks as-is
	}

	embedStart := time.Now()
	vectors, err := e.embedBatched(ctx, embedTexts)
	embedDuration.Observe(time.Since(embedStart).Seconds())
	if err != nil {
		embedCallsTotal.WithLabelValues("error").Inc()
		return nil, fmt.Errorf("embed document: %w", err)
	}
	embedCallsTotal.WithLabelValues("success").Inc()
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

func (e *Embedder) embedBatched(ctx context.Context, chunks []string) ([][]float32, error) {
	if len(chunks) <= maxEmbedBatchSize {
		return e.client.Embed(ctx, chunks)
	}
	var all [][]float32
	for i := 0; i < len(chunks); i += maxEmbedBatchSize {
		end := i + maxEmbedBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch, err := e.client.Embed(ctx, chunks[i:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d: %w", i/maxEmbedBatchSize, err)
		}
		all = append(all, batch...)
	}
	return all, nil
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
		blockTokens := estimateBPETokens(block)
		if blockTokens == 0 {
			continue
		}
		if blockTokens > e.tokenLimit || len(block) > maxChunkChars {
			flushCurrent()
			// splitLargeBlock works on word arrays; convert BPE limits to word counts
			wordLimit := int(float64(e.tokenLimit) / bpeMultiplier)
			wordOverlap := int(float64(e.tokenOverlap) / bpeMultiplier)
			chunks = append(chunks, splitLargeBlock(tokenize(block), wordLimit, wordOverlap)...)
			continue
		}

		if currentTokens+blockTokens <= e.tokenLimit {
			current = append(current, block)
			currentTokens += blockTokens
			continue
		}

		flushCurrent()
		overlapText := overlapTokens(chunks[len(chunks)-1], e.tokenOverlap)
		if overlapText != "" {
			overlapToks := estimateBPETokens(overlapText)
			if overlapToks+blockTokens <= e.tokenLimit {
				current = append(current, overlapText)
				currentTokens = overlapToks
			}
		}
		current = append(current, block)
		currentTokens += blockTokens
	}

	flushCurrent()
	return enforceCharLimit(chunks, maxChunkChars)
}

func splitBlocks(content string) []string {
	raw := strings.Split(content, "\n")
	var blocks []string
	var current []string
	var currentHeading string
	inCodeFence := false

	flush := func() {
		if len(current) == 0 {
			return
		}
		block := strings.TrimSpace(strings.Join(current, " "))
		if block != "" {
			if currentHeading != "" {
				block = currentHeading + "\n\n" + block
			}
			blocks = append(blocks, block)
		}
		current = nil
	}

	for _, line := range raw {
		trimmed := strings.TrimSpace(line)

		// Code fence toggle (``` or ~~~)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			flush()
			inCodeFence = !inCodeFence
			continue
		}
		if inCodeFence {
			current = append(current, trimmed)
			continue
		}

		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			flush()
			currentHeading = trimmed
			continue
		}
		if isHorizontalRule(trimmed) {
			flush()
			continue
		}
		// HTML block-level elements act as section boundaries
		if isHTMLBlockTag(trimmed) {
			flush()
			continue
		}
		current = append(current, trimmed)
	}
	flush()

	return blocks
}

func isHorizontalRule(line string) bool {
	clean := strings.ReplaceAll(line, " ", "")
	if len(clean) < 3 {
		return false
	}
	switch clean[0] {
	case '-', '*', '_':
		for i := 1; i < len(clean); i++ {
			if clean[i] != clean[0] {
				return false
			}
		}
		return true
	}
	return false
}

var htmlBlockPrefixes = []string{
	"<hr", "<div", "</div", "<section", "</section",
	"<article", "</article", "<nav", "</nav",
	"<header", "</header", "<footer", "</footer",
}

func isHTMLBlockTag(line string) bool {
	lower := strings.ToLower(line)
	for _, prefix := range htmlBlockPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func tokenize(text string) []string {
	return strings.Fields(text)
}

// estimateBPETokens returns an approximate BPE token count for text.
// Word count * 1.3 gives ~75% accuracy vs true BPE tokenizers.
func estimateBPETokens(text string) int {
	return int(math.Ceil(float64(len(strings.Fields(text))) * bpeMultiplier))
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

// enforceCharLimit splits any chunk whose character count exceeds maxChars
// at word boundaries. This catches cases where word-based token estimation
// dramatically underestimates (e.g. base64 blobs count as 1 word).
func enforceCharLimit(chunks []string, maxChars int) []string {
	var result []string
	for _, chunk := range chunks {
		if len(chunk) <= maxChars {
			result = append(result, chunk)
			continue
		}
		words := strings.Fields(chunk)
		var buf strings.Builder
		for _, w := range words {
			if buf.Len() > 0 && buf.Len()+1+len(w) > maxChars {
				result = append(result, buf.String())
				buf.Reset()
			}
			if buf.Len() > 0 {
				buf.WriteByte(' ')
			}
			buf.WriteString(w)
		}
		if buf.Len() > 0 {
			result = append(result, buf.String())
		}
	}
	return result
}

// isNavigationChunk detects chunks that are mostly navigation text (short
// link-like words). Heuristic: >50% of words are 3 characters or shorter.
func isNavigationChunk(chunk string) bool {
	words := strings.Fields(chunk)
	if len(words) < 5 {
		return false
	}
	short := 0
	for _, w := range words {
		if len(w) <= 3 {
			short++
		}
	}
	return float64(short)/float64(len(words)) > 0.5
}

// normalizeForDedup returns a lowercased, whitespace-collapsed version of
// the chunk for near-identical deduplication.
func normalizeForDedup(chunk string) string {
	return strings.ToLower(strings.Join(strings.Fields(chunk), " "))
}
