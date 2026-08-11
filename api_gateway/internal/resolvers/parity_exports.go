package resolvers

import (
	"frameworks/api_gateway/graph/model"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"
)

// ProtoConversationToModelForParity exposes conversation mapping for parity tests.
func ProtoConversationToModelForParity(conv *deckhandpb.DeckhandConversation) *model.Conversation {
	return protoConversationToModel(conv)
}

// ProtoMessageToModelForParity exposes message mapping for parity tests.
func ProtoMessageToModelForParity(msg *deckhandpb.DeckhandMessage) *model.Message {
	return protoMessageToModel(msg)
}
