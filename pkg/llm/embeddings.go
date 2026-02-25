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

type EmbeddingClient interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type EmbeddingProvider struct {
	client   *http.Client
	apiKey   string
	apiURL   string
	model    string
	provider string
}

func NewEmbeddingClient(cfg Config) (EmbeddingClient, error) {
	if cfg.Model == "" {
		return nil, errors.New("embedding model is required")
	}
	apiURL := strings.TrimRight(cfg.APIURL, "/")
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1"
	}

	return &EmbeddingProvider{
		client:   &http.Client{Timeout: 120 * time.Second},
		apiKey:   cfg.APIKey,
		apiURL:   apiURL,
		model:    cfg.Model,
		provider: strings.ToLower(cfg.Provider),
	}, nil
}

func (p *EmbeddingProvider) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, errors.New("inputs are required")
	}
	switch p.provider {
	case "ollama":
		return p.embedOllama(ctx, inputs)
	case "openai", "":
		return p.embedOpenAI(ctx, inputs)
	default:
		return nil, fmt.Errorf("embedding provider %q is not supported", p.provider)
	}
}

type openAIEmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (p *EmbeddingProvider) embedOpenAI(ctx context.Context, inputs []string) ([][]float32, error) {
	payload, err := json.Marshal(openAIEmbeddingRequest{Model: p.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("openai embed: marshal request: %w", err)
	}
	url := p.apiURL + "/embeddings"
	return p.postEmbeddings(ctx, url, payload, true, len(inputs))
}

type ollamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type ollamaEmbeddingResponse struct {
	Embedding []float32 `json:"embedding"`
}

func (p *EmbeddingProvider) embedOllama(ctx context.Context, inputs []string) ([][]float32, error) {
	var vectors [][]float32
	for _, input := range inputs {
		payload, err := json.Marshal(ollamaEmbeddingRequest{Model: p.model, Prompt: input})
		if err != nil {
			return nil, fmt.Errorf("ollama embed: marshal request: %w", err)
		}
		url := strings.TrimRight(p.apiURL, "/") + "/api/embeddings"
		respBytes, err := p.postEmbeddings(ctx, url, payload, false, 1)
		if err != nil {
			return nil, err
		}
		vectors = append(vectors, respBytes[0])
	}
	return vectors, nil
}

// ProbeEmbeddingDimensions makes a single embedding call and returns the
// vector length. Use this at startup to discover the model's output dimensions
// without hardcoding a model-to-dimension mapping.
func ProbeEmbeddingDimensions(ctx context.Context, client EmbeddingClient) (int, error) {
	vecs, err := client.Embed(ctx, []string{"dimension probe"})
	if err != nil {
		return 0, fmt.Errorf("probe embedding dimensions: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return 0, errors.New("probe returned empty embedding")
	}
	return len(vecs[0]), nil
}

func (p *EmbeddingProvider) postEmbeddings(ctx context.Context, endpoint string, payload []byte, openAI bool, expected int) ([][]float32, error) {
	resp, err := doWithRetry(ctx, p.client, func() (*http.Request, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if reqErr != nil {
			return nil, fmt.Errorf("create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if openAI {
		var response openAIEmbeddingResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		if len(response.Data) != expected {
			return nil, fmt.Errorf("unexpected embeddings count: %d", len(response.Data))
		}
		vectors := make([][]float32, 0, len(response.Data))
		for _, entry := range response.Data {
			vectors = append(vectors, entry.Embedding)
		}
		return vectors, nil
	}

	var response ollamaEmbeddingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return [][]float32{response.Embedding}, nil
}
