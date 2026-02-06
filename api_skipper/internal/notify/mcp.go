package notify

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const investigationEventType = "skipper/investigation"

type MCPNotifier struct {
	server    *mcp.Server
	logger    logging.Logger
	eventType string
}

type MCPNotification struct {
	Type      string    `json:"type"`
	Report    Report    `json:"report"`
	SentAt    time.Time `json:"sent_at"`
	TenantID  string    `json:"tenant_id"`
	TenantTag string    `json:"tenant_tag,omitempty"`
}

func NewMCPNotifier(server *mcp.Server, logger logging.Logger) *MCPNotifier {
	return &MCPNotifier{
		server:    server,
		logger:    logger,
		eventType: investigationEventType,
	}
}

func (n *MCPNotifier) Notify(ctx context.Context, report Report) error {
	if n.server == nil {
		n.logger.Warn("MCP notifier not configured, skipping skipper investigation notification")
		return nil
	}

	sentAt := time.Now().UTC()
	if !report.GeneratedAt.IsZero() {
		sentAt = report.GeneratedAt
	}

	payload := MCPNotification{
		Type:      n.eventType,
		Report:    report,
		SentAt:    sentAt,
		TenantID:  report.TenantID,
		TenantTag: report.TenantName,
	}

	type logSession interface {
		Log(ctx context.Context, params *mcp.LoggingMessageParams) error
	}

	var sessions []logSession
	for session := range n.server.Sessions() {
		ls, ok := any(session).(logSession)
		if !ok {
			continue
		}
		sessions = append(sessions, ls)
	}

	if len(sessions) == 0 {
		n.logger.WithField("tenant_id", report.TenantID).Info("No MCP sessions available for skipper investigation notification")
		return nil
	}

	// Safety: Sessions() returns all active sessions with no tenant scoping.
	// Until we can reliably map sessions to tenant IDs, avoid broadcasting tenant data.
	if len(sessions) > 1 {
		n.logger.WithField("tenant_id", report.TenantID).WithField("session_count", len(sessions)).Warn("Skipping MCP notification: multiple sessions present and tenant scoping is not implemented")
		return nil
	}

	if err := sessions[0].Log(ctx, &mcp.LoggingMessageParams{
		Level:  mcp.LoggingLevel("info"),
		Logger: n.eventType,
		Data:   payload,
	}); err != nil {
		return fmt.Errorf("send mcp notification: %w", err)
	}

	return nil
}
