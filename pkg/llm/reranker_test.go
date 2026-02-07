package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewRerankClient_RequiresProvider(t *testing.T) {
	_, err := NewRerankClient(RerankConfig{})
	if err == nil {
		t.Fatal("expected error for empty provider")
	}
}

func TestNewRerankClient_GenericRequiresURL(t *testing.T) {
	_, err := NewRerankClient(RerankConfig{Provider: "generic"})
	if err == nil {
		t.Fatal("expected error when generic has no URL")
	}
}

func TestNewRerankClient_UnknownProvider(t *testing.T) {
	_, err := NewRerankClient(RerankConfig{Provider: "notreal", APIURL: "http://localhost"})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRerankCohere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected auth: %s", r.Header.Get("Authorization"))
		}
		var req cohereRerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if req.Query != "streaming issues" {
			t.Fatalf("unexpected query: %s", req.Query)
		}
		if len(req.Documents) != 2 {
			t.Fatalf("expected 2 documents, got %d", len(req.Documents))
		}
		resp := cohereRerankResponse{
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			}{
				{Index: 1, RelevanceScore: 0.95},
				{Index: 0, RelevanceScore: 0.42},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, err := NewRerankClient(RerankConfig{
		Provider: "cohere",
		Model:    "rerank-v3.5",
		APIKey:   "test-key",
		APIURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	results, err := client.Rerank(context.Background(), "streaming issues", []string{"doc A", "doc B"})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Index != 1 || results[0].RelevanceScore != 0.95 {
		t.Fatalf("unexpected first result: %+v", results[0])
	}
}

func TestRerankJina(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := jinaRerankResponse{
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			}{
				{Index: 0, RelevanceScore: 0.88},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, err := NewRerankClient(RerankConfig{
		Provider: "jina",
		Model:    "jina-reranker-v2",
		APIURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	results, err := client.Rerank(context.Background(), "query", []string{"single doc"})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].RelevanceScore != 0.88 {
		t.Fatalf("unexpected score: %f", results[0].RelevanceScore)
	}
}

func TestRerankGeneric(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := genericRerankResponse{
			Results: []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			}{
				{Index: 0, RelevanceScore: 0.75},
				{Index: 1, RelevanceScore: 0.50},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client, err := NewRerankClient(RerankConfig{
		Provider: "generic",
		APIURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	results, err := client.Rerank(context.Background(), "query", []string{"a", "b"})
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestRerankEmptyDocuments(t *testing.T) {
	client, err := NewRerankClient(RerankConfig{
		Provider: "cohere",
		Model:    "rerank-v3.5",
		APIKey:   "key",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	results, err := client.Rerank(context.Background(), "query", nil)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil results for empty input, got %v", results)
	}
}

func TestRerankServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	client, err := NewRerankClient(RerankConfig{
		Provider: "cohere",
		Model:    "rerank-v3.5",
		APIKey:   "key",
		APIURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}

	_, err = client.Rerank(context.Background(), "query", []string{"doc"})
	if err == nil {
		t.Fatal("expected error for server error response")
	}
}

func TestRerankStatus300(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
		w.Write([]byte("redirect"))
	}))
	defer srv.Close()

	client, err := NewRerankClient(RerankConfig{Provider: "cohere", Model: "m", APIKey: "k", APIURL: srv.URL})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.Rerank(context.Background(), "q", []string{"doc"})
	if err == nil {
		t.Fatal("expected error for status 300")
	}
}

func TestRerankClientTimeout(t *testing.T) {
	client, err := NewRerankClient(RerankConfig{Provider: "cohere", Model: "m", APIKey: "k"})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	rp := client.(*rerankProvider)
	if rp.client.Timeout != 30*time.Second {
		t.Fatalf("expected 30s timeout, got %v", rp.client.Timeout)
	}
}
