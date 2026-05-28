package chat

import (
	"strings"
	"testing"
)

func TestGuardUntrustedContext_TrimsAndLabels(t *testing.T) {
	content := strings.Repeat("token ", maxSummaryTokens+5)
	block := guardUntrustedContext("Summary", content, maxSummaryTokens)
	if !strings.Contains(block, untrustedContextLabel) {
		t.Fatalf("expected untrusted context label, got %q", block)
	}
	parts := strings.SplitN(block, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected header and body, got %q", block)
	}
	if got := len(strings.Fields(parts[1])); got != maxSummaryTokens {
		t.Fatalf("expected %d tokens, got %d", maxSummaryTokens, got)
	}
}

func TestResolvePromptTokenBudget(t *testing.T) {
	if got := ResolvePromptTokenBudget("anthropic", "claude-sonnet-4-5", 12345, 0, 4096); got != 12345 {
		t.Fatalf("explicit budget should win, got %d", got)
	}
	if got := ResolvePromptTokenBudget("openai", "gpt-5", 0, 0, 4096); got != maxDerivedPromptTokenBudget {
		t.Fatalf("expected derived gpt-5 budget capped at %d, got %d", maxDerivedPromptTokenBudget, got)
	}
	if got := ResolvePromptTokenBudget("ollama", "local", 0, 8192, 2048); got != 4096 {
		t.Fatalf("expected configured window-derived budget, got %d", got)
	}
	if got := ResolvePromptTokenBudget("custom", "unknown", 0, 0, 4096); got != defaultPromptTokenBudget {
		t.Fatalf("expected conservative default for unknown provider, got %d", got)
	}
}
