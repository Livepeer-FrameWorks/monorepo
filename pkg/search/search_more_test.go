package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBraveStatus300Rejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
	}))
	defer server.Close()

	provider, err := NewBraveProvider("k", server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Search(context.Background(), "q", SearchOptions{Limit: 1})
	if err == nil {
		t.Fatal("status 300 must be rejected")
	}
	if !strings.Contains(err.Error(), "300") {
		t.Fatalf("expected status 300 error, got %v", err)
	}
}

func TestSearxngStatus300Rejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
	}))
	defer server.Close()

	provider, err := NewSearxngProvider(server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Search(context.Background(), "q", SearchOptions{Limit: 1})
	if err == nil {
		t.Fatal("status 300 must be rejected")
	}
	if !strings.Contains(err.Error(), "300") {
		t.Fatalf("expected status 300 error, got %v", err)
	}
}

func TestTavilyStatus300Rejected(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
	}))
	defer server.Close()

	provider, err := NewTavilyProvider("k", server.URL)
	if err != nil {
		t.Fatalf("new provider: %v", err)
	}
	_, err = provider.Search(context.Background(), "q", SearchOptions{Limit: 1})
	if err == nil {
		t.Fatal("status 300 must be rejected")
	}
	if !strings.Contains(err.Error(), "300") {
		t.Fatalf("expected status 300 error, got %v", err)
	}
}

func TestBraveCountParamGatedOnLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		limit     int
		wantCount string
	}{
		{"zero_limit_omits_count", 0, ""},
		{"positive_limit_sets_count", 5, "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCount := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCount <- r.URL.Query().Get("count")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			provider, err := NewBraveProvider("k", server.URL)
			if err != nil {
				t.Fatalf("new provider: %v", err)
			}
			if _, err := provider.Search(context.Background(), "q", SearchOptions{Limit: tt.limit}); err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := <-gotCount; got != tt.wantCount {
				t.Fatalf("limit=%d count=%q want %q", tt.limit, got, tt.wantCount)
			}
		})
	}
}

func TestSearxngCountParamGatedOnLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		limit     int
		wantCount string
	}{
		{"zero_limit_omits_count", 0, ""},
		{"positive_limit_sets_count", 5, "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCount := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotCount <- r.URL.Query().Get("count")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			provider, err := NewSearxngProvider(server.URL)
			if err != nil {
				t.Fatalf("new provider: %v", err)
			}
			if _, err := provider.Search(context.Background(), "q", SearchOptions{Limit: tt.limit}); err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := <-gotCount; got != tt.wantCount {
				t.Fatalf("limit=%d count=%q want %q", tt.limit, got, tt.wantCount)
			}
		})
	}
}
