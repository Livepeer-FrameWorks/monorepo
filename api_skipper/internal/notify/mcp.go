package notify

import (
	"context"
	"errors"
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

	var errs []error
	sentCount := 0
	for session := range n.server.Sessions() {
		sentCount++
		if err := session.Log(ctx, &mcp.LoggingMessageParams{
			Level:  mcp.LoggingLevel("info"),
			Logger: n.eventType,
			Data:   payload,
		}); err != nil {
			errs = append(errs, fmt.Errorf("send mcp notification: %w", err))
		}
	}

	if sentCount == 0 {
		n.logger.WithField("tenant_id", report.TenantID).Info("No MCP sessions available for skipper investigation notification")
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}
