package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// SearxngProvider implements the SearXNG API.
type SearxngProvider struct {
	apiURL string
	client *http.Client
}

// NewSearxngProvider creates a SearXNG provider.
func NewSearxngProvider(apiURL string) (*SearxngProvider, error) {
	if strings.TrimSpace(apiURL) == "" {
		return nil, fmt.Errorf("searxng api url is required")
	}
	return &SearxngProvider{
		apiURL: strings.TrimRight(apiURL, "/"),
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type searxngResponse struct {
	Results []struct {
		Title   string  `json:"title"`
		URL     string  `json:"url"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	} `json:"results"`
}

// Search executes a query against a SearXNG instance.
func (p *SearxngProvider) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	endpoint, err := url.Parse(p.apiURL + "/search")
	if err != nil {
		return nil, fmt.Errorf("parse searxng url: %w", err)
	}
	q := endpoint.Query()
	q.Set("q", query)
	q.Set("format", "json")
	if opts.Limit > 0 {
		q.Set("count", fmt.Sprintf("%d", opts.Limit))
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create searxng request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("searxng request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("searxng request failed with status %d", resp.StatusCode)
	}

	var decoded searxngResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode searxng response: %w", err)
	}

	results := make([]Result, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		results = append(results, Result{
			Title:   item.Title,
			URL:     item.URL,
			Content: strings.TrimSpace(item.Content),
			Score:   item.Score,
		})
	}

	return results, nil
}
