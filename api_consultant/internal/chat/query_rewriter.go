package chat

import (
	"context"
	"io"
	"strings"
	"time"

	"frameworks/pkg/llm"
)

const queryRewriteTimeout = 10 * time.Second

const queryRewritePrompt = `Rewrite this conversational user query into a concise search query optimized for documentation retrieval about a live video streaming platform. Output only the rewritten query, nothing else.

User query: %s`

// QueryRewriter transforms conversational user queries into search-optimized
// queries using a utility LLM. This improves both knowledge and web search by
// bridging vocabulary gaps (e.g. "my stream keeps dying" → "stream disconnection troubleshooting").
type QueryRewriter struct {
	llm llm.Provider
}

// NewQueryRewriter creates a rewriter backed by the given LLM provider.
func NewQueryRewriter(provider llm.Provider) *QueryRewriter {
	return &QueryRewriter{llm: provider}
}

// Rewrite transforms a conversational query into a search-optimized query.
// Returns the original query on any error.
func (qr *QueryRewriter) Rewrite(ctx context.Context, query string) string {
	if qr == nil || qr.llm == nil {
		return query
	}

	ctx, cancel := context.WithTimeout(ctx, queryRewriteTimeout)
	defer cancel()

	prompt := strings.Replace(queryRewritePrompt, "%s", query, 1)
	stream, err := qr.llm.Complete(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return query
	}
	defer stream.Close()

	var result strings.Builder
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return query
		}
		result.WriteString(chunk.Content)
	}

	rewritten := strings.TrimSpace(result.String())
	if rewritten == "" {
		return query
	}
	return rewritten
}
