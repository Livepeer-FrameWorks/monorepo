package llm

import (
	"context"
	"encoding/json"
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
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"search\",\"arguments\":\"{\\\"q\\\":\\\"x\\\"}\"}}]}}]}\n\n")
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
		if err == io.EOF {
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
}
