package deckhand

import (
	"context"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Close() error
	ListConversations(ctx context.Context, tenantID string, page, perPage int32) (*deckhandpb.ListConversationsResponse, error)
	SearchConversations(ctx context.Context, tenantID, query string, page, perPage int32) (*deckhandpb.SearchConversationsResponse, error)
	GetConversation(ctx context.Context, conversationID string) (*deckhandpb.DeckhandConversation, error)
	CreateConversation(ctx context.Context, subject, initialMessage string, customAttrs map[string]string) (*deckhandpb.DeckhandConversation, error)
	ListMessages(ctx context.Context, conversationID string, page, perPage int32) (*deckhandpb.ListMessagesResponse, error)
	SendMessage(ctx context.Context, conversationID, content string) (*deckhandpb.DeckhandMessage, error)
}

var _ Interface = (*GRPCClient)(nil)
