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

type OpenAIProvider struct {
	client    *http.Client
	apiKey    string
	apiURL    string
	model     string
	maxTokens int
}

func NewOpenAIProvider(cfg Config) *OpenAIProvider {
	apiURL := strings.TrimRight(cfg.APIURL, "/")
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		client:    &http.Client{Timeout: 60 * time.Second},
		apiKey:    cfg.APIKey,
		apiURL:    apiURL,
		model:     cfg.Model,
		maxTokens: cfg.MaxTokens,
	}
}

func (p *OpenAIProvider) Complete(ctx context.Context, messages []Message, tools []Tool) (Stream, error) {
	if p.model == "" {
		return nil, errors.New("openai model is required")
	}
	reqBody := openAIRequest{
		Model:     p.model,
		Messages:  messages,
		Stream:    true,
		MaxTokens: p.maxTokens,
	}
	if len(tools) > 0 {
		reqBody.Tools = make([]openAITool, 0, len(tools))
		for _, tool := range tools {
			reqBody.Tools = append(reqBody.Tools, openAITool{
				Type:     "function",
				Function: openAIFunction(tool),
			})
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	resp, err := doWithRetry(ctx, p.client, func() (*http.Request, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/chat/completions", bytes.NewReader(payload))
		if reqErr != nil {
			return nil, fmt.Errorf("openai: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+p.apiKey)
		}
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return newSSEStream(resp, newOpenAIChunkDecoder()), nil
}

type openAIRequest struct {
	Model     string       `json:"model"`
	Messages  []Message    `json:"messages"`
	Stream    bool         `json:"stream"`
	MaxTokens int          `json:"max_tokens,omitempty"`
	Tools     []openAITool `json:"tools,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type openAIStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content   string            `json:"content"`
			ToolCalls []openAIToolCall  `json:"tool_calls"`
			Role      string            `json:"role"`
			Refusal   string            `json:"refusal"`
			Name      string            `json:"name"`
			Metadata  map[string]string `json:"metadata"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Index    int                `json:"index"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func newOpenAIChunkDecoder() func([]byte) (Chunk, error) {
	acc := make(map[string]*ToolCall)

	return func(data []byte) (Chunk, error) {
		var payload openAIStreamResponse
		if err := json.Unmarshal(data, &payload); err != nil {
			return Chunk{}, fmt.Errorf("openai: decode chunk: %w", err)
		}
		if len(payload.Choices) == 0 {
			return Chunk{}, nil
		}

		choice := payload.Choices[0]
		delta := choice.Delta
		chunk := Chunk{Content: delta.Content}

		if len(delta.ToolCalls) > 0 {
			for _, call := range delta.ToolCalls {
				key := call.ID
				if key == "" {
					key = fmt.Sprintf("index_%d", call.Index)
				}
				tc := acc[key]
				if tc == nil {
					tc = &ToolCall{ID: call.ID}
					acc[key] = tc
				}
				if call.Function.Name != "" {
					tc.Name = call.Function.Name
				}
				tc.Arguments += call.Function.Arguments
			}
		}

		if choice.FinishReason != nil && *choice.FinishReason == "tool_calls" {
			chunk.ToolCalls = make([]ToolCall, 0, len(acc))
			for _, tc := range acc {
				chunk.ToolCalls = append(chunk.ToolCalls, *tc)
			}
			// Reset for the next tool-call round.
			clear(acc)
		}

		return chunk, nil
	}
}
