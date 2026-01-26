package resolvers

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/globalid"
	"frameworks/pkg/middleware"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConversationsConnection returns paginated conversations for the current tenant
func (r *Resolver) ConversationsConnection(ctx context.Context, page *model.ConnectionInput) (*model.ConversationsConnection, error) {
	if r.Clients.Deckhand == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	tenantID := middleware.GetTenantID(ctx)
	if tenantID == "" {
		return &model.ConversationsConnection{
			Edges:      []*model.ConversationEdge{},
			PageInfo:   &model.PageInfo{HasNextPage: false, HasPreviousPage: false},
			TotalCount: 0,
		}, nil
	}

	first := 50
	if page != nil && page.First != nil {
		first = *page.First
	}

	resp, err := r.Clients.Deckhand.ListConversations(ctx, tenantID, 1, int32(first))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list conversations")
		return nil, fmt.Errorf("failed to list conversations")
	}

	edges := make([]*model.ConversationEdge, len(resp.Conversations))
	for i, conv := range resp.Conversations {
		edges[i] = &model.ConversationEdge{
			Node:   protoConversationToModel(conv),
			Cursor: conv.Id,
		}
	}

	return &model.ConversationsConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasNextPage:     len(resp.Conversations) >= first,
			HasPreviousPage: false,
		},
		TotalCount: int(resp.TotalCount),
	}, nil
}

// Conversation returns a single conversation by ID
func (r *Resolver) Conversation(ctx context.Context, id string) (*model.Conversation, error) {
	if r.Clients.Deckhand == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	// Decode global ID to get the conversation ID
	convID := id
	if typ, rawID, ok := globalid.Decode(id); ok && typ == globalid.TypeConversation {
		convID = rawID
	}

	conv, err := r.Clients.Deckhand.GetConversation(ctx, convID)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return nil, nil
		}
		r.Logger.WithError(err).Error("Failed to get conversation")
		return nil, fmt.Errorf("failed to get conversation")
	}

	return protoConversationToModel(conv), nil
}

// MessagesConnection returns paginated messages for a conversation
func (r *Resolver) MessagesConnection(ctx context.Context, conversationID string, page *model.ConnectionInput) (*model.MessagesConnection, error) {
	if r.Clients.Deckhand == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	// Decode global ID if needed
	convID := conversationID
	if typ, rawID, ok := globalid.Decode(conversationID); ok && typ == globalid.TypeConversation {
		convID = rawID
	}

	first := 50
	if page != nil && page.First != nil {
		first = *page.First
	}

	resp, err := r.Clients.Deckhand.ListMessages(ctx, convID, 1, int32(first))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list messages")
		return nil, fmt.Errorf("failed to list messages")
	}

	edges := make([]*model.MessageEdge, len(resp.Messages))
	for i, msg := range resp.Messages {
		edges[i] = &model.MessageEdge{
			Node:   protoMessageToModel(msg),
			Cursor: msg.Id,
		}
	}

	return &model.MessagesConnection{
		Edges: edges,
		PageInfo: &model.PageInfo{
			HasNextPage:     len(resp.Messages) >= first,
			HasPreviousPage: false,
		},
		TotalCount: int(resp.TotalCount),
	}, nil
}

// MessageByID returns a single message by conversation and message ID.
func (r *Resolver) MessageByID(ctx context.Context, conversationID string, messageID string) (*model.Message, error) {
	if r.Clients.Deckhand == nil {
		return nil, fmt.Errorf("messaging not configured")
	}

	convID := conversationID
	if typ, rawID, ok := globalid.Decode(conversationID); ok && typ == globalid.TypeConversation {
		convID = rawID
	}

	const perPage int32 = 100
	page := int32(1)
	totalCount := int32(-1)

	for {
		resp, err := r.Clients.Deckhand.ListMessages(ctx, convID, page, perPage)
		if err != nil {
			r.Logger.WithError(err).Error("Failed to list messages")
			return nil, fmt.Errorf("failed to list messages")
		}

		if totalCount == -1 {
			totalCount = resp.TotalCount
		}

		for _, msg := range resp.Messages {
			if msg.Id == messageID {
				return protoMessageToModel(msg), nil
			}
		}

		if int32(len(resp.Messages)) < perPage {
			break
		}
		if totalCount > 0 && page*perPage >= totalCount {
			break
		}
		page++
	}

	return nil, nil
}

