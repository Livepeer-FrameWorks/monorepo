package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTavilySearch(t *testing.T) {
	t.Parallel()

	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errCh <- fmt.Errorf("expected POST, got %s", r.Method)
			return
		}
		var req tavilyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errCh <- fmt.Errorf("decode request: %w", err)
			return
		}
		if req.APIKey != "test-key" {
			errCh <- fmt.Errorf("expected api_key test-key, got %q", req.APIKey)
			return
		}
		if req.SearchDepth != "advanced" {
			errCh <- fmt.Errorf("expected search_depth advanced, got %q", req.SearchDepth)
			return
		}
		if req.MaxResults != 2 {
			errCh <- fmt.Errorf("expected max_results 2, got %d", req.MaxResults)
			return
		}

		resp := tavilyResponse{
			Results: []struct {
				Title      string  `json:"title"`
				URL        string  `json:"url"`
				Content    string  `json:"content"`
				RawContent string  `json:"raw_content"`
				Score      float64 `json:"score"`
			}{
				{
					Title:      "Example",
					URL:        "https://example.com",
					Content:    "snippet",
					RawContent: "full content",
					Score:      0.99,
				},
			},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			errCh <- fmt.Errorf("encode response: %w", err)
			return
		}
	}))
	defer server.Close()

	provider, err := NewTavilyProvider("test-key", server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	results, err := provider.Search(context.Background(), "query", SearchOptions{Limit: 2, SearchDepth: "advanced"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("handler error: %v", err)
	default:
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Content != "full content" {
		t.Fatalf("expected raw content, got %q", results[0].Content)
	}
}
