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
