package resources

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// SupportConversation represents a support conversation for MCP.
type SupportConversation struct {
	ID          string          `json:"id"`
	Subject     string          `json:"subject"`
	Status      string          `json:"status"`
	UnreadCount int             `json:"unread_count"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	LastMessage *SupportMessage `json:"last_message,omitempty"`
}

// SupportMessage represents a message in a conversation.
type SupportMessage struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Sender    string    `json:"sender"`
	CreatedAt time.Time `json:"created_at"`
}

// SupportConversationList represents the list of conversations.
type SupportConversationList struct {
	Conversations []SupportConversation `json:"conversations"`
	TotalCount    int                   `json:"total_count"`
}

// SupportConversationDetail represents a conversation with messages.
type SupportConversationDetail struct {
	Conversation SupportConversation `json:"conversation"`
	Messages     []SupportMessage    `json:"messages"`
}

// conversationIDPattern matches support://conversations/{id}
var conversationIDPattern = regexp.MustCompile(`^support://conversations/([^/]+)$`)

// RegisterSupportResources registers support-related MCP resources.
func RegisterSupportResources(server *mcp.Server, serviceClients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// List all conversations
	server.AddResource(&mcp.Resource{
		URI:         "support://conversations",
		Name:        "Support Conversations",
		Description: "List of your support conversations. Use this to find past discussions about issues.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleListConversations(ctx, serviceClients, logger)
	})

	// Single conversation with messages - uses template
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "support://conversations/{conversation_id}",
		Name:        "Support Conversation Detail",
		Description: "Full conversation with all messages. Use for reviewing past troubleshooting sessions.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		matches := conversationIDPattern.FindStringSubmatch(req.Params.URI)
		if len(matches) < 2 {
			return nil, fmt.Errorf("invalid conversation URI: %s", req.Params.URI)
		}
		conversationID := matches[1]
		return handleGetConversation(ctx, conversationID, serviceClients, logger)
	})
}

func handleListConversations(ctx context.Context, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	resp, err := serviceClients.Deckhand.ListConversations(ctx, tenantID, 1, 50)
	if err != nil {
		logger.WithError(err).Warn("Failed to list conversations from Deckhand")
		return nil, fmt.Errorf("failed to fetch conversations: %w", err)
	}

	conversations := make([]SupportConversation, 0, len(resp.Conversations))
	for _, conv := range resp.Conversations {
		sc := convertConversation(conv)
		conversations = append(conversations, sc)
	}

	result := SupportConversationList{
		Conversations: conversations,
		TotalCount:    int(resp.TotalCount),
	}

	return marshalResourceResult("support://conversations", result)
}

func handleGetConversation(ctx context.Context, conversationID string, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	conv, err := serviceClients.Deckhand.GetConversation(ctx, conversationID)
	if err != nil {
		logger.WithError(err).WithField("conversation_id", conversationID).Warn("Failed to get conversation")
		return nil, fmt.Errorf("failed to fetch conversation: %w", err)
	}

	msgs, err := serviceClients.Deckhand.ListMessages(ctx, conversationID, 1, 100)
	if err != nil {
		logger.WithError(err).WithField("conversation_id", conversationID).Warn("Failed to list messages")
		return nil, fmt.Errorf("failed to fetch messages: %w", err)
	}

	messages := make([]SupportMessage, 0, len(msgs.Messages))
	for _, msg := range msgs.Messages {
		messages = append(messages, convertMessage(msg))
	}

	result := SupportConversationDetail{
		Conversation: convertConversation(conv),
		Messages:     messages,
	}

	uri := fmt.Sprintf("support://conversations/%s", conversationID)
	return marshalResourceResult(uri, result)
}

func convertConversation(conv *pb.DeckhandConversation) SupportConversation {
	sc := SupportConversation{
		ID:          conv.Id,
		Subject:     conv.Subject,
		UnreadCount: int(conv.UnreadCount),
	}

	switch conv.Status {
	case pb.ConversationStatus_CONVERSATION_STATUS_OPEN:
		sc.Status = "open"
	case pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED:
		sc.Status = "resolved"
	case pb.ConversationStatus_CONVERSATION_STATUS_PENDING:
		sc.Status = "pending"
	default:
		sc.Status = "unknown"
	}

	if conv.CreatedAt != nil {
		sc.CreatedAt = conv.CreatedAt.AsTime()
	}
	if conv.UpdatedAt != nil {
		sc.UpdatedAt = conv.UpdatedAt.AsTime()
	}
	if conv.LastMessage != nil {
		msg := convertMessage(conv.LastMessage)
		sc.LastMessage = &msg
	}

	return sc
}

func convertMessage(msg *pb.DeckhandMessage) SupportMessage {
	sm := SupportMessage{
		ID:      msg.Id,
		Content: msg.Content,
		Sender:  convertSender(msg.Sender),
	}

	if msg.CreatedAt != nil {
		sm.CreatedAt = msg.CreatedAt.AsTime()
	}

	return sm
}

func convertSender(sender pb.MessageSender) string {
	switch sender {
	case pb.MessageSender_MESSAGE_SENDER_USER:
		return "user"
	case pb.MessageSender_MESSAGE_SENDER_AGENT:
		return "agent"
	default:
		return "unknown"
	}
}
