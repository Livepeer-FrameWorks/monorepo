package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnthropicProviderStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "test-key" {
			t.Fatalf("expected api key header")
		}
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.System != "system note" {
			t.Fatalf("unexpected system %q", req.System)
		}
		if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
			t.Fatalf("unexpected messages")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"Hello \"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"world\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"search\",\"input\":{\"query\":\"abc\"}}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	provider := NewAnthropicProvider(Config{
		APIURL: server.URL,
		APIKey: "test-key",
		Model:  "claude-test",
	})

	stream, err := provider.Complete(context.Background(), []Message{
		{Role: "system", Content: "system note"},
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()

	var content strings.Builder
	var toolCalls []ToolCall
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		content.WriteString(chunk.Content)
		toolCalls = append(toolCalls, chunk.ToolCalls...)
	}

	if content.String() != "Hello world" {
		t.Fatalf("unexpected content %q", content.String())
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected tool call, got %d", len(toolCalls))
	}
	if !strings.Contains(toolCalls[0].Arguments, "\"query\"") {
		t.Fatalf("unexpected tool args %q", toolCalls[0].Arguments)
	}
}

func TestAnthropicProviderClientTimeout(t *testing.T) {
	p := NewAnthropicProvider(Config{Model: "test"})
	if p.client.Timeout != 60*time.Second {
		t.Fatalf("expected 60s timeout, got %v", p.client.Timeout)
	}
}

func TestAnthropicProviderDefaultMaxTokens(t *testing.T) {
	p := NewAnthropicProvider(Config{Model: "test", MaxTokens: 0})
	if p.maxTokens != defaultAnthropicMaxTokens {
		t.Fatalf("expected default max tokens %d, got %d", defaultAnthropicMaxTokens, p.maxTokens)
	}
	p2 := NewAnthropicProvider(Config{Model: "test", MaxTokens: 1})
	if p2.maxTokens != 1 {
		t.Fatalf("expected max tokens 1, got %d", p2.maxTokens)
	}
}

func TestAnthropicProviderNoToolsInRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(req.Tools) != 0 {
			t.Fatalf("expected no tools in request, got %d", len(req.Tools))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"ok\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewAnthropicProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
	}
}

func TestAnthropicProviderStatus300(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
		w.Write([]byte("redirect"))
	}))
	defer server.Close()

	p := NewAnthropicProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for status 300")
	}
}

func TestAnthropicProviderToolResultMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// Find the tool result message and verify conversion
		foundToolResult := false
		for _, msg := range req.Messages {
			for _, c := range msg.Content {
				if c.Type == "tool_result" {
					foundToolResult = true
					if msg.Role != "user" {
						t.Fatalf("expected tool_result role 'user', got %q", msg.Role)
					}
					if c.ToolUseID != "toolu_1" {
						t.Fatalf("expected tool_use_id toolu_1, got %s", c.ToolUseID)
					}
				}
			}
		}
		if !foundToolResult {
			t.Fatal("expected tool_result content block in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"ok\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewAnthropicProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "search result", ToolCallID: "toolu_1"},
	}, nil)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
	}
}

func TestAnthropicMessagesFromMergesToolResults(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "let me check", ToolCalls: []ToolCall{
			{ID: "toolu_a", Name: "search", Arguments: `{"q":"x"}`},
			{ID: "toolu_b", Name: "lookup", Arguments: `{"id":"1"}`},
		}},
		{Role: "tool", Content: "result A", ToolCallID: "toolu_a"},
		{Role: "tool", Content: "result B", ToolCallID: "toolu_b"},
	}

	out, system := anthropicMessagesFrom(msgs)
	if system != "sys" {
		t.Fatalf("expected system 'sys', got %q", system)
	}
	// user, assistant, user (merged tool results) = 3 messages
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}

	toolResultMsg := out[2]
	if toolResultMsg.Role != "user" {
		t.Fatalf("expected role 'user', got %q", toolResultMsg.Role)
	}
	if len(toolResultMsg.Content) != 2 {
		t.Fatalf("expected 2 tool_result blocks, got %d", len(toolResultMsg.Content))
	}
	for _, c := range toolResultMsg.Content {
		if c.Type != "tool_result" {
			t.Fatalf("expected type 'tool_result', got %q", c.Type)
		}
	}
	if toolResultMsg.Content[0].ToolUseID != "toolu_a" {
		t.Fatalf("expected toolu_a, got %s", toolResultMsg.Content[0].ToolUseID)
	}
	if toolResultMsg.Content[1].ToolUseID != "toolu_b" {
		t.Fatalf("expected toolu_b, got %s", toolResultMsg.Content[1].ToolUseID)
	}
}

func TestAnthropicMessagesFromSingleToolResult(t *testing.T) {
	t.Parallel()
	msgs := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{
			{ID: "toolu_x", Name: "search", Arguments: `{}`},
		}},
		{Role: "tool", Content: "found it", ToolCallID: "toolu_x"},
	}

	out, _ := anthropicMessagesFrom(msgs)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(out))
	}
	if len(out[2].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(out[2].Content))
	}
	if out[2].Content[0].Type != "tool_result" {
		t.Fatalf("expected tool_result, got %s", out[2].Content[0].Type)
	}
}

func TestAnthropicProviderWithToolsInRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(req.Tools) != 1 {
			t.Fatalf("expected 1 tool in request, got %d", len(req.Tools))
		}
		if req.Tools[0].Name != "search" {
			t.Fatalf("expected tool name 'search', got %q", req.Tools[0].Name)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"text\":\"ok\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewAnthropicProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{
		{Name: "search", Description: "searches", Parameters: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
	}
}
