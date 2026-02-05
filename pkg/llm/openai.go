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
	client *http.Client
	apiKey string
	apiURL string
	model  string
}

func NewOpenAIProvider(cfg Config) *OpenAIProvider {
	apiURL := strings.TrimRight(cfg.APIURL, "/")
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1"
	}
	return &OpenAIProvider{
		client: &http.Client{Timeout: 60 * time.Second},
		apiKey: cfg.APIKey,
		apiURL: apiURL,
		model:  cfg.Model,
	}
}

func (p *OpenAIProvider) Complete(ctx context.Context, messages []Message, tools []Tool) (Stream, error) {
	if p.model == "" {
		return nil, errors.New("openai model is required")
	}
	reqBody := openAIRequest{
		Model:    p.model,
		Messages: messages,
		Stream:   true,
	}
	if len(tools) > 0 {
		reqBody.Tools = make([]openAITool, 0, len(tools))
		for _, tool := range tools {
			reqBody.Tools = append(reqBody.Tools, openAITool{
				Type: "function",
				Function: openAIFunction{
					Name:        tool.Name,
					Description: tool.Description,
					Parameters:  tool.Parameters,
				},
			})
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("openai: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openai: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai: request failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return newSSEStream(resp, decodeOpenAIChunk), nil
}

type openAIRequest struct {
	Model    string       `json:"model"`
	Messages []Message    `json:"messages"`
	Stream   bool         `json:"stream"`
	Tools    []openAITool `json:"tools,omitempty"`
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
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func decodeOpenAIChunk(data []byte) (Chunk, error) {
	var payload openAIStreamResponse
	if err := json.Unmarshal(data, &payload); err != nil {
		return Chunk{}, fmt.Errorf("openai: decode chunk: %w", err)
	}
	if len(payload.Choices) == 0 {
		return Chunk{}, nil
	}
	delta := payload.Choices[0].Delta
	chunk := Chunk{Content: delta.Content}
	if len(delta.ToolCalls) > 0 {
		chunk.ToolCalls = make([]ToolCall, 0, len(delta.ToolCalls))
		for _, call := range delta.ToolCalls {
			chunk.ToolCalls = append(chunk.ToolCalls, ToolCall{
				ID:        call.ID,
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			})
		}
	}
	return chunk, nil
}
