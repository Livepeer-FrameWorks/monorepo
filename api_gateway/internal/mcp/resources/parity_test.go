package resources

import (
	"testing"
	"time"

	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/globalid"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestProtoToVODAssetInfo_ParityWithGraphQLMapper(t *testing.T) {
	playbackID := "pbk-1"
	sizeBytes := int64(2048)
	durationMs := int32(30000)
	resolution := "1280x720"
	videoCodec := "h264"
	audioCodec := "aac"
	bitrateKbps := int32(1800)
	errorMessage := "none"
	createdAt := time.Date(2026, 2, 10, 10, 11, 12, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 10, 12, 13, 14, 0, time.UTC)
	expiresAt := time.Date(2026, 3, 10, 8, 9, 10, 0, time.UTC)

	input := &pb.VodAssetInfo{
		Id:              "vod-raw-id",
		ArtifactHash:    "",
		Title:           "Demo",
		Description:     "Parity fixture",
		Filename:        "demo.mp4",
		Status:          pb.VodStatus_VOD_STATUS_READY,
		StorageLocation: "s3",
		SizeBytes:       &sizeBytes,
		DurationMs:      &durationMs,
		Resolution:      &resolution,
		VideoCodec:      &videoCodec,
		AudioCodec:      &audioCodec,
		BitrateKbps:     &bitrateKbps,
		CreatedAt:       timestamppb.New(createdAt),
		UpdatedAt:       timestamppb.New(updatedAt),
		ExpiresAt:       timestamppb.New(expiresAt),
		ErrorMessage:    &errorMessage,
		PlaybackId:      &playbackID,
	}

	mcpMapped := protoToVODAssetInfo(input)
	gqlMapped := resolvers.ProtoToVodAssetForParity(input)

	if gqlMapped == nil {
		t.Fatal("GraphQL mapper returned nil")
	}
	if mcpMapped.ID != gqlMapped.ID {
		t.Fatalf("ID mismatch: MCP=%q GraphQL=%q", mcpMapped.ID, gqlMapped.ID)
	}
	if mcpMapped.ArtifactHash != gqlMapped.ArtifactHash {
		t.Fatalf("ArtifactHash mismatch: MCP=%q GraphQL=%q", mcpMapped.ArtifactHash, gqlMapped.ArtifactHash)
	}
	if mcpMapped.PlaybackID != gqlMapped.PlaybackID {
		t.Fatalf("PlaybackID mismatch: MCP=%q GraphQL=%q", mcpMapped.PlaybackID, gqlMapped.PlaybackID)
	}
	if mcpMapped.Status != string(gqlMapped.Status) {
		t.Fatalf("Status mismatch: MCP=%q GraphQL=%q", mcpMapped.Status, gqlMapped.Status)
	}
	if mcpMapped.Title == nil || gqlMapped.Title == nil || *mcpMapped.Title != *gqlMapped.Title {
		t.Fatalf("Title mismatch: MCP=%v GraphQL=%v", mcpMapped.Title, gqlMapped.Title)
	}
	if mcpMapped.Description == nil || gqlMapped.Description == nil || *mcpMapped.Description != *gqlMapped.Description {
		t.Fatalf("Description mismatch: MCP=%v GraphQL=%v", mcpMapped.Description, gqlMapped.Description)
	}
	if mcpMapped.Filename == nil || gqlMapped.Filename == nil || *mcpMapped.Filename != *gqlMapped.Filename {
		t.Fatalf("Filename mismatch: MCP=%v GraphQL=%v", mcpMapped.Filename, gqlMapped.Filename)
	}
	if mcpMapped.SizeBytes == nil || gqlMapped.SizeBytes == nil || float64(*mcpMapped.SizeBytes) != *gqlMapped.SizeBytes {
		t.Fatalf("SizeBytes mismatch: MCP=%v GraphQL=%v", mcpMapped.SizeBytes, gqlMapped.SizeBytes)
	}
	if mcpMapped.DurationMs == nil || gqlMapped.DurationMs == nil || *mcpMapped.DurationMs != *gqlMapped.DurationMs {
		t.Fatalf("DurationMs mismatch: MCP=%v GraphQL=%v", mcpMapped.DurationMs, gqlMapped.DurationMs)
	}
	if mcpMapped.BitrateKbps == nil || gqlMapped.BitrateKbps == nil || *mcpMapped.BitrateKbps != *gqlMapped.BitrateKbps {
		t.Fatalf("BitrateKbps mismatch: MCP=%v GraphQL=%v", mcpMapped.BitrateKbps, gqlMapped.BitrateKbps)
	}
	if mcpMapped.CreatedAt != gqlMapped.CreatedAt.UTC().Format("2006-01-02T15:04:05Z") {
		t.Fatalf("CreatedAt mismatch: MCP=%q GraphQL=%q", mcpMapped.CreatedAt, gqlMapped.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if mcpMapped.UpdatedAt != gqlMapped.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z") {
		t.Fatalf("UpdatedAt mismatch: MCP=%q GraphQL=%q", mcpMapped.UpdatedAt, gqlMapped.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"))
	}
	if mcpMapped.ExpiresAt == nil || gqlMapped.ExpiresAt == nil || *mcpMapped.ExpiresAt != gqlMapped.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z") {
		t.Fatalf("ExpiresAt mismatch: MCP=%v GraphQL=%v", mcpMapped.ExpiresAt, gqlMapped.ExpiresAt)
	}
}

func TestSupportMapperParityWithGraphQLMapper(t *testing.T) {
	createdAt := time.Date(2026, 2, 10, 1, 2, 3, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 11, 4, 5, 6, 0, time.UTC)
	lastCreatedAt := time.Date(2026, 2, 11, 4, 4, 0, 0, time.UTC)

	conv := &pb.DeckhandConversation{
		Id:          "conv-1",
		Subject:     "Parity conversation",
		Status:      pb.ConversationStatus_CONVERSATION_STATUS_OPEN,
		UnreadCount: 5,
		CreatedAt:   timestamppb.New(createdAt),
		UpdatedAt:   timestamppb.New(updatedAt),
		LastMessage: &pb.DeckhandMessage{
			Id:             "msg-1",
			ConversationId: "conv-1",
			Content:        "Try reducing source bitrate",
			Sender:         pb.MessageSender_MESSAGE_SENDER_AGENT,
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

func conversationStatusLabel(status pb.ConversationStatus) string {
	switch status {
	case pb.ConversationStatus_CONVERSATION_STATUS_OPEN:
		return "open"
	case pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED:
		return "resolved"
	case pb.ConversationStatus_CONVERSATION_STATUS_PENDING:
		return "pending"
	default:
		return "unknown"
	}
}

func messageSenderLabel(sender pb.MessageSender) string {
	switch sender {
	case pb.MessageSender_MESSAGE_SENDER_USER:
		return "user"
	case pb.MessageSender_MESSAGE_SENDER_AGENT:
		return "agent"
	default:
		return "unknown"
	}
}
