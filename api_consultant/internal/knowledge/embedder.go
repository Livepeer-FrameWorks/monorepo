package knowledge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	maxBatchTokens      = 250_000 // stay under typical 300K per-request API limits
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
	providerName string
	modelName    string
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

func WithProviderInfo(provider, model string) EmbedderOption {
	return func(e *Embedder) {
		e.providerName = provider
		e.modelName = model
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
	embedDuration.WithLabelValues(e.providerName, e.modelName).Observe(time.Since(embedStart).Seconds())
	if err != nil {
		embedCallsTotal.WithLabelValues(e.providerName, e.modelName, "error").Inc()
		return nil, fmt.Errorf("embed document: %w", err)
	}
	embedCallsTotal.WithLabelValues(e.providerName, e.modelName, "success").Inc()
	embedInputsTotal.WithLabelValues(e.providerName, e.modelName).Add(float64(len(embedTexts)))
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
	batches := splitBatches(chunks, maxEmbedBatchSize, maxBatchTokens)
	if len(batches) == 1 {
		return e.client.Embed(ctx, batches[0])
	}
	var all [][]float32
	for i, batch := range batches {
		vecs, err := e.client.Embed(ctx, batch)
		if err != nil {
			return nil, fmt.Errorf("embed batch %d: %w", i, err)
		}
		all = append(all, vecs...)
	}
	return all, nil
}

// splitBatches groups chunks into batches respecting both a maximum chunk
// count and a maximum total token budget per batch. This prevents the
// embedding API from rejecting requests that exceed its per-call token limit.
func splitBatches(chunks []string, maxChunks, maxTokens int) [][]string {
	var batches [][]string
	start := 0
	tokens := 0
	for i, chunk := range chunks {
		t := estimateBPETokens(chunk)
		if i > start && (i-start >= maxChunks || tokens+t > maxTokens) {
			batches = append(batches, chunks[start:i])
			start = i
			tokens = 0
		}
		tokens += t
	}
	if start < len(chunks) {
		batches = append(batches, chunks[start:])
	}
	return batches
}

func (e *Embedder) EmbedQuery(ctx context.Context, query string) ([]float32, error) {
	if query == "" {
		return nil, errors.New("query is required")
	}
	embedInputsTotal.WithLabelValues(e.providerName, e.modelName).Add(1)
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
	chunkCharLimit := e.chunkCharLimit()

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
		if blockTokens > e.tokenLimit || utf8.RuneCountInString(block) > chunkCharLimit {
			flushCurrent()
			blockWords := tokenize(block)
			// No-whitespace chunks (CJK, minified text, long blobs) need rune-based
			// splitting because strings.Fields cannot split them.
			if len(blockWords) <= 1 {
				runeLimit := e.tokenLimit
				if runeLimit <= 0 {
					runeLimit = chunkCharLimit
				}
				chunks = append(chunks, splitByRunes(block, runeLimit)...)
				continue
			}
			// splitLargeBlock works on word arrays; convert BPE limits to word counts
			wordLimit := int(float64(e.tokenLimit) / bpeMultiplier)
			wordOverlap := int(float64(e.tokenOverlap) / bpeMultiplier)
			chunks = append(chunks, splitLargeBlock(blockWords, wordLimit, wordOverlap)...)
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
	return enforceCharLimit(chunks, chunkCharLimit)
}

func (e *Embedder) chunkCharLimit() int {
	if e.tokenLimit <= 0 {
		return maxChunkChars
	}
	// Approximate 4 chars per token for Latin scripts, then cap globally.
	limit := e.tokenLimit * 4
	if limit > maxChunkChars {
		return maxChunkChars
	}
	return limit
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
// Word count * 1.3 gives ~75% accuracy for space-delimited text.
// For non-space scripts (CJK) and long single-token blobs, fallback to rune
// count to avoid severe underestimation.
func estimateBPETokens(text string) int {
	words := len(strings.Fields(text))
	byWords := int(math.Ceil(float64(words) * bpeMultiplier))
	runes := utf8.RuneCountInString(text)
	if runes == 0 {
		return byWords
	}
	// If content is effectively a single token, assume worst-case tokenization.
	if words <= 1 {
		if !hasCJKRunes(text) && runes <= 32 {
			return byWords
		}
		if runes > byWords {
			return runes
		}
		return byWords
	}
	// General safety floor for mixed scripts / punctuation-heavy text.
	byRunes := int(math.Ceil(float64(runes) / 4.0))
	if byRunes > byWords {
		return byRunes
	}
	return byWords
}

func hasCJKRunes(text string) bool {
	for _, r := range text {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			return true
		}
	}
	return false
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
	if maxChars <= 0 {
		return chunks
	}
	var result []string
	for _, chunk := range chunks {
		if utf8.RuneCountInString(chunk) <= maxChars {
			result = append(result, chunk)
			continue
		}
		words := strings.Fields(chunk)
		if len(words) == 0 {
			result = append(result, splitByRunes(chunk, maxChars)...)
			continue
		}
		var buf strings.Builder
		bufRunes := 0
		for _, w := range words {
			wRunes := utf8.RuneCountInString(w)
			if wRunes > maxChars {
				if bufRunes > 0 {
					result = append(result, buf.String())
					buf.Reset()
					bufRunes = 0
				}
				result = append(result, splitByRunes(w, maxChars)...)
				continue
			}
			added := wRunes
			if bufRunes > 0 {
				added++
			}
			if bufRunes > 0 && bufRunes+added > maxChars {
				result = append(result, buf.String())
				buf.Reset()
				bufRunes = 0
			}
			if bufRunes > 0 {
				buf.WriteByte(' ')
				bufRunes++
			}
			buf.WriteString(w)
			bufRunes += wRunes
		}
		if bufRunes > 0 {
			result = append(result, buf.String())
		}
	}
	return result
}

func splitByRunes(text string, limit int) []string {
	if limit <= 0 || text == "" {
		return nil
	}
	runes := []rune(text)
	result := make([]string, 0, (len(runes)+limit-1)/limit)
	for start := 0; start < len(runes); start += limit {
		end := start + limit
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[start:end])
		if chunk != "" {
			result = append(result, chunk)
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