// CreateConversation creates a new support conversation
func (r *Resolver) CreateConversation(ctx context.Context, input model.CreateConversationInput) (model.CreateConversationResult, error) {
	if r.Clients.Deckhand == nil {
		return &model.ValidationError{
			Message: "Messaging not configured",
			Code:    strPtr("MESSAGING_DISABLED"),
		}, nil
	}

	subject := ""
	if input.Subject != nil {
		subject = *input.Subject
	}

	customAttrs := make(map[string]string)
	if input.PageURL != nil {
		customAttrs["page_url"] = *input.PageURL
	}

	conv, err := r.Clients.Deckhand.CreateConversation(ctx, subject, input.Message, customAttrs)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create conversation")
		return &model.ValidationError{
			Message: "Failed to create conversation",
			Code:    strPtr("CREATE_FAILED"),
		}, nil
	}

	return protoConversationToModel(conv), nil
}

// SendMessage sends a message in a conversation
func (r *Resolver) SendMessage(ctx context.Context, input model.SendMessageInput) (model.SendMessageResult, error) {
	if r.Clients.Deckhand == nil {
		return &model.ValidationError{
			Message: "Messaging not configured",
			Code:    strPtr("MESSAGING_DISABLED"),
		}, nil
	}

	// Decode global ID if needed
	convID := input.ConversationID
	if typ, rawID, ok := globalid.Decode(input.ConversationID); ok && typ == globalid.TypeConversation {
		convID = rawID
	}

	msg, err := r.Clients.Deckhand.SendMessage(ctx, convID, input.Content)
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			return &model.NotFoundError{
				Message:      "Conversation not found",
				Code:         strPtr("NOT_FOUND"),
				ResourceType: "Conversation",
				ResourceID:   input.ConversationID,
			}, nil
		}
		r.Logger.WithError(err).Error("Failed to send message")
		return &model.ValidationError{
			Message: "Failed to send message",
			Code:    strPtr("SEND_FAILED"),
		}, nil
	}

	return protoMessageToModel(msg), nil
}

// protoConversationToModel converts a proto Conversation to a GraphQL model
func protoConversationToModel(conv *pb.DeckhandConversation) *model.Conversation {
	result := &model.Conversation{
		ID:          globalid.Encode(globalid.TypeConversation, conv.Id),
		Status:      conv.Status, // Uses proto.ConversationStatus directly
		UnreadCount: int(conv.UnreadCount),
	}

	if conv.Subject != "" {
		result.Subject = &conv.Subject
	}

	// Timestamps
	if conv.CreatedAt != nil {
		result.CreatedAt = conv.CreatedAt.AsTime()
	}
	if conv.UpdatedAt != nil {
		result.UpdatedAt = conv.UpdatedAt.AsTime()
	}

	// Last message
	if conv.LastMessage != nil {
		result.LastMessage = protoMessageToModel(conv.LastMessage)
	}

	return result
}

// protoMessageToModel converts a proto Message to a GraphQL model
func protoMessageToModel(msg *pb.DeckhandMessage) *model.Message {
	result := &model.Message{
		ID:             globalid.EncodeComposite(globalid.TypeMessage, msg.ConversationId, msg.Id),
		ConversationID: globalid.Encode(globalid.TypeConversation, msg.ConversationId),
		Content:        msg.Content,
		Sender:         msg.Sender, // Uses proto.MessageSender directly
	}

	// Timestamp
	if msg.CreatedAt != nil {
		result.CreatedAt = msg.CreatedAt.AsTime()
	} else {
		result.CreatedAt = time.Now()
	}

	return result
}
