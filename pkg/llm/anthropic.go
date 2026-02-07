package llm

import (
	"bufio"
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

type AnthropicProvider struct {
	client    *http.Client
	apiKey    string
	apiURL    string
	model     string
	maxTokens int
}

const defaultAnthropicMaxTokens = 4096

func NewAnthropicProvider(cfg Config) *AnthropicProvider {
	apiURL := strings.TrimRight(cfg.APIURL, "/")
	if apiURL == "" {
		apiURL = "https://api.anthropic.com"
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxTokens
	}
	return &AnthropicProvider{
		client:    &http.Client{Timeout: 60 * time.Second},
		apiKey:    cfg.APIKey,
		apiURL:    apiURL,
		model:     cfg.Model,
		maxTokens: maxTokens,
	}
}

func (p *AnthropicProvider) Complete(ctx context.Context, messages []Message, tools []Tool) (Stream, error) {
	if p.model == "" {
		return nil, errors.New("anthropic model is required")
	}
	reqBody := anthropicRequest{
		Model:     p.model,
		MaxTokens: p.maxTokens,
		Stream:    true,
	}
	reqBody.Messages, reqBody.System = anthropicMessagesFrom(messages)
	if len(tools) > 0 {
		reqBody.Tools = make([]anthropicTool, 0, len(tools))
		for _, tool := range tools {
			reqBody.Tools = append(reqBody.Tools, anthropicTool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.Parameters,
			})
		}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	resp, err := doWithRetry(ctx, p.client, func() (*http.Request, error) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, p.apiURL+"/v1/messages", bytes.NewReader(payload))
		if reqErr != nil {
			return nil, fmt.Errorf("anthropic: create request: %w", reqErr)
		}
		req.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			req.Header.Set("X-API-Key", p.apiKey)
		}
		req.Header.Set("Anthropic-Version", "2023-06-01")
		return req, nil
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: request failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("anthropic: unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	stream := newAnthropicStream(resp)
	return stream, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicEvent struct {
	Type         string                 `json:"type"`
	Index        int                    `json:"index,omitempty"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`
	Delta        *anthropicContentDelta `json:"delta,omitempty"`
	Message      map[string]interface{} `json:"message,omitempty"`
	Error        map[string]interface{} `json:"error,omitempty"`
	Usage        map[string]interface{} `json:"usage,omitempty"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type anthropicContentDelta struct {
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

type anthropicStream struct {
	base       *sseStream
	indexToID  map[int]string
	toolInputs map[string]string
	toolNames  map[string]string
}

func newAnthropicStream(resp *http.Response) Stream {
	stream := &anthropicStream{
		indexToID:  make(map[int]string),
		toolInputs: make(map[string]string),
		toolNames:  make(map[string]string),
	}
	stream.base = &sseStream{
		resp:   resp,
		reader: bufio.NewReader(resp.Body),
		decode: stream.decodeEvent,
	}
	return stream
}

func (s *anthropicStream) Close() error {
	return s.base.Close()
}

func (s *anthropicStream) Recv() (Chunk, error) {
	return s.base.Recv()
}

func (s *anthropicStream) decodeEvent(data []byte) (Chunk, error) {
	var event anthropicEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return Chunk{}, fmt.Errorf("anthropic: decode event: %w", err)
	}
	switch event.Type {
	case "content_block_start":
		if event.ContentBlock == nil {
			return Chunk{}, nil
		}
		if event.ContentBlock.Type == "text" {
			return Chunk{Content: event.ContentBlock.Text}, nil
		}
		if event.ContentBlock.Type == "tool_use" {
			callID := event.ContentBlock.ID
			s.indexToID[event.Index] = callID
			s.toolNames[callID] = event.ContentBlock.Name
			if len(event.ContentBlock.Input) > 0 {
				s.toolInputs[callID] = string(event.ContentBlock.Input)
			}
			return Chunk{
				ToolCalls: []ToolCall{
					{
						ID:        callID,
						Name:      event.ContentBlock.Name,
						Arguments: s.toolInputs[callID],
					},
				},
			}, nil
		}
	case "content_block_delta":
		if event.Delta == nil {
			return Chunk{}, nil
		}
		if event.Delta.Text != "" {
			return Chunk{Content: event.Delta.Text}, nil
		}
		if event.Delta.PartialJSON != "" {
			callID := s.indexToID[event.Index]
			s.toolInputs[callID] = s.toolInputs[callID] + event.Delta.PartialJSON
			return Chunk{
				ToolCalls: []ToolCall{
					{
						ID:        callID,
						Name:      s.toolNames[callID],
						Arguments: s.toolInputs[callID],
					},
				},
			}, nil
		}
	}
	return Chunk{}, nil
}

func anthropicMessagesFrom(messages []Message) ([]anthropicMessage, string) {
	var systemParts []string
	out := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "system" {
			systemParts = append(systemParts, message.Content)
			continue
		}
		content := anthropicContent{
			Type: "text",
			Text: message.Content,
		}
		if message.Role == "tool" {
			content = anthropicContent{
				Type:      "tool_result",
				ToolUseID: message.ToolCallID,
				Content:   message.Content,
			}
		}
		out = append(out, anthropicMessage{
			Role:    message.Role,
			Content: []anthropicContent{content},
		})
	}
	return out, strings.Join(systemParts, "\n")
}
