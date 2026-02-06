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
	manager   *TenantMCPManager
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

func NewMCPNotifier(manager *TenantMCPManager, logger logging.Logger) *MCPNotifier {
	return &MCPNotifier{
		manager:   manager,
		logger:    logger,
		eventType: investigationEventType,
	}
}

func (n *MCPNotifier) Notify(ctx context.Context, report Report) error {
	if n.manager == nil {
		n.logger.Warn("MCP notifier not configured, skipping skipper investigation notification")
		return nil
	}

	sessions := n.manager.SessionsForTenant(report.TenantID)
	if len(sessions) == 0 {
		n.logger.WithField("tenant_id", report.TenantID).Info("No MCP sessions available for skipper investigation notification")
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

	var firstErr error
	for _, session := range sessions {
		if err := session.Log(ctx, &mcp.LoggingMessageParams{
			Level:  mcp.LoggingLevel("info"),
			Logger: n.eventType,
			Data:   payload,
		}); err != nil {
			n.logger.WithError(err).WithField("tenant_id", report.TenantID).Warn("Failed to send MCP notification to session")
			if firstErr == nil {
				firstErr = fmt.Errorf("send mcp notification: %w", err)
			}
		}
	}

	return firstErr
}
