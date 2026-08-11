package resources

import (
	"testing"
	"time"

	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSupportMapperParityWithGraphQLMapper(t *testing.T) {
	createdAt := time.Date(2026, 2, 10, 1, 2, 3, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 11, 4, 5, 6, 0, time.UTC)
	lastCreatedAt := time.Date(2026, 2, 11, 4, 4, 0, 0, time.UTC)

	conv := &deckhandpb.DeckhandConversation{
		Id:          "conv-1",
		Subject:     "Parity conversation",
		Status:      deckhandpb.ConversationStatus_CONVERSATION_STATUS_OPEN,
		UnreadCount: 5,
		CreatedAt:   timestamppb.New(createdAt),
		UpdatedAt:   timestamppb.New(updatedAt),
		LastMessage: &deckhandpb.DeckhandMessage{
			Id:             "msg-1",
			ConversationId: "conv-1",
			Content:        "Try reducing source bitrate",
			Sender:         deckhandpb.MessageSender_MESSAGE_SENDER_AGENT,
			CreatedAt:      timestamppb.New(lastCreatedAt),
		},
	}

	mcpConv := convertConversation(conv)
	gqlConv := resolvers.ProtoConversationToModelForParity(conv)

	if gqlConv == nil {
		t.Fatal("GraphQL conversation mapper returned nil")
	}
	typ, rawID, ok := globalid.Decode(gqlConv.ID)
	if !ok || typ != globalid.TypeConversation || rawID != mcpConv.ID {
		t.Fatalf("Conversation ID mismatch: MCP=%q GraphQL=%q (type=%q raw=%q ok=%v)", mcpConv.ID, gqlConv.ID, typ, rawID, ok)
	}
	if mcpConv.Subject == "" || gqlConv.Subject == nil || mcpConv.Subject != *gqlConv.Subject {
		t.Fatalf("Subject mismatch: MCP=%q GraphQL=%v", mcpConv.Subject, gqlConv.Subject)
	}
	if mcpConv.UnreadCount != gqlConv.UnreadCount {
		t.Fatalf("UnreadCount mismatch: MCP=%d GraphQL=%d", mcpConv.UnreadCount, gqlConv.UnreadCount)
	}
	if mcpConv.Status != conversationStatusLabel(gqlConv.Status) {
		t.Fatalf("Status mismatch: MCP=%q GraphQL=%q", mcpConv.Status, conversationStatusLabel(gqlConv.Status))
	}
	if !mcpConv.CreatedAt.Equal(gqlConv.CreatedAt) {
		t.Fatalf("CreatedAt mismatch: MCP=%v GraphQL=%v", mcpConv.CreatedAt, gqlConv.CreatedAt)
	}
	if !mcpConv.UpdatedAt.Equal(gqlConv.UpdatedAt) {
		t.Fatalf("UpdatedAt mismatch: MCP=%v GraphQL=%v", mcpConv.UpdatedAt, gqlConv.UpdatedAt)
	}
	if mcpConv.LastMessage == nil || gqlConv.LastMessage == nil {
		t.Fatalf("LastMessage mismatch: MCP=%v GraphQL=%v", mcpConv.LastMessage, gqlConv.LastMessage)
	}

	msgParts, err := globalid.DecodeCompositeExpected(gqlConv.LastMessage.ID, globalid.TypeMessage, 2)
	if err != nil {
		t.Fatalf("decode GraphQL message ID: %v", err)
	}
	if msgParts[1] != mcpConv.LastMessage.ID {
		t.Fatalf("LastMessage.ID mismatch: MCP=%q GraphQL raw=%q", mcpConv.LastMessage.ID, msgParts[1])
	}
	if mcpConv.LastMessage.Content != gqlConv.LastMessage.Content {
		t.Fatalf("LastMessage.Content mismatch: MCP=%q GraphQL=%q", mcpConv.LastMessage.Content, gqlConv.LastMessage.Content)
	}
	if mcpConv.LastMessage.Sender != messageSenderLabel(gqlConv.LastMessage.Sender) {
		t.Fatalf("LastMessage.Sender mismatch: MCP=%q GraphQL=%q", mcpConv.LastMessage.Sender, messageSenderLabel(gqlConv.LastMessage.Sender))
	}
	if !mcpConv.LastMessage.CreatedAt.Equal(gqlConv.LastMessage.CreatedAt) {
		t.Fatalf("LastMessage.CreatedAt mismatch: MCP=%v GraphQL=%v", mcpConv.LastMessage.CreatedAt, gqlConv.LastMessage.CreatedAt)
	}
}

func conversationStatusLabel(status deckhandpb.ConversationStatus) string {
	switch status {
	case deckhandpb.ConversationStatus_CONVERSATION_STATUS_OPEN:
		return "open"
	case deckhandpb.ConversationStatus_CONVERSATION_STATUS_RESOLVED:
		return "resolved"
	case deckhandpb.ConversationStatus_CONVERSATION_STATUS_PENDING:
		return "pending"
	default:
		return "unknown"
	}
}

func messageSenderLabel(sender deckhandpb.MessageSender) string {
	switch sender {
	case deckhandpb.MessageSender_MESSAGE_SENDER_USER:
		return "user"
	case deckhandpb.MessageSender_MESSAGE_SENDER_AGENT:
		return "agent"
	default:
		return "unknown"
	}
}
