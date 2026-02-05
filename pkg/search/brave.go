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

const defaultBraveURL = "https://api.search.brave.com/res/v1/web/search"

// BraveProvider implements the Brave Search API.
type BraveProvider struct {
	apiKey string
	apiURL string
	client *http.Client
}

// NewBraveProvider creates a Brave search provider.
func NewBraveProvider(apiKey, apiURL string) (*BraveProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("brave api key is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultBraveURL
	}
	return &BraveProvider{
		apiKey: apiKey,
		apiURL: apiURL,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string  `json:"title"`
			URL         string  `json:"url"`
			Description string  `json:"description"`
			Score       float64 `json:"score"`
		} `json:"results"`
	} `json:"web"`
}

// Search executes a query against the Brave Search API.
func (p *BraveProvider) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	endpoint, err := url.Parse(p.apiURL)
	if err != nil {
		return nil, fmt.Errorf("parse brave url: %w", err)
	}
	q := endpoint.Query()
	q.Set("q", query)
	if opts.Limit > 0 {
		q.Set("count", fmt.Sprintf("%d", opts.Limit))
	}
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create brave request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("brave request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("brave request failed with status %d", resp.StatusCode)
	}

	var decoded braveResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode brave response: %w", err)
	}

	results := make([]Result, 0, len(decoded.Web.Results))
	for _, item := range decoded.Web.Results {
		results = append(results, Result{
			Title:   item.Title,
			URL:     item.URL,
			Content: strings.TrimSpace(item.Description),
			Score:   item.Score,
		})
	}

	return results, nil
}
