package chat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/pkg/llm"
)

type compactFakeLLMProvider struct {
	response string
	err      error
}

func (f *compactFakeLLMProvider) Complete(_ context.Context, _ []llm.Message, _ []llm.Tool) (llm.Stream, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &compactFakeStream{content: f.response, done: false}, nil
}

type compactFakeStream struct {
	content string
	done    bool
}

func (s *compactFakeStream) Recv() (llm.Chunk, error) {
	if s.done {
		return llm.Chunk{}, io.EOF
	}
	s.done = true
	return llm.Chunk{Content: s.content}, nil
}

func (s *compactFakeStream) Close() error { return nil }

func buildMessages(roles ...string) []llm.Message {
	msgs := make([]llm.Message, len(roles))
	for i, role := range roles {
		content := role + " message " + string(rune('A'+i))
		msgs[i] = llm.Message{Role: role, Content: content}
	}
	return msgs
}

func TestCompactMessagesFitsInBudget(t *testing.T) {
	msgs := buildMessages("system", "user", "assistant")
	result := compactMessages(context.Background(), msgs, 10000, nil)
	if len(result) != 3 {
		t.Errorf("expected 3 messages unchanged, got %d", len(result))
	}
}

func TestCompactMessagesPrunesOldToolMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "tool", Content: strings.Repeat("old tool data ", 100)},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	result := compactMessages(context.Background(), msgs, 10000, nil)
	for _, m := range result {
		if m.Role == "tool" && strings.Contains(m.Content, "old tool data") {
			t.Error("expected old tool message to be pruned")
		}
	}
}

func TestCompactMessagesTier1Summarize(t *testing.T) {
	// Build a conversation that exceeds 80% of a small budget.
	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: strings.Repeat("question one ", 50)},
		{Role: "assistant", Content: strings.Repeat("answer one ", 50)},
		{Role: "user", Content: strings.Repeat("question two ", 50)},
		{Role: "assistant", Content: strings.Repeat("answer two ", 50)},
		{Role: "user", Content: "final question"},
		{Role: "assistant", Content: "final answer"},
		{Role: "user", Content: "last user msg"},
		{Role: "assistant", Content: "last assistant msg"},
	}

	provider := &compactFakeLLMProvider{response: "Summarized history."}
	budget := 80 // very tight budget
	result := compactMessages(context.Background(), msgs, budget, provider)

	// Should have been compacted — fewer messages.
	if len(result) >= len(msgs) {
		t.Errorf("expected compacted messages (fewer than %d), got %d", len(msgs), len(result))
	}
	// System should always be first.
	if result[0].Role != "system" {
		t.Errorf("expected system first, got %s", result[0].Role)
	}
}

func TestCompactMessagesTier3Emergency(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: strings.Repeat("verbose ", 200)},
		{Role: "assistant", Content: strings.Repeat("verbose ", 200)},
		{Role: "user", Content: "last question"},
	}

	// No provider → can't summarize → falls to tier 3.
	budget := 10 // extremely tight
	result := compactMessages(context.Background(), msgs, budget, nil)
	if len(result) != 3 {
		t.Fatalf("expected 3 messages (system + placeholder + last), got %d", len(result))
	}
	if result[0].Role != "system" {
		t.Error("expected system first")
	}
	if !strings.Contains(result[1].Content, "truncated") {
		t.Error("expected truncation notice in emergency tier")
	}
	if result[2].Content != "last question" {
		t.Errorf("expected last message preserved, got %q", result[2].Content)
	}
}

func TestCompactMessagesSummaryFailure(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("big ", 200)},
		{Role: "assistant", Content: strings.Repeat("big ", 200)},
		{Role: "user", Content: "last"},
	}

	provider := &compactFakeLLMProvider{err: errors.New("llm unavailable")}
	budget := 10
	result := compactMessages(context.Background(), msgs, budget, provider)
	// Should fall to tier 3 when summary fails.
	if len(result) != 3 {
		t.Fatalf("expected 3 messages on summary failure, got %d", len(result))
	}
}

func TestPruneOldToolMessages(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "tool", Content: "old tool 1"},
		{Role: "tool", Content: "old tool 2"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "tool", Content: "recent tool"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	result := pruneOldToolMessages(msgs)
	toolCount := 0
	for _, m := range result {
		if m.Role == "tool" {
			toolCount++
		}
	}
	// Old tools (q1's exchange) should be pruned; recent tool (q2's exchange) kept.
	if toolCount != 1 {
		t.Errorf("expected 1 tool message (recent only), got %d", toolCount)
	}
}

func TestEmergencyCompact(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "mid"},
		{Role: "assistant", Content: "mid"},
		{Role: "user", Content: "last"},
	}
	result := emergencyCompact(msgs)
	if len(result) != 3 {
		t.Fatalf("expected 3, got %d", len(result))
	}
	if result[0].Content != "sys" {
		t.Error("expected system preserved")
	}
	if result[2].Content != "last" {
		t.Error("expected last message preserved")
	}
}
