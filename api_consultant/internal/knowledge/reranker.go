package knowledge

import (
	"context"
	"sort"
	"strings"
	"time"

	"frameworks/pkg/llm"
)

const rrfK = 60 // standard Reciprocal Rank Fusion constant

// Reranker rescores retrieved chunks. When a cross-encoder client is
// configured it delegates to the model; otherwise it falls back to the
// RRF (Reciprocal Rank Fusion) heuristic.
type Reranker struct {
	client       llm.RerankClient // nil = keyword fallback
	providerName string
	modelName    string
}

// NewReranker creates a Reranker. Pass nil client to use the keyword fallback.
func NewReranker(client llm.RerankClient, provider, model string) *Reranker {
	return &Reranker{client: client, providerName: provider, modelName: model}
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
	return rrfRerank(query, chunks)
}

func (r *Reranker) crossEncoderRerank(ctx context.Context, query string, chunks []Chunk) []Chunk {
	documents := make([]string, len(chunks))
	for i, c := range chunks {
		documents[i] = c.Text
	}

	start := time.Now()
	results, err := r.client.Rerank(ctx, query, documents)
	rerankDuration.WithLabelValues(r.providerName, r.modelName).Observe(time.Since(start).Seconds())
	if err != nil {
		rerankCallsTotal.WithLabelValues(r.providerName, r.modelName, "error").Inc()
		return nil
	}
	rerankCallsTotal.WithLabelValues(r.providerName, r.modelName, "success").Inc()
	rerankDocumentsTotal.WithLabelValues(r.providerName, r.modelName).Add(float64(len(documents)))

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

// rrfRerank uses Reciprocal Rank Fusion to combine vector similarity and
// keyword overlap rankings. RRF is rank-based (immune to score scale
// differences) and is the standard fusion method in information retrieval.
// score(d) = 1/(k + vectorRank) + 1/(k + keywordRank), k=60.
func rrfRerank(query string, chunks []Chunk) []Chunk {
	if len(chunks) == 0 {
		return chunks
	}

	queryTerms := uniqueLowerTerms(query)
	if len(queryTerms) == 0 {
		return chunks
	}

	n := len(chunks)

	// Compute keyword overlap scores.
	kwScores := make([]float64, n)
	for i, c := range chunks {
		kwScores[i] = keywordOverlap(queryTerms, c.Text)
	}

	// Rank by vector similarity (Similarity field from DB), descending.
	vectorOrder := make([]int, n)
	for i := range vectorOrder {
		vectorOrder[i] = i
	}
	sort.SliceStable(vectorOrder, func(a, b int) bool {
		return chunks[vectorOrder[a]].Similarity > chunks[vectorOrder[b]].Similarity
	})
	vectorRank := make([]int, n)
	for rank, idx := range vectorOrder {
		vectorRank[idx] = rank + 1
	}

	// Rank by keyword overlap, descending.
	kwOrder := make([]int, n)
	for i := range kwOrder {
		kwOrder[i] = i
	}
	sort.SliceStable(kwOrder, func(a, b int) bool {
		return kwScores[kwOrder[a]] > kwScores[kwOrder[b]]
	})
	kwRank := make([]int, n)
	for rank, idx := range kwOrder {
		kwRank[idx] = rank + 1
	}

	// RRF fusion.
	type scored struct {
		chunk Chunk
		score float64
	}
	items := make([]scored, n)
	for i, c := range chunks {
		score := 1.0/float64(rrfK+vectorRank[i]) + 1.0/float64(rrfK+kwRank[i])
		items[i] = scored{chunk: c, score: score}
	}

	sort.SliceStable(items, func(a, b int) bool {
		return items[a].score > items[b].score
	})

	result := make([]Chunk, n)
	for i, item := range items {
		item.chunk.Similarity = item.score
		result[i] = item.chunk
	}
	return result
}

// Rerank is the package-level function preserved for backward compatibility.
// It uses the RRF fallback.
func Rerank(query string, chunks []Chunk) []Chunk {
	return rrfRerank(query, chunks)
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
