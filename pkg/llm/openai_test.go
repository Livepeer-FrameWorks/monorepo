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

func TestOpenAIProviderStream(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("expected auth header")
		}
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var req openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !req.Stream {
			t.Fatalf("expected stream true")
		}
		if len(req.Tools) != 1 {
			t.Fatalf("expected tools in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")

		send := func(v any) {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			fmt.Fprintf(w, "data: %s\n\n", b)
		}

		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello "}}}})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "world"}}}})
		send(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"id":    "call_1",
						"index": 0,
						"type":  "function",
						"function": map[string]any{
							"name":      "search",
							"arguments": "{\"q\":\"",
						},
					}},
				},
			}},
		})
		send(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"id":    "call_1",
						"index": 0,
						"type":  "function",
						"function": map[string]any{
							"arguments": "x\"}",
						},
					}},
				},
			}},
		})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}})

		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProvider(Config{
		APIURL: server.URL,
		APIKey: "test-key",
		Model:  "gpt-test",
	})

	stream, err := provider.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, []Tool{
		{
			Name:        "search",
			Description: "searches",
			Parameters: map[string]interface{}{
				"type": "object",
			},
		},
	})
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
	if toolCalls[0].Name != "search" {
		t.Fatalf("unexpected tool name %q", toolCalls[0].Name)
	}
	if toolCalls[0].Arguments != "{\"q\":\"x\"}" {
		t.Fatalf("unexpected tool arguments %q", toolCalls[0].Arguments)
	}
}

func TestOpenAIProviderClientTimeout(t *testing.T) {
	p := NewOpenAIProvider(Config{Model: "test"})
	if p.client.Timeout != 60*time.Second {
		t.Fatalf("expected 60s timeout, got %v", p.client.Timeout)
	}
}

func TestOpenAIProviderNoToolsInRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(req.Tools) != 0 {
			t.Fatalf("expected no tools in request, got %d", len(req.Tools))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "ok"}}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
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

func TestOpenAIProviderStatus300(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMultipleChoices)
		w.Write([]byte("redirect"))
	}))
	defer server.Close()

	p := NewOpenAIProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	_, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error for status 300")
	}
}

func TestOpenAIProviderToolCallWithEmptyID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		// Tool call with empty ID — should use index-based fallback key
		send(map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"id":    "",
						"index": 0,
						"type":  "function",
						"function": map[string]any{
							"name":      "test_tool",
							"arguments": "{\"a\":1}",
						},
					}},
				},
			}},
		})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, []Tool{
		{Name: "test_tool", Description: "t", Parameters: map[string]any{"type": "object"}},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	defer stream.Close()

	var toolCalls []ToolCall
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("recv: %v", err)
		}
		toolCalls = append(toolCalls, chunk.ToolCalls...)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls))
	}
	if toolCalls[0].Name != "test_tool" {
		t.Fatalf("unexpected tool name %q", toolCalls[0].Name)
	}
}

func TestOpenAIProviderStreamWithNoToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(v any) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
		// Chunks with empty tool_calls array
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": "hello", "tool_calls": []any{}}}}})
		send(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": " world"}}}})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenAIProvider(Config{APIURL: server.URL, APIKey: "k", Model: "m"})
	stream, err := p.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
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
	if content.String() != "hello world" {
		t.Fatalf("unexpected content %q", content.String())
	}
	if len(toolCalls) != 0 {
		t.Fatalf("expected no tool calls, got %d", len(toolCalls))
	}
}
