package resolvers

import (
	"frameworks/api_gateway/graph/model"
	deckhandpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/deckhand"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// ProtoToVodAssetForParity exposes VOD proto mapping for cross-package parity tests.
func ProtoToVodAssetForParity(p *sharedpb.VodAssetInfo) *model.VodAsset {
	return protoToVodAsset(p)
}

// ProtoConversationToModelForParity exposes conversation mapping for parity tests.
func ProtoConversationToModelForParity(conv *deckhandpb.DeckhandConversation) *model.Conversation {
	return protoConversationToModel(conv)
}

// ProtoMessageToModelForParity exposes message mapping for parity tests.
func ProtoMessageToModelForParity(msg *deckhandpb.DeckhandMessage) *model.Message {
	return protoMessageToModel(msg)
}
