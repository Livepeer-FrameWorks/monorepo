package skipper

import (
	"context"
	"testing"
	"time"

	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"
)

type fakeServiceEventSender struct {
	lastEvent *pb.ServiceEvent
}

func (f *fakeServiceEventSender) SendServiceEvent(event *pb.ServiceEvent) error {
	f.lastEvent = event
	return nil
}

func TestDecklogUsageLoggerPopulatesCorrelationFields(t *testing.T) {
	sender := &fakeServiceEventSender{}
	logger := &DecklogUsageLogger{Client: sender}

	ctx := context.Background()
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenHash, uint64(4242))
	ctx = context.WithValue(ctx, ctxkeys.KeyJWTToken, "jwt-token")

	startedAt := time.Now().Add(-2 * time.Second)
	logger.LogChatUsage(ctx, ChatUsageEvent{
		TenantID:       "tenant-123",
		UserID:         "user-456",
		ConversationID: "conv-789",
		StartedAt:      startedAt,
		TokensIn:       10,
		TokensOut:      20,
		HadError:       true,
	})

	if sender.lastEvent == nil {
		t.Fatal("expected service event to be sent")
	}
	if sender.lastEvent.ResourceType != "skipper_conversation" {
		t.Fatalf("expected resource_type to be skipper_conversation, got %q", sender.lastEvent.ResourceType)
	}
	if sender.lastEvent.ResourceId != "conv-789" {
		t.Fatalf("expected resource_id to be conv-789, got %q", sender.lastEvent.ResourceId)
	}
	if sender.lastEvent.UserId != "user-456" {
		t.Fatalf("expected user_id to be user-456, got %q", sender.lastEvent.UserId)
	}

	batch := sender.lastEvent.GetApiRequestBatch()
	if batch == nil || len(batch.Aggregates) != 1 {
		t.Fatalf("expected one aggregate in batch, got %#v", batch)
	}
	agg := batch.Aggregates[0]
	if agg.ErrorCount != 1 {
		t.Fatalf("expected error_count 1, got %d", agg.ErrorCount)
	}
	if len(agg.UserHashes) != 1 {
		t.Fatalf("expected user_hashes populated, got %#v", agg.UserHashes)
	}
	if len(agg.TokenHashes) != 1 || agg.TokenHashes[0] != 4242 {
		t.Fatalf("expected token_hashes to include 4242, got %#v", agg.TokenHashes)
	}
	expectedUserHash := hashIdentifier("user-456")
	if agg.UserHashes[0] != expectedUserHash {
		t.Fatalf("expected user_hash %d, got %d", expectedUserHash, agg.UserHashes[0])
	}
}

func TestDecklogUsageLoggerPreservesBillingAttribution(t *testing.T) {
	sender := &fakeServiceEventSender{}
	logger := &DecklogUsageLogger{Client: sender}

	startedAt := time.Now().Add(-1 * time.Second)
	logger.LogChatUsage(context.Background(), ChatUsageEvent{
		TenantID:       "tenant-999",
		UserID:         "user-999",
		ConversationID: "conv-999",
		StartedAt:      startedAt,
		TokensIn:       2,
		TokensOut:      3,
		UserHash:       111,
		TokenHash:      222,
	})

	if sender.lastEvent == nil {
		t.Fatal("expected service event to be sent")
	}
	if sender.lastEvent.TenantId != "tenant-999" {
		t.Fatalf("expected tenant-999, got %q", sender.lastEvent.TenantId)
	}
	batch := sender.lastEvent.GetApiRequestBatch()
	if batch == nil || len(batch.Aggregates) != 1 {
		t.Fatalf("expected one aggregate in batch, got %#v", batch)
	}
	agg := batch.Aggregates[0]
	if agg.TenantId != "tenant-999" {
		t.Fatalf("expected aggregate tenant-999, got %q", agg.TenantId)
	}
	if len(agg.UserHashes) != 1 || agg.UserHashes[0] != 111 {
		t.Fatalf("expected user hash 111, got %#v", agg.UserHashes)
	}
	if len(agg.TokenHashes) != 1 || agg.TokenHashes[0] != 222 {
		t.Fatalf("expected token hash 222, got %#v", agg.TokenHashes)
	}
}
