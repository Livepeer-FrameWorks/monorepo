package chat

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
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

func sanitizeToolProtocolMessages(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			end := i + 1
			resultsByID := make(map[string]struct{}, len(msg.ToolCalls))
			for end < len(messages) && messages[end].Role == "tool" {
				if messages[end].ToolCallID != "" {
					resultsByID[messages[end].ToolCallID] = struct{}{}
				}
				end++
			}
			if hasCompleteToolResults(msg.ToolCalls, resultsByID) {
				result = append(result, msg)
				result = append(result, messages[i+1:end]...)
				i = end - 1
				continue
			}
			msg.ToolCalls = nil
			if strings.TrimSpace(msg.Content) != "" {
				result = append(result, msg)
			}
			continue
		}
		if msg.Role == "tool" {
			result = append(result, llm.Message{
				Role:    "user",
				Content: orphanToolResultText(msg),
			})
			continue
		}
		result = append(result, msg)
	}
	return result
}

func hasCompleteToolResults(calls []llm.ToolCall, resultsByID map[string]struct{}) bool {
	if len(calls) == 0 || len(resultsByID) != len(calls) {
		return false
	}
	for _, call := range calls {
		if call.ID == "" {
			return false
		}
		if _, ok := resultsByID[call.ID]; !ok {
			return false
		}
	}
	return true
}

func orphanToolResultText(msg llm.Message) string {
	var parts []string
	if msg.Name != "" {
		parts = append(parts, "tool="+msg.Name)
	}
	if msg.ToolCallID != "" {
		parts = append(parts, "tool_call_id="+msg.ToolCallID)
	}
	header := "Previous tool result was detached from its tool request"
	if len(parts) > 0 {
		header += " (" + strings.Join(parts, ", ") + ")"
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return "[" + header + ".]"
	}
	return "[" + header + ":]\n" + content
}

// summarizeMiddle generates a summary of the middle messages and returns
// [system, summary_injection, ...tail]. The tail always includes the current
// user request and any active tool-call/tool-result block, even if that keeps
// more than keepLast messages.
func summarizeMiddle(ctx context.Context, messages []llm.Message, keepLast int, provider llm.Provider) []llm.Message {
	if len(messages) <= keepLast+1 {
		return messages
	}

	system := messages[0]
	tailStart := compactionTailStart(messages, keepLast)
	tail := messages[tailStart:]
	middle := messages[1:tailStart]

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

// emergencyCompact keeps system plus the irreducible active turn tail.
func emergencyCompact(messages []llm.Message) []llm.Message {
	system := messages[0]
	tailStart := compactionTailStart(messages, 1)
	result := make([]llm.Message, 0, len(messages)-tailStart+1)
	result = append(result, system)
	result = append(result, messages[tailStart:]...)
	return result
}

func compactionTailStart(messages []llm.Message, keepLast int) int {
	if len(messages) <= 1 {
		return 0
	}
	if keepLast < 1 {
		keepLast = 1
	}
	tailStart := len(messages) - keepLast
	if tailStart < 1 {
		tailStart = 1
	}

	if lastUser := lastRoleIndex(messages, "user"); lastUser >= 1 && lastUser < tailStart {
		tailStart = lastUser
	}

	if toolBlockStart := trailingToolBlockStart(messages); toolBlockStart >= 1 && toolBlockStart < tailStart {
		tailStart = toolBlockStart
		if lastUser := lastRoleIndexBefore(messages, "user", toolBlockStart); lastUser >= 1 && lastUser < tailStart {
			tailStart = lastUser
		}
	}

	return tailStart
}

func trailingToolBlockStart(messages []llm.Message) int {
	i := len(messages) - 1
	for i >= 1 && messages[i].Role == "tool" {
		i--
	}
	if i < 1 || i == len(messages)-1 {
		return -1
	}
	if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
		return i
	}
	return -1
}

func lastRoleIndex(messages []llm.Message, role string) int {
	return lastRoleIndexBefore(messages, role, len(messages))
}

func lastRoleIndexBefore(messages []llm.Message, role string, before int) int {
	if before > len(messages) {
		before = len(messages)
	}
	for i := before - 1; i >= 0; i-- {
		if messages[i].Role == role {
			return i
		}
	}
	return -1
}
