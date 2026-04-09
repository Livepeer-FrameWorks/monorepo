package grpc

import (
	"testing"

	"frameworks/api_ticketing/internal/chatwoot"
	pb "frameworks/pkg/proto"
)

func newTestServer() *Server {
	return &Server{}
}

func TestChatwootConvToProto_FullMapping(t *testing.T) {
	s := newTestServer()
	conv := &chatwoot.Conversation{
		ID:             42,
		Status:         "open",
		UnreadCount:    3,
		CreatedAt:      1700000000,
		LastActivityAt: 1700001000,
		CustomAttributes: map[string]string{
			"subject":   "Need help",
			"tenant_id": "t-1",
		},
		Messages: []chatwoot.Message{
			{ID: 1, ConversationID: 42, Content: "Hello", MessageType: chatwoot.MessageTypeIncoming, CreatedAt: 1700000500},
		},
	}

	result := s.chatwootConvToProto(conv)

	if result.Id != "42" {
		t.Fatalf("ID: got %q, want %q", result.Id, "42")
	}
	if result.Status != pb.ConversationStatus_CONVERSATION_STATUS_OPEN {
		t.Fatalf("Status: got %v, want OPEN", result.Status)
	}
	if result.UnreadCount != 3 {
		t.Fatalf("UnreadCount: got %d, want 3", result.UnreadCount)
	}
	if result.Subject != "Need help" {
		t.Fatalf("Subject: got %q, want %q", result.Subject, "Need help")
	}
	if result.CreatedAt == nil {
		t.Fatal("CreatedAt should not be nil")
	}
	if result.UpdatedAt == nil {
		t.Fatal("UpdatedAt (LastActivityAt) should not be nil")
	}
	if result.LastMessage == nil {
		t.Fatal("LastMessage should not be nil")
	}
	if result.LastMessage.Content != "Hello" {
		t.Fatalf("LastMessage.Content: got %q, want %q", result.LastMessage.Content, "Hello")
	}
}

func TestChatwootConvToProto_StatusMapping(t *testing.T) {
	s := newTestServer()
	tests := []struct {
		status string
		want   pb.ConversationStatus
	}{
		{"open", pb.ConversationStatus_CONVERSATION_STATUS_OPEN},
		{"resolved", pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED},
		{"pending", pb.ConversationStatus_CONVERSATION_STATUS_PENDING},
		{"unknown", pb.ConversationStatus_CONVERSATION_STATUS_UNSPECIFIED},
		{"", pb.ConversationStatus_CONVERSATION_STATUS_UNSPECIFIED},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			conv := &chatwoot.Conversation{ID: 1, Status: tc.status}
			result := s.chatwootConvToProto(conv)
			if result.Status != tc.want {
				t.Fatalf("got %v, want %v", result.Status, tc.want)
			}
		})
	}
}

func TestChatwootConvToProto_NoMessages(t *testing.T) {
	s := newTestServer()
	conv := &chatwoot.Conversation{ID: 1}
	result := s.chatwootConvToProto(conv)
	if result.LastMessage != nil {
		t.Fatal("LastMessage should be nil when no messages")
	}
}

func TestChatwootConvToProto_ZeroTimestamps(t *testing.T) {
	s := newTestServer()
	conv := &chatwoot.Conversation{ID: 1, CreatedAt: 0, LastActivityAt: 0}
	result := s.chatwootConvToProto(conv)
	if result.CreatedAt != nil {
		t.Fatal("CreatedAt should be nil when 0")
	}
	if result.UpdatedAt != nil {
		t.Fatal("UpdatedAt should be nil when 0")
	}
}

func TestChatwootMsgToProto_AgentMessage(t *testing.T) {
	s := newTestServer()
	msg := &chatwoot.Message{
		ID:             10,
		ConversationID: 42,
		Content:        "Response from agent",
		MessageType:    chatwoot.MessageTypeOutgoing,
		CreatedAt:      1700000000,
		Sender: &struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		}{ID: 1, Type: "user", Name: "Agent Smith"},
	}

	result := s.chatwootMsgToProto(msg)
	if result.Id != "10" {
		t.Fatalf("ID: got %q, want %q", result.Id, "10")
	}
	if result.ConversationId != "42" {
		t.Fatalf("ConversationId: got %q, want %q", result.ConversationId, "42")
	}
	if result.Sender != pb.MessageSender_MESSAGE_SENDER_AGENT {
		t.Fatalf("Sender: got %v, want AGENT", result.Sender)
	}
	if result.CreatedAt == nil {
		t.Fatal("CreatedAt should not be nil")
	}
}

func TestChatwootMsgToProto_CustomerMessage(t *testing.T) {
	s := newTestServer()
	msg := &chatwoot.Message{
		ID:          20,
		MessageType: chatwoot.MessageTypeIncoming,
		Sender: &struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		}{Type: "contact"},
	}

	result := s.chatwootMsgToProto(msg)
	if result.Sender != pb.MessageSender_MESSAGE_SENDER_USER {
		t.Fatalf("Sender: got %v, want USER", result.Sender)
	}
}

func TestChatwootMsgToProto_SystemActivity(t *testing.T) {
	s := newTestServer()
	msg := &chatwoot.Message{
		ID:          30,
		MessageType: chatwoot.MessageTypeActivity,
		Content:     "Conversation resolved",
	}

	result := s.chatwootMsgToProto(msg)
	if result.Sender != pb.MessageSender_MESSAGE_SENDER_SYSTEM {
		t.Fatalf("Sender: got %v, want SYSTEM", result.Sender)
	}
}

func TestChatwootMsgToProto_NoSender(t *testing.T) {
	s := newTestServer()
	msg := &chatwoot.Message{
		ID:          40,
		MessageType: chatwoot.MessageTypeOutgoing,
		Sender:      nil,
	}

	result := s.chatwootMsgToProto(msg)
	if result.Sender != pb.MessageSender_MESSAGE_SENDER_AGENT {
		t.Fatalf("Sender: got %v, want AGENT (fallback for outgoing without sender)", result.Sender)
	}
}

func TestChatwootMsgToProto_ZeroCreatedAt(t *testing.T) {
	s := newTestServer()
	msg := &chatwoot.Message{ID: 50, CreatedAt: 0}
	result := s.chatwootMsgToProto(msg)
	if result.CreatedAt != nil {
		t.Fatal("CreatedAt should be nil when 0")
	}
}
