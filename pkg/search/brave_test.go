package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBraveSearch(t *testing.T) {
	t.Parallel()

	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "brave-key" {
			errCh <- fmt.Errorf("missing brave api key")
			return
		}
		if got := r.URL.Query().Get("q"); got != "streaming" {
			errCh <- fmt.Errorf("expected query streaming, got %q", got)
			return
		}
		if got := r.URL.Query().Get("count"); got != "3" {
			errCh <- fmt.Errorf("expected count 3, got %q", got)
			return
		}
		resp := braveResponse{}
		resp.Web.Results = []struct {
			Title       string  `json:"title"`
			URL         string  `json:"url"`
			Description string  `json:"description"`
			Score       float64 `json:"score"`
		}{
			{
				Title:       "Brave Result",
				URL:         "https://brave.com",
				Description: "snippet",
				Score:       0.88,
			},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			errCh <- fmt.Errorf("encode response: %w", err)
		}
	}))
	defer server.Close()

	provider, err := NewBraveProvider("brave-key", server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}

	results, err := provider.Search(context.Background(), "streaming", SearchOptions{Limit: 3})
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
	if results[0].URL != "https://brave.com" {
		t.Fatalf("unexpected url %q", results[0].URL)
	}
}
