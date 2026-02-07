package knowledge

import (
	"context"
	"strings"

	"frameworks/pkg/llm"
)

const (
	vectorWeight  = 0.7
	keywordWeight = 0.3
)

// Reranker rescores retrieved chunks. When a cross-encoder client is
// configured it delegates to the model; otherwise it falls back to the
// keyword-overlap heuristic.
type Reranker struct {
	client llm.RerankClient // nil = keyword fallback
}

// NewReranker creates a Reranker. Pass nil to use the keyword fallback.
func NewReranker(client llm.RerankClient) *Reranker {
	return &Reranker{client: client}
}

// Rerank rescores chunks and returns them sorted by descending relevance.
// The method is context-aware so the cross-encoder call can be cancelled.
func (r *Reranker) Rerank(ctx context.Context, query string, chunks []Chunk) []Chunk {
	if len(chunks) == 0 {
		return chunks
	}
	if r != nil && r.client != nil {
		result := r.crossEncoderRerank(ctx, query, chunks)
		if result != nil {
			return result
		}
		// Cross-encoder failed; fall through to keyword fallback.
	}
	return keywordRerank(query, chunks)
}

func (r *Reranker) crossEncoderRerank(ctx context.Context, query string, chunks []Chunk) []Chunk {
	documents := make([]string, len(chunks))
	for i, c := range chunks {
		documents[i] = c.Text
	}

	results, err := r.client.Rerank(ctx, query, documents)
	if err != nil {
		return nil
	}

	type scored struct {
		chunk Chunk
		score float64
	}
	items := make([]scored, len(results))
	for i, rr := range results {
		items[i] = scored{chunk: chunks[rr.Index], score: rr.RelevanceScore}
	}

	// Sort descending by score (insertion sort for small slices)
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].score > items[j-1].score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}

	out := make([]Chunk, len(items))
	for i, item := range items {
		item.chunk.Similarity = item.score
		out[i] = item.chunk
	}
	return out
}

// keywordRerank is the original heuristic: 0.7*vector + 0.3*keyword overlap.
func keywordRerank(query string, chunks []Chunk) []Chunk {
	if len(chunks) == 0 {
		return chunks
	}

	queryTerms := uniqueLowerTerms(query)
	if len(queryTerms) == 0 {
		return chunks
	}

	type scored struct {
		chunk Chunk
		score float64
	}

	items := make([]scored, len(chunks))
	for i, chunk := range chunks {
		kwScore := keywordOverlap(queryTerms, chunk.Text)
		combined := vectorWeight*chunk.Similarity + keywordWeight*kwScore
		items[i] = scored{chunk: chunk, score: combined}
	}

	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].score > items[j-1].score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}

	result := make([]Chunk, len(items))
	for i, item := range items {
		item.chunk.Similarity = item.score
		result[i] = item.chunk
	}
	return result
}

// Rerank is the package-level function preserved for backward compatibility.
// It uses the keyword-overlap fallback only.
func Rerank(query string, chunks []Chunk) []Chunk {
	return keywordRerank(query, chunks)
}

// keywordOverlap returns the fraction of query terms found in the text.
func keywordOverlap(queryTerms map[string]struct{}, text string) float64 {
	if len(queryTerms) == 0 {
		return 0
	}
	lower := strings.ToLower(text)
	found := 0
	for term := range queryTerms {
		if strings.Contains(lower, term) {
			found++
		}
	}
	return float64(found) / float64(len(queryTerms))
}

// DeduplicateBySource caps the number of chunks from any single source URL
// to maxPerSource, returning at most limit results total.
func DeduplicateBySource(chunks []Chunk, limit, maxPerSource int) []Chunk {
	if len(chunks) <= limit {
		return chunks
	}
	sourceCounts := make(map[string]int)
	result := make([]Chunk, 0, limit)
	for _, chunk := range chunks {
		if len(result) >= limit {
			break
		}
		if sourceCounts[chunk.SourceURL] >= maxPerSource {
			continue
		}
		sourceCounts[chunk.SourceURL]++
		result = append(result, chunk)
	}
	return result
}

func uniqueLowerTerms(text string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(text))
	terms := make(map[string]struct{}, len(words))
	for _, w := range words {
		if len(w) >= 3 {
			terms[w] = struct{}{}
		}
	}
	return terms
}
