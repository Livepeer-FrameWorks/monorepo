package search

import "context"

// Provider defines the interface for web search providers.
type Provider interface {
	Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error)
}

// Result represents a single search result.
type Result struct {
	Title   string
	URL     string
	Content string
	Score   float64
}

// SearchOptions controls search behavior across providers.
type SearchOptions struct {
	Limit       int
	SearchDepth string
}
