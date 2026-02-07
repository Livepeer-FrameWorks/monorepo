package knowledge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/pkg/llm"
)

const contextualTimeout = 60 * time.Second

// ContextualSummarizer generates short document-level context strings
// to prepend to each chunk before embedding, improving retrieval accuracy.
// See: https://www.anthropic.com/news/contextual-retrieval
type ContextualSummarizer interface {
	SummarizeChunks(ctx context.Context, title, docPrefix string, chunks []string) ([]string, error)
}

// LLMContextualSummarizer uses a (cheap) LLM to generate 1-2 sentence
// context for each chunk in a single batched call per document.
type LLMContextualSummarizer struct {
	provider llm.Provider
}

// NewLLMContextualSummarizer creates a summarizer backed by the given LLM provider.
func NewLLMContextualSummarizer(provider llm.Provider) *LLMContextualSummarizer {
	return &LLMContextualSummarizer{provider: provider}
}

const contextualPromptTemplate = `You are given a document and its chunks. For each chunk, write a concise 1-2 sentence context that situates the chunk within the overall document. The context should help a search engine understand what the chunk is about without reading the full document.

Document title: %s

Document beginning:
%s

Chunks (numbered):
%s

For each chunk, respond with ONLY the context on a single line, numbered to match. Example:
1. This chunk describes the authentication setup for the API gateway.
2. This chunk covers rate limiting configuration options.

Respond with exactly %d lines, one per chunk.`

func (s *LLMContextualSummarizer) SummarizeChunks(ctx context.Context, title, docPrefix string, chunks []string) ([]string, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	var numbered strings.Builder
	for i, chunk := range chunks {
		// Truncate each chunk preview to ~200 words for the prompt
		preview := truncateWords(chunk, 200)
		fmt.Fprintf(&numbered, "%d. %s\n", i+1, preview)
	}

	prompt := fmt.Sprintf(contextualPromptTemplate, title, docPrefix, numbered.String(), len(chunks))

	ctx, cancel := context.WithTimeout(ctx, contextualTimeout)
	defer cancel()

	start := time.Now()
	stream, err := s.provider.Complete(ctx, []llm.Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		contextualCallsTotal.WithLabelValues("error").Inc()
		return nil, fmt.Errorf("contextual summarization: %w", err)
	}
	defer stream.Close()

	var content strings.Builder
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			contextualCallsTotal.WithLabelValues("error").Inc()
			return nil, fmt.Errorf("contextual summarization stream: %w", recvErr)
		}
		content.WriteString(chunk.Content)
	}

	contextualDuration.Observe(time.Since(start).Seconds())
	contextualCallsTotal.WithLabelValues("success").Inc()

	lines := parseNumberedLines(content.String(), len(chunks))
	return lines, nil
}

// parseNumberedLines extracts context lines from the LLM response.
// Handles formats like "1. Context here" or "1: Context here" or just "Context here".
func parseNumberedLines(response string, expected int) []string {
	raw := strings.Split(strings.TrimSpace(response), "\n")
	results := make([]string, expected)

	idx := 0
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if idx >= expected {
			break
		}
		// Strip leading number prefix: "1. " or "1: " or "1) "
		cleaned := stripNumberPrefix(line)
		results[idx] = cleaned
		idx++
	}
	return results
}

func stripNumberPrefix(s string) string {
	for i, r := range s {
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '.' || r == ':' || r == ')' {
			return strings.TrimSpace(s[i+1:])
		}
		break
	}
	return s
}

func truncateWords(s string, maxWords int) string {
	words := strings.Fields(s)
	if len(words) <= maxWords {
		return s
	}
	return strings.Join(words[:maxWords], " ") + "..."
}
