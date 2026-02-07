package skipper

import (
	"context"
	"time"
)

// UsageLogger receives chat usage events. Implementations handle
// platform-specific logging (e.g. Decklog service events). Nil-safe:
// callers should check for nil before invoking.
type UsageLogger interface {
	LogChatUsage(ctx context.Context, event ChatUsageEvent)
}

type ChatUsageEvent struct {
	TenantID       string
	UserID         string
	ConversationID string
	StartedAt      time.Time
	TokensIn       int
	TokensOut      int
	HadError       bool
	UserHash       uint64
	TokenHash      uint64
}
