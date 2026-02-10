package chat

import (
	"context"
	"strings"

	"frameworks/pkg/llm"
)

// compactMessages implements tiered context compaction, replacing the naive
// FIFO trim. Returns messages that fit within the token budget.
//
// Tiers:
//
//	0 (Tool Prune)  — always: strip tool-role messages older than last 2 exchanges
//	1 (Normal)      — >80% budget: summarize middle, keep system + summary + last 4
//	2 (Aggressive)  — still >budget: keep system + summary + last 2
//	3 (Emergency)   — >120% or no provider: system + static note + last 1
func compactMessages(ctx context.Context, messages []llm.Message, budget int, provider llm.Provider) []llm.Message {
	if len(messages) <= 2 || budget <= 0 {
		return messages
	}

	// Tier 0: prune old tool messages (keep last 2 user/assistant exchanges).
	messages = pruneOldToolMessages(messages)

	tokens := countTokensInMessages(messages)
	if tokens <= budget {
		return messages
	}

	threshold80 := budget * 80 / 100

	// Tier 1: summarize middle, keep system + summary + last 4 messages.
	if tokens > threshold80 && provider != nil {
		compacted := summarizeMiddle(ctx, messages, 4, provider)
		if countTokensInMessages(compacted) <= budget {
			return compacted
		}
		// Tier 2: same but keep only last 2 messages.
		compacted = summarizeMiddle(ctx, messages, 2, provider)
		if countTokensInMessages(compacted) <= budget {
			return compacted
		}
	}

	// Tier 3: emergency — static placeholder + last 1 message.
	return emergencyCompact(messages)
}

// pruneOldToolMessages removes tool-role messages except those in the last 2
// user/assistant exchanges, to reduce token usage from prior tool rounds.
func pruneOldToolMessages(messages []llm.Message) []llm.Message {
	// Walk backward and find the start of the last 2 user/assistant exchanges.
	// An "exchange" is a user message (possibly followed by tool messages and an assistant reply).
	userCount := 0
	keepFrom := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			userCount++
			if userCount >= 2 {
				keepFrom = i
				break
			}
		}
	}

	result := make([]llm.Message, 0, len(messages))
	for i, msg := range messages {
		if i < keepFrom && msg.Role == "tool" {
			continue
		}
		result = append(result, msg)
	}
	return result
}

// summarizeMiddle generates a summary of the middle messages and returns
// [system, summary_injection, ...last_N].
func summarizeMiddle(ctx context.Context, messages []llm.Message, keepLast int, provider llm.Provider) []llm.Message {
	if len(messages) <= keepLast+1 {
		return messages
	}

	system := messages[0]
	tail := messages[len(messages)-keepLast:]
	middle := messages[1 : len(messages)-keepLast]

	// Convert middle to Message type for generateSummary.
	var chatMsgs []Message
	for _, m := range middle {
		if m.Role == "tool" {
			continue
		}
		chatMsgs = append(chatMsgs, Message{Role: m.Role, Content: m.Content})
	}

	summary, err := generateSummary(ctx, provider, chatMsgs)
	if err != nil || strings.TrimSpace(summary) == "" {
		// Fall through — can't summarize.
		return messages
	}

	result := make([]llm.Message, 0, keepLast+2)
	result = append(result, system)
	result = append(result, llm.Message{
		Role:    "user",
		Content: "[Summary of earlier conversation: " + summary + "]",
	})
	result = append(result, tail...)
	return result
}

// emergencyCompact keeps only system + static note + last 1 message.
func emergencyCompact(messages []llm.Message) []llm.Message {
	system := messages[0]
	last := messages[len(messages)-1]
	return []llm.Message{
		system,
		{
			Role:    "user",
			Content: "[Earlier conversation history was truncated due to context limits. The assistant should ask the user to re-state their question if context is needed.]",
		},
		last,
	}
}
