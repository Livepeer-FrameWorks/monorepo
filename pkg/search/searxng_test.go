package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSearxngSearch(t *testing.T) {
	t.Parallel()

	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("format"); got != "json" {
			errCh <- fmt.Errorf("expected format json, got %q", got)
			return
		}
		if got := r.URL.Query().Get("q"); got != "encoder" {
			errCh <- fmt.Errorf("expected query encoder, got %q", got)
			return
		}
		resp := searxngResponse{
			Results: []struct {
				Title   string  `json:"title"`
				URL     string  `json:"url"`
				Content string  `json:"content"`
				Score   float64 `json:"score"`
			}{
				{
					Title:   "Searx Result",
					URL:     "https://searx.example",
					Content: "text",
					Score:   0.42,
				},
			},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			errCh <- fmt.Errorf("encode response: %w", err)
		}
	}))
	defer server.Close()

	provider, err := NewSearxngProvider(server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	results, err := provider.Search(context.Background(), "encoder", SearchOptions{})
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
	if results[0].Title != "Searx Result" {
		t.Fatalf("unexpected title %q", results[0].Title)
	}
}
