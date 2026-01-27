package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"frameworks/api_ticketing/internal/chatwoot"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ChatwootWebhookPayload represents the webhook payload from Chatwoot
type ChatwootWebhookPayload struct {
	Event            string                 `json:"event"`
	ID               int64                  `json:"id"`
	ConversationID   int64                  `json:"conversation_id,omitempty"`
	InboxID          int64                  `json:"inbox_id,omitempty"`
	AccountID        int64                  `json:"account_id,omitempty"`
	Status           string                 `json:"status,omitempty"`
	Subject          string                 `json:"subject,omitempty"`
	CustomAttributes map[string]interface{} `json:"custom_attributes,omitempty"`
	Sender           *ChatwootSender        `json:"sender,omitempty"`
	Content          string                 `json:"content,omitempty"`
	ContentType      string                 `json:"content_type,omitempty"`
	MessageType      string                 `json:"message_type,omitempty"`
}

// ChatwootSender represents the sender in a Chatwoot webhook
type ChatwootSender struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Type  string `json:"type"` // "user" or "contact"
}

// HandleChatwootWebhook handles incoming webhooks from Chatwoot
func HandleChatwootWebhook(c *gin.Context) {
	body, err := c.GetRawData()
	if err != nil {
		deps.Logger.WithError(err).Warn("Failed to read Chatwoot webhook body")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	// NOTE: Chatwoot does not support webhook signature verification (HMAC).
	// See: https://github.com/chatwoot/chatwoot/issues/9354
	// Security relies on internal Docker network isolation.

	// Restore body for JSON binding
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	var payload ChatwootWebhookPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		deps.Logger.WithError(err).Warn("Failed to parse Chatwoot webhook payload")
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payload"})
		return
	}

	deps.Metrics.WebhooksReceived.WithLabelValues(payload.Event).Inc()

	deps.Logger.WithFields(map[string]interface{}{
		"event":           payload.Event,
		"conversation_id": payload.ConversationID,
	}).Info("Received Chatwoot webhook")

	switch payload.Event {
	case "conversation_created":
		handleConversationCreated(c, payload)
	case "conversation_updated":
		handleConversationUpdated(c, payload)
	case "message_created":
		handleMessageCreated(c, payload)
	case "message_updated":
		handleMessageUpdated(c, payload)
	default:
		// Acknowledge but ignore other events
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
	}
}

// handleConversationCreated enriches a new conversation with tenant context
func handleConversationCreated(c *gin.Context, payload ChatwootWebhookPayload) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()

	// Extract tenant_id from custom attributes
	tenantID, ok := payload.CustomAttributes["tenant_id"].(string)
	if !ok || tenantID == "" {
		deps.Logger.Warn("Conversation created without tenant_id")
		c.JSON(http.StatusOK, gin.H{"status": "no_tenant"})
		return
	}

	deps.Logger.WithFields(map[string]interface{}{
		"conversation_id": payload.ConversationID,
		"tenant_id":       tenantID,
	}).Info("Enriching conversation with tenant context")

	// Fetch tenant info from Quartermaster
	var tenantName string
	var tenantCreatedAt time.Time
	if deps.Quartermaster != nil {
		tenant, err := deps.Quartermaster.GetTenant(ctx, tenantID)
		if err != nil {
			deps.Logger.WithError(err).Warn("Failed to fetch tenant info")
			deps.Metrics.EnrichmentCalls.WithLabelValues("quartermaster", "error").Inc()
		} else if tenant != nil && tenant.Tenant != nil {
			deps.Metrics.EnrichmentCalls.WithLabelValues("quartermaster", "ok").Inc()
			tenantName = tenant.Tenant.Name
			if tenant.Tenant.CreatedAt != nil {
				tenantCreatedAt = tenant.Tenant.CreatedAt.AsTime()
			}
		}
	}

	// Fetch billing info from Purser
	var billingEmail, tierName, billingStatus string
	if deps.Purser != nil {
		billing, err := deps.Purser.GetBillingStatus(ctx, tenantID)
		if err != nil {
			deps.Logger.WithError(err).Warn("Failed to fetch billing info")
			deps.Metrics.EnrichmentCalls.WithLabelValues("purser", "error").Inc()
		} else if billing != nil {
			deps.Metrics.EnrichmentCalls.WithLabelValues("purser", "ok").Inc()
			billingStatus = billing.BillingStatus
			if billing.Subscription != nil {
				billingEmail = billing.Subscription.BillingEmail
			}
			if billing.Tier != nil {
				tierName = billing.Tier.DisplayName
			}
		}
	}

	// Extract page URL from custom attributes
	pageURL, _ := payload.CustomAttributes["page_url"].(string)

	// Format enrichment note
	note := formatEnrichmentNote(tenantID, tenantName, tenantCreatedAt, billingEmail, tierName, billingStatus, pageURL)

	// Post as private note to Chatwoot
	if deps.ChatwootBaseURL != "" && deps.ChatwootToken != "" {
		client := chatwoot.NewClient(chatwoot.Config{
			BaseURL:   deps.ChatwootBaseURL,
			APIToken:  deps.ChatwootToken,
			AccountID: int(payload.AccountID),
			InboxID:   int(payload.InboxID),
		})

		_, err := client.CreateNote(ctx, payload.ConversationID, note)
		if err != nil {
			deps.Logger.WithError(err).Warn("Failed to post enrichment note")
			deps.Metrics.ChatwootAPICalls.WithLabelValues("create_note", "error").Inc()
		} else {
			deps.Metrics.ChatwootAPICalls.WithLabelValues("create_note", "ok").Inc()
		}
	}

	// Broadcast conversation creation via service_events
	if deps.Decklog != nil {
		ml := &pb.MessageLifecycleData{
			EventType:      pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED,
			ConversationId: strconv.FormatInt(payload.ConversationID, 10),
			Timestamp:      time.Now().Unix(),
		}
		if tenantID != "" {
			ml.TenantId = &tenantID
		}

		event := &pb.ServiceEvent{
			EventType: "conversation_created",
			Timestamp: timestamppb.Now(),
			Source:    "deckhand",
			TenantId:  tenantID,
			Payload:   &pb.ServiceEvent_SupportEvent{SupportEvent: ml},
		}
		if err := deps.Decklog.SendServiceEvent(event); err != nil {
			deps.Logger.WithError(err).Warn("Failed to broadcast conversation_created via Decklog")
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "enriched"})
}

