package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const defaultTavilyURL = "https://api.tavily.com/search"

// TavilyProvider implements the Tavily Search API.
type TavilyProvider struct {
	apiKey string
	apiURL string
	client *http.Client
}

// NewTavilyProvider creates a Tavily search provider.
func NewTavilyProvider(apiKey, apiURL string) (*TavilyProvider, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("tavily api key is required")
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = defaultTavilyURL
	}
	return &TavilyProvider{
		apiKey: apiKey,
		apiURL: apiURL,
		client: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type tavilyRequest struct {
	APIKey            string `json:"api_key"`
	Query             string `json:"query"`
	SearchDepth       string `json:"search_depth,omitempty"`
	MaxResults        int    `json:"max_results,omitempty"`
	IncludeRawContent bool   `json:"include_raw_content"`
}

type tavilyResponse struct {
	Results []struct {
		Title      string  `json:"title"`
		URL        string  `json:"url"`
		Content    string  `json:"content"`
		RawContent string  `json:"raw_content"`
		Score      float64 `json:"score"`
	} `json:"results"`
}

// Search executes a query against the Tavily Search API.
func (p *TavilyProvider) Search(ctx context.Context, query string, opts SearchOptions) ([]Result, error) {
	reqBody := tavilyRequest{
		APIKey:            p.apiKey,
		Query:             query,
		SearchDepth:       opts.SearchDepth,
		MaxResults:        opts.Limit,
		IncludeRawContent: true,
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal tavily request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create tavily request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tavily request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("tavily request failed with status %d", resp.StatusCode)
	}

	var decoded tavilyResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode tavily response: %w", err)
	}

	results := make([]Result, 0, len(decoded.Results))
	for _, item := range decoded.Results {
		content := strings.TrimSpace(item.RawContent)
		if content == "" {
			content = strings.TrimSpace(item.Content)
		}
		results = append(results, Result{
			Title:   item.Title,
			URL:     item.URL,
			Content: content,
			Score:   item.Score,
		})
	}

	return results, nil
}
