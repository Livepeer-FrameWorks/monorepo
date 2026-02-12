package resources

import (
	"testing"
	"time"

	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestConvertSender(t *testing.T) {
	tests := []struct {
		name string
		in   pb.MessageSender
		want string
	}{
		{
			name: "user",
			in:   pb.MessageSender_MESSAGE_SENDER_USER,
			want: "user",
		},
		{
			name: "agent",
			in:   pb.MessageSender_MESSAGE_SENDER_AGENT,
			want: "agent",
		},
		{
			name: "unknown fallback",
			in:   pb.MessageSender(999),
			want: "unknown",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := convertSender(tc.in)
			if got != tc.want {
				t.Fatalf("convertSender(%v): got %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConvertMessage(t *testing.T) {
	createdAt := time.Date(2026, 2, 10, 3, 4, 5, 0, time.UTC)

	msg := &pb.DeckhandMessage{
		Id:        "m1",
		Content:   "hello",
		Sender:    pb.MessageSender_MESSAGE_SENDER_AGENT,
		CreatedAt: timestamppb.New(createdAt),
	}

	got := convertMessage(msg)

	if got.ID != "m1" {
		t.Fatalf("ID: got %q, want %q", got.ID, "m1")
	}
	if got.Content != "hello" {
		t.Fatalf("Content: got %q, want %q", got.Content, "hello")
	}
	if got.Sender != "agent" {
		t.Fatalf("Sender: got %q, want %q", got.Sender, "agent")
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt: got %v, want %v", got.CreatedAt, createdAt)
	}
}

func TestConvertConversation(t *testing.T) {
	createdAt := time.Date(2026, 2, 10, 1, 2, 3, 0, time.UTC)
	updatedAt := time.Date(2026, 2, 11, 4, 5, 6, 0, time.UTC)
	lastCreatedAt := time.Date(2026, 2, 11, 4, 4, 0, 0, time.UTC)

	conv := &pb.DeckhandConversation{
		Id:          "c1",
		Subject:     "Playback keeps buffering",
		Status:      pb.ConversationStatus_CONVERSATION_STATUS_PENDING,
		UnreadCount: 3,
		CreatedAt:   timestamppb.New(createdAt),
		UpdatedAt:   timestamppb.New(updatedAt),
		LastMessage: &pb.DeckhandMessage{
			Id:        "m2",
			Content:   "We are checking your cluster status.",
			Sender:    pb.MessageSender_MESSAGE_SENDER_AGENT,
			CreatedAt: timestamppb.New(lastCreatedAt),
		},
	}

	got := convertConversation(conv)

	if got.ID != "c1" {
		t.Fatalf("ID: got %q, want %q", got.ID, "c1")
	}
	if got.Subject != "Playback keeps buffering" {
		t.Fatalf("Subject: got %q, want %q", got.Subject, "Playback keeps buffering")
	}
	if got.Status != "pending" {
		t.Fatalf("Status: got %q, want %q", got.Status, "pending")
	}
	if got.UnreadCount != 3 {
		t.Fatalf("UnreadCount: got %d, want %d", got.UnreadCount, 3)
	}
	if !got.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt: got %v, want %v", got.CreatedAt, createdAt)
	}
	if !got.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdatedAt: got %v, want %v", got.UpdatedAt, updatedAt)
	}
	if got.LastMessage == nil {
		t.Fatal("LastMessage: got nil, want non-nil")
	}
	if got.LastMessage.ID != "m2" {
		t.Fatalf("LastMessage.ID: got %q, want %q", got.LastMessage.ID, "m2")
	}
	if got.LastMessage.Sender != "agent" {
		t.Fatalf("LastMessage.Sender: got %q, want %q", got.LastMessage.Sender, "agent")
	}
}

func TestConvertConversation_UnknownAndNilFields(t *testing.T) {
	conv := &pb.DeckhandConversation{
		Id:          "c2",
		Subject:     "",
		Status:      pb.ConversationStatus(999),
		UnreadCount: 0,
	}

	got := convertConversation(conv)

	if got.Status != "unknown" {
		t.Fatalf("Status: got %q, want %q", got.Status, "unknown")
	}
	if got.LastMessage != nil {
		t.Fatal("LastMessage: got non-nil, want nil")
	}
	if !got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt: got %v, want zero time", got.CreatedAt)
	}
	if !got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt: got %v, want zero time", got.UpdatedAt)
	}
}