// formatEnrichmentNote creates a formatted note with customer context
func formatEnrichmentNote(tenantID, tenantName string, createdAt time.Time, billingEmail, tierName, billingStatus, pageURL string) string {
	note := "## Customer Context\n\n"

	if tenantName != "" {
		note += fmt.Sprintf("**Tenant:** %s\n", tenantName)
	} else {
		note += fmt.Sprintf("**Tenant ID:** %s\n", tenantID)
	}

	if billingEmail != "" {
		note += fmt.Sprintf("**Email:** %s\n", billingEmail)
	}

	if tierName != "" {
		if billingStatus != "" {
			note += fmt.Sprintf("**Plan:** %s (%s)\n", tierName, billingStatus)
		} else {
			note += fmt.Sprintf("**Plan:** %s\n", tierName)
		}
	}

	if !createdAt.IsZero() {
		note += fmt.Sprintf("**Member since:** %s\n", createdAt.Format("Jan 2006"))
	}

	if pageURL != "" {
		note += fmt.Sprintf("**Page:** %s\n", pageURL)
	}

	return note
}

// handleMessageCreated broadcasts new messages for real-time updates
func handleMessageCreated(c *gin.Context, payload ChatwootWebhookPayload) {
	// Only broadcast agent replies to users (not user messages back to agent dashboard)
	if payload.Sender == nil || payload.Sender.Type != "user" {
		// This is an agent message - broadcast to webapp via Decklog → Signalman
		tenantID, _ := payload.CustomAttributes["tenant_id"].(string)
		if tenantID != "" && deps.Decklog != nil {
			msgID := strconv.FormatInt(payload.ID, 10)
			content := payload.Content
			sender := "AGENT"

			ml := &pb.MessageLifecycleData{
				EventType:      pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED,
				ConversationId: strconv.FormatInt(payload.ConversationID, 10),
				MessageId:      &msgID,
				Content:        &content,
				Sender:         &sender,
				Timestamp:      time.Now().Unix(),
			}
			ml.TenantId = &tenantID

			event := &pb.ServiceEvent{
				EventType: "message_received",
				Timestamp: timestamppb.Now(),
				Source:    "deckhand",
				TenantId:  tenantID,
				Payload:   &pb.ServiceEvent_SupportEvent{SupportEvent: ml},
			}

			if err := deps.Decklog.SendServiceEvent(event); err != nil {
				deps.Logger.WithError(err).Warn("Failed to broadcast message via Decklog")
				deps.Metrics.MessagesSent.WithLabelValues("error").Inc()
			} else {
				deps.Logger.WithFields(map[string]interface{}{
					"conversation_id": payload.ConversationID,
					"tenant_id":       tenantID,
					"message_id":      payload.ID,
				}).Debug("Broadcasted agent message via Decklog")
				deps.Metrics.MessagesSent.WithLabelValues("broadcast").Inc()
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleConversationUpdated(c *gin.Context, payload ChatwootWebhookPayload) {
	tenantID, _ := payload.CustomAttributes["tenant_id"].(string)
	if tenantID == "" || deps.Decklog == nil {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
		return
	}

	subject := payload.Subject
	status := payload.Status
	ml := &pb.MessageLifecycleData{
		EventType:      pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED,
		ConversationId: strconv.FormatInt(payload.ConversationID, 10),
		Status:         &status,
		Subject:        &subject,
		Timestamp:      time.Now().Unix(),
	}
	ml.TenantId = &tenantID

	event := &pb.ServiceEvent{
		EventType: "conversation_updated",
		Timestamp: timestamppb.Now(),
		Source:    "deckhand",
		TenantId:  tenantID,
		Payload:   &pb.ServiceEvent_SupportEvent{SupportEvent: ml},
	}

	if err := deps.Decklog.SendServiceEvent(event); err != nil {
		deps.Logger.WithError(err).Warn("Failed to broadcast conversation_updated via Decklog")
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func handleMessageUpdated(c *gin.Context, payload ChatwootWebhookPayload) {
	if payload.Sender == nil || payload.Sender.Type != "user" {
		tenantID, _ := payload.CustomAttributes["tenant_id"].(string)
		if tenantID != "" && deps.Decklog != nil {
			msgID := strconv.FormatInt(payload.ID, 10)
			content := payload.Content
			sender := "AGENT"

			ml := &pb.MessageLifecycleData{
				EventType:      pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED,
				ConversationId: strconv.FormatInt(payload.ConversationID, 10),
				MessageId:      &msgID,
				Content:        &content,
				Sender:         &sender,
				Timestamp:      time.Now().Unix(),
			}
			ml.TenantId = &tenantID

			event := &pb.ServiceEvent{
				EventType: "message_updated",
				Timestamp: timestamppb.Now(),
				Source:    "deckhand",
				TenantId:  tenantID,
				Payload:   &pb.ServiceEvent_SupportEvent{SupportEvent: ml},
			}

			if err := deps.Decklog.SendServiceEvent(event); err != nil {
				deps.Logger.WithError(err).Warn("Failed to broadcast message_updated via Decklog")
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
