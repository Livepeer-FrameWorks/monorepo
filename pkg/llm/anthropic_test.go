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
