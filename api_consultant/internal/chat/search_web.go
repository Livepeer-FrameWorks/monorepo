package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_consultant/internal/metering"
	"frameworks/pkg/search"
)

const (
	defaultSearchLimit  = 8
	maxSearchLimit      = 20
	defaultSearchDepth  = "basic"
	maxSnippetRuneCount = 320
)

type SearchWebInput struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit,omitempty"`
	SearchDepth string `json:"search_depth,omitempty"`
}

type SearchWebResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

type SearchWebResponse struct {
	Query   string            `json:"query"`
	Context string            `json:"context"`
	Results []SearchWebResult `json:"results"`
	Sources []Source          `json:"sources"`
}

type SearchWebTool struct {
	provider    search.Provider
	searchLimit int
}

func NewSearchWebTool(provider search.Provider) *SearchWebTool {
	return &SearchWebTool{provider: provider, searchLimit: defaultSearchLimit}
}

// SetSearchLimit overrides the default limit for web searches.
func (t *SearchWebTool) SetSearchLimit(limit int) {
	if limit > 0 {
		t.searchLimit = limit
	}
}

func (t *SearchWebTool) Call(ctx context.Context, arguments string) (SearchWebResponse, error) {
	if t.provider == nil {
		return SearchWebResponse{}, errors.New("search provider is required")
	}

	var input SearchWebInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return SearchWebResponse{}, fmt.Errorf("parse search_web arguments: %w", err)
	}

	return t.Search(ctx, input)
}

func (t *SearchWebTool) Search(ctx context.Context, input SearchWebInput) (SearchWebResponse, error) {
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return SearchWebResponse{}, errors.New("search query is required")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = t.searchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	depth := strings.TrimSpace(input.SearchDepth)
	if depth == "" {
		depth = defaultSearchDepth
	}

	results, err := t.provider.Search(ctx, query, search.SearchOptions{
		Limit:       limit,
		SearchDepth: depth,
	})
	if err != nil {
		return SearchWebResponse{}, err
	}
	metering.RecordSearchQuery(ctx)

	mapped := make([]SearchWebResult, 0, len(results))
	sources := make([]Source, 0, len(results))
	for _, result := range results {
		title := strings.TrimSpace(result.Title)
		url := strings.TrimSpace(result.URL)
		if title == "" {
			title = url
		}
		snippet := snippetFromContent(result.Content)
		mapped = append(mapped, SearchWebResult{
			Title:   title,
			URL:     url,
			Snippet: snippet,
			Score:   result.Score,
		})
		sources = append(sources, Source{
			Title: title,
			URL:   url,
			Type:  SourceTypeWeb,
		})
	}

	return SearchWebResponse{
		Query:   query,
		Context: formatSearchContext(mapped),
		Results: mapped,
		Sources: sources,
	}, nil
}

func formatSearchContext(results []SearchWebResult) string {
	if len(results) == 0 {
		return "No web search results found."
	}

	var builder strings.Builder
	builder.WriteString("Web search results:\n")
	for i, result := range results {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, result.Title)
		if result.URL != "" {
			fmt.Fprintf(&builder, "URL: %s\n", result.URL)
		}
		if result.Snippet != "" {
			fmt.Fprintf(&builder, "Snippet: %s\n", result.Snippet)
		}
		if i < len(results)-1 {
			builder.WriteString("\n")
		}
	}

	return strings.TrimSpace(builder.String())
}

func snippetFromContent(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	content = strings.Join(strings.Fields(content), " ")
	return truncateRunes(content, maxSnippetRuneCount)
}

func truncateRunes(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	if limit == 1 {
		return string(runes[:1])
	}
	return string(runes[:limit-1]) + "…"
}
