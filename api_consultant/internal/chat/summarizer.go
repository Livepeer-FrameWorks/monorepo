package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"frameworks/pkg/llm"
)

const (
	summaryThreshold      = 10
	summaryUpdateInterval = 5
)

const summarizePrompt = `Summarize the following conversation history into a concise paragraph (3-5 sentences).
Focus on: the user's questions and goals, key information discovered, and any decisions made.
Do NOT include greetings, filler, or meta-commentary. Output only the summary paragraph.`

// generateSummary compresses older messages into a compact summary using the LLM.
func generateSummary(ctx context.Context, provider llm.Provider, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, msg := range messages {
		if msg.Role == "tool" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n\n", msg.Role, msg.Content)
	}

	stream, err := provider.Complete(ctx, []llm.Message{
		{Role: "system", Content: summarizePrompt},
		{Role: "user", Content: b.String()},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("summarize: %w", err)
	}

	var result strings.Builder
	for {
		chunk, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			_ = stream.Close()
			return "", fmt.Errorf("summarize stream: %w", err)
		}
		result.WriteString(chunk.Content)
	}
	_ = stream.Close()

	return strings.TrimSpace(result.String()), nil
}
