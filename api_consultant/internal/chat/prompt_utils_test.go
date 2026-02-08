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
