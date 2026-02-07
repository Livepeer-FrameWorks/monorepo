package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RerankClient scores (query, document) pairs for relevance.
type RerankClient interface {
	Rerank(ctx context.Context, query string, documents []string) ([]RerankResult, error)
}

// RerankResult holds the relevance score for a single document.
type RerankResult struct {
	Index          int
	RelevanceScore float64
}

// RerankConfig configures a reranking provider.
type RerankConfig struct {
	Provider string // "cohere", "jina", or "generic"
	Model    string
	APIKey   string
	APIURL   string
}

type rerankProvider struct {
	client   *http.Client
	provider string
	model    string
	apiKey   string
	apiURL   string
}

// NewRerankClient creates a reranking client for the given provider.
func NewRerankClient(cfg RerankConfig) (RerankClient, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		return nil, errors.New("reranker provider is required")
	}

	apiURL := strings.TrimRight(cfg.APIURL, "/")
	switch provider {
	case "cohere":
		if apiURL == "" {
			apiURL = "https://api.cohere.com/v2"
		}
	case "jina":
		if apiURL == "" {
			apiURL = "https://api.jina.ai/v1"
		}
	case "generic":
		if apiURL == "" {
			return nil, errors.New("RERANKER_API_URL is required for generic provider")
		}
	default:
		return nil, fmt.Errorf("unknown reranker provider %q", provider)
	}

	return &rerankProvider{
		client:   &http.Client{Timeout: 30 * time.Second},
		provider: provider,
		model:    cfg.Model,
		apiKey:   cfg.APIKey,
		apiURL:   apiURL,
	}, nil
}

func (p *rerankProvider) Rerank(ctx context.Context, query string, documents []string) ([]RerankResult, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	switch p.provider {
	case "cohere":
		return p.rerankCohere(ctx, query, documents)
	case "jina":
		return p.rerankJina(ctx, query, documents)
	case "generic":
		return p.rerankGeneric(ctx, query, documents)
	default:
		return nil, fmt.Errorf("unsupported reranker provider %q", p.provider)
	}
}

// Cohere v2 rerank API

type cohereRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type cohereRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (p *rerankProvider) rerankCohere(ctx context.Context, query string, documents []string) ([]RerankResult, error) {
	payload, err := json.Marshal(cohereRerankRequest{
		Model:     p.model,
		Query:     query,
		Documents: documents,
	})
	if err != nil {
		return nil, fmt.Errorf("cohere rerank: marshal: %w", err)
	}

	body, err := p.doRerank(ctx, p.apiURL+"/rerank", payload)
	if err != nil {
		return nil, fmt.Errorf("cohere rerank: %w", err)
	}

	var resp cohereRerankResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cohere rerank: decode: %w", err)
	}

	results := make([]RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
	}
	return results, nil
}

// Jina rerank API

type jinaRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type jinaRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (p *rerankProvider) rerankJina(ctx context.Context, query string, documents []string) ([]RerankResult, error) {
	payload, err := json.Marshal(jinaRerankRequest{
		Model:     p.model,
		Query:     query,
		Documents: documents,
	})
	if err != nil {
		return nil, fmt.Errorf("jina rerank: marshal: %w", err)
	}

	body, err := p.doRerank(ctx, p.apiURL+"/rerank", payload)
	if err != nil {
		return nil, fmt.Errorf("jina rerank: %w", err)
	}

	var resp jinaRerankResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("jina rerank: decode: %w", err)
	}

	results := make([]RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
	}
	return results, nil
}

// Generic /v1/rerank (OpenAI-compatible pattern used by Voyage, SiliconFlow, self-hosted models)

type genericRerankRequest struct {
	Model     string   `json:"model,omitempty"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type genericRerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
}

func (p *rerankProvider) rerankGeneric(ctx context.Context, query string, documents []string) ([]RerankResult, error) {
	payload, err := json.Marshal(genericRerankRequest{
		Model:     p.model,
		Query:     query,
		Documents: documents,
	})
	if err != nil {
		return nil, fmt.Errorf("rerank: marshal: %w", err)
	}

	endpoint := p.apiURL + "/rerank"
	body, err := p.doRerank(ctx, endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}

	var resp genericRerankResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("rerank: decode: %w", err)
	}

	results := make([]RerankResult, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = RerankResult{Index: r.Index, RelevanceScore: r.RelevanceScore}
	}
	return results, nil
}

func (p *rerankProvider) doRerank(ctx context.Context, endpoint string, payload []byte) ([]byte, error) {
	resp, err := doWithRetry(ctx, p.client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return io.ReadAll(resp.Body)
}
