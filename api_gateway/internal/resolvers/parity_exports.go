package resolvers

import (
	"frameworks/api_gateway/graph/model"
	pb "frameworks/pkg/proto"
)

// ProtoToVodAssetForParity exposes VOD proto mapping for cross-package parity tests.
func ProtoToVodAssetForParity(p *pb.VodAssetInfo) *model.VodAsset {
	return protoToVodAsset(p)
}

// ProtoConversationToModelForParity exposes conversation mapping for parity tests.
func ProtoConversationToModelForParity(conv *pb.DeckhandConversation) *model.Conversation {
	return protoConversationToModel(conv)
}

// ProtoMessageToModelForParity exposes message mapping for parity tests.
func ProtoMessageToModelForParity(msg *pb.DeckhandMessage) *model.Message {
	return protoMessageToModel(msg)
}
