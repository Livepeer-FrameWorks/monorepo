package llm

import (
	"strings"
	"testing"
)

func TestBadChunkSampleTruncation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		size          int
		wantTruncated bool
	}{
		{name: "short kept verbatim", size: 10, wantTruncated: false},
		{name: "exactly at limit kept", size: 256, wantTruncated: false},
		{name: "over limit truncated", size: 257, wantTruncated: true},
		{name: "well over limit truncated", size: 1000, wantTruncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := []byte(strings.Repeat("a", tt.size))
			got := badChunkSample(data)
			hasMarker := strings.Contains(got, "<truncated>")
			if hasMarker != tt.wantTruncated {
				t.Fatalf("size %d: truncated=%v, want %v (got %q)", tt.size, hasMarker, tt.wantTruncated, got)
			}
		})
	}
}

func TestOpenAIMessagesFromToolCallsBranch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		msg           Message
		wantToolCalls int
	}{
		{
			name:          "assistant with tool calls emits them",
			msg:           Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call_1", Name: "search", Arguments: `{"q":"x"}`}}},
			wantToolCalls: 1,
		},
		{
			name:          "assistant without tool calls emits none",
			msg:           Message{Role: "assistant", Content: "hi"},
			wantToolCalls: 0,
		},
		{
			name:          "user with tool calls ignored (wrong role)",
			msg:           Message{Role: "user", Content: "hi", ToolCalls: []ToolCall{{ID: "call_1", Name: "search"}}},
			wantToolCalls: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := openAIMessagesFrom([]Message{tt.msg})
			if len(out) != 1 {
				t.Fatalf("expected 1 message, got %d", len(out))
			}
			if len(out[0].ToolCalls) != tt.wantToolCalls {
				t.Fatalf("tool_calls = %d, want %d", len(out[0].ToolCalls), tt.wantToolCalls)
			}
			if tt.wantToolCalls > 0 {
				if out[0].ToolCalls[0].Type != "function" {
					t.Fatalf("expected type 'function', got %q", out[0].ToolCalls[0].Type)
				}
				if out[0].ToolCalls[0].Function.Name != "search" {
					t.Fatalf("expected function name 'search', got %q", out[0].ToolCalls[0].Function.Name)
				}
			}
		})
	}
}

func TestOpenAIDecoderUsageOnlyChunk(t *testing.T) {
	t.Parallel()
	decode := newOpenAIChunkDecoder()
	// Usage present, zero choices => usage-only chunk.
	chunk, err := decode([]byte(`{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chunk.Usage == nil {
		t.Fatal("expected usage on usage-only chunk")
	}
	if chunk.Usage.InputTokens != 12 || chunk.Usage.OutputTokens != 3 || chunk.Usage.TotalTokens != 15 {
		t.Fatalf("unexpected usage %#v", chunk.Usage)
	}
	if chunk.Content != "" {
		t.Fatalf("expected no content, got %q", chunk.Content)
	}
}

func TestOpenAIDecoderEmptyChoicesNoUsage(t *testing.T) {
	t.Parallel()
	decode := newOpenAIChunkDecoder()
	chunk, err := decode([]byte(`{"choices":[]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chunk.Usage != nil {
		t.Fatalf("expected nil usage, got %#v", chunk.Usage)
	}
	if chunk.Content != "" || len(chunk.ToolCalls) != 0 {
		t.Fatalf("expected empty chunk, got %#v", chunk)
	}
}

func TestOpenAIDecoderAccumulatesToolCallsByIndex(t *testing.T) {
	t.Parallel()
	decode := newOpenAIChunkDecoder()
	// First delta: no id, only index 0 + name. key falls back to index_0.
	if _, err := decode([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"search","arguments":"{\"q\":"}}]}}]}`)); err != nil {
		t.Fatalf("decode 1: %v", err)
	}
	// Second delta: same index, more arguments.
	if _, err := decode([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"abc\"}"}}]}}]}`)); err != nil {
		t.Fatalf("decode 2: %v", err)
	}
	// Finish reason flushes accumulated calls.
	chunk, err := decode([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatalf("decode 3: %v", err)
	}
	if len(chunk.ToolCalls) != 1 {
		t.Fatalf("expected 1 accumulated tool call, got %d", len(chunk.ToolCalls))
	}
	if chunk.ToolCalls[0].Name != "search" {
		t.Fatalf("expected name 'search', got %q", chunk.ToolCalls[0].Name)
	}
	if chunk.ToolCalls[0].Arguments != `{"q":"abc"}` {
		t.Fatalf("expected accumulated args, got %q", chunk.ToolCalls[0].Arguments)
	}
}

func TestOpenAIDecoderEmptyIDDistinctIndicesStayDistinct(t *testing.T) {
	t.Parallel()
	decode := newOpenAIChunkDecoder()
	// Two parallel tool calls, both with empty id but distinct indices. The
	// index fallback key must keep them separate; otherwise they collide.
	if _, err := decode([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"alpha","arguments":"{}"}}]}}]}`)); err != nil {
		t.Fatalf("decode 1: %v", err)
	}
	if _, err := decode([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"name":"beta","arguments":"{}"}}]}}]}`)); err != nil {
		t.Fatalf("decode 2: %v", err)
	}
	chunk, err := decode([]byte(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatalf("decode 3: %v", err)
	}
	if len(chunk.ToolCalls) != 2 {
		t.Fatalf("expected 2 distinct tool calls, got %d", len(chunk.ToolCalls))
	}
	names := map[string]bool{}
	for _, tc := range chunk.ToolCalls {
		names[tc.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Fatalf("expected both alpha and beta, got %v", names)
	}
}

func TestOpenAIDecoderNoToolCallsLeavesChunkClean(t *testing.T) {
	t.Parallel()
	decode := newOpenAIChunkDecoder()
	chunk, err := decode([]byte(`{"choices":[{"delta":{"content":"hello"}}]}`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chunk.Content != "hello" {
		t.Fatalf("expected content 'hello', got %q", chunk.Content)
	}
	if len(chunk.ToolCalls) != 0 {
		t.Fatalf("expected no tool calls, got %d", len(chunk.ToolCalls))
	}
}
