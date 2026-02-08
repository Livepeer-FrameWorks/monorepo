package resolvers

import (
	"testing"
	"time"
)

func TestStableCursorRoundTrip(t *testing.T) {
	timestamp := time.Date(2024, time.March, 12, 10, 5, 3, 120000000, time.UTC)
	cursor := encodeStableCursor(timestamp, "event-123")

	decoded, err := decodeStableCursor(cursor)
	if err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if decoded == nil {
		t.Fatal("expected decoded cursor")
	}
	if !decoded.Timestamp.Equal(timestamp) {
		t.Fatalf("expected timestamp %s, got %s", timestamp, decoded.Timestamp)
	}
	if decoded.ID != "event-123" {
		t.Fatalf("expected id event-123, got %q", decoded.ID)
	}
}

func TestStableCursorEmptyReturnsNil(t *testing.T) {
	decoded, err := decodeStableCursor("")
	if err != nil {
		t.Fatalf("unexpected error for empty cursor: %v", err)
	}
	if decoded != nil {
		t.Fatalf("expected nil cursor, got %#v", decoded)
	}
}

func TestStableCursorInvalidReturnsError(t *testing.T) {
	if _, err := decodeStableCursor("not-a-cursor"); err == nil {
		t.Fatal("expected decode error for invalid cursor")
	}
}
