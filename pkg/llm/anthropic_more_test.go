package llm

import (
	"encoding/json"
	"testing"
)

func TestUsageFromMap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  map[string]interface{}
		want *Usage
	}{
		{name: "nil map", raw: nil, want: nil},
		{name: "all zero", raw: map[string]interface{}{"input_tokens": float64(0), "output_tokens": float64(0)}, want: nil},
		{name: "input only", raw: map[string]interface{}{"input_tokens": float64(10)}, want: &Usage{InputTokens: 10, TotalTokens: 10}},
		{name: "output only", raw: map[string]interface{}{"output_tokens": float64(5)}, want: &Usage{OutputTokens: 5, TotalTokens: 5}},
		{name: "both", raw: map[string]interface{}{"input_tokens": float64(10), "output_tokens": float64(5)}, want: &Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usageFromMap(tt.raw)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil usage, got %#v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected %#v, got nil", tt.want)
			}
			if *got != *tt.want {
				t.Fatalf("expected %#v, got %#v", tt.want, got)
			}
		})
	}
}

func TestAnthropicUsageFromEvent(t *testing.T) {
	t.Parallel()
	t.Run("top-level usage wins", func(t *testing.T) {
		event := anthropicEvent{
			Usage:   map[string]interface{}{"input_tokens": float64(3), "output_tokens": float64(4)},
			Message: map[string]interface{}{"usage": map[string]interface{}{"input_tokens": float64(99)}},
		}
		got := anthropicUsageFromEvent(event)
		if got == nil || got.InputTokens != 3 || got.OutputTokens != 4 {
			t.Fatalf("expected top-level usage 3/4, got %#v", got)
		}
	})
	t.Run("falls back to message.usage", func(t *testing.T) {
		event := anthropicEvent{
			Message: map[string]interface{}{"usage": map[string]interface{}{"input_tokens": float64(7), "output_tokens": float64(8)}},
		}
		got := anthropicUsageFromEvent(event)
		if got == nil || got.InputTokens != 7 || got.OutputTokens != 8 {
			t.Fatalf("expected message usage 7/8, got %#v", got)
		}
	})
	t.Run("nil message yields nil", func(t *testing.T) {
		if got := anthropicUsageFromEvent(anthropicEvent{}); got != nil {
			t.Fatalf("expected nil usage when no usage present, got %#v", got)
		}
	})
	t.Run("message without usage key yields nil", func(t *testing.T) {
		event := anthropicEvent{Message: map[string]interface{}{"role": "assistant"}}
		if got := anthropicUsageFromEvent(event); got != nil {
			t.Fatalf("expected nil usage when message lacks usage, got %#v", got)
		}
	})
}

func TestDecodeEventToolUseInputThreshold(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		wantArgs string
	}{
		{name: "empty object placeholder dropped", input: `{}`, wantArgs: ""},
		{name: "real input kept", input: `{"q":"abc"}`, wantArgs: `{"q":"abc"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &anthropicStream{
				indexToID:  make(map[int]string),
				toolInputs: make(map[string]string),
				toolNames:  make(map[string]string),
			}
			raw, _ := json.Marshal(map[string]interface{}{
				"type":  "content_block_start",
				"index": 0,
				"content_block": map[string]interface{}{
					"type":  "tool_use",
					"id":    "toolu_1",
					"name":  "search",
					"input": json.RawMessage(tt.input),
				},
			})
			chunk, err := s.decodeEvent(raw)
			if err != nil {
				t.Fatalf("decodeEvent: %v", err)
			}
			if len(chunk.ToolCalls) != 1 {
				t.Fatalf("expected 1 tool call, got %d", len(chunk.ToolCalls))
			}
			if chunk.ToolCalls[0].Arguments != tt.wantArgs {
				t.Fatalf("input %q: expected args %q, got %q", tt.input, tt.wantArgs, chunk.ToolCalls[0].Arguments)
			}
		})
	}
}

func TestAnthropicMessagesFromToolUseContentComposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		assistantContent string
		toolArgs         string
		wantTextBlock    bool
		wantInput        string
	}{
		{name: "with content prepends text block, empty args default to {}", assistantContent: "thinking", toolArgs: "", wantTextBlock: true, wantInput: "{}"},
		{name: "empty content omits text block, real args preserved", assistantContent: "", toolArgs: `{"q":"x"}`, wantTextBlock: false, wantInput: `{"q":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := anthropicMessagesFrom([]Message{
				{Role: "user", Content: "hi"},
				{Role: "assistant", Content: tt.assistantContent, ToolCalls: []ToolCall{
					{ID: "toolu_1", Name: "search", Arguments: tt.toolArgs},
				}},
				{Role: "tool", Content: "result", ToolCallID: "toolu_1"},
			})
			assistant := out[1]
			if assistant.Role != "assistant" {
				t.Fatalf("expected assistant message at index 1, got %q", assistant.Role)
			}
			var textBlocks, toolUseBlocks int
			var toolInput string
			for _, c := range assistant.Content {
				switch c.Type {
				case "text":
					textBlocks++
				case "tool_use":
					toolUseBlocks++
					toolInput = string(c.Input)
				}
			}
			if (textBlocks > 0) != tt.wantTextBlock {
				t.Fatalf("text block present=%v, want %v (content=%q)", textBlocks > 0, tt.wantTextBlock, tt.assistantContent)
			}
			if toolUseBlocks != 1 {
				t.Fatalf("expected 1 tool_use block, got %d", toolUseBlocks)
			}
			if toolInput != tt.wantInput {
				t.Fatalf("tool input = %q, want %q", toolInput, tt.wantInput)
			}
		})
	}
}

func TestAnthropicMessagesFromAssistantWithoutToolCallsTakesTextPath(t *testing.T) {
	t.Parallel()
	// assistant with zero tool calls must go through the plain-text branch,
	// not the tool-pairing branch.
	out, _ := anthropicMessagesFrom([]Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "plain reply"},
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	if out[1].Role != "assistant" {
		t.Fatalf("expected assistant role, got %q", out[1].Role)
	}
	if len(out[1].Content) != 1 || out[1].Content[0].Type != "text" || out[1].Content[0].Text != "plain reply" {
		t.Fatalf("expected single text content 'plain reply', got %#v", out[1].Content)
	}
}
