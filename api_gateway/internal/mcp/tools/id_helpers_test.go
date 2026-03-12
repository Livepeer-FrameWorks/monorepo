package tools

import (
	"context"
	"testing"

	"frameworks/api_gateway/internal/clients"
	"frameworks/pkg/globalid"

	"github.com/google/uuid"
)

func TestDecodeStreamID_Empty(t *testing.T) {
	_, err := decodeStreamID("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if err.Error() != "stream_id is required" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeStreamID_RawID(t *testing.T) {
	id, err := decodeStreamID("stream-abc")
	if err != nil {
		t.Fatal(err)
	}
	if id != "stream-abc" {
		t.Fatalf("expected passthrough, got %q", id)
	}
}

func TestDecodeStreamID_ValidRelayID(t *testing.T) {
	relay := globalid.Encode(globalid.TypeStream, "s1")
	id, err := decodeStreamID(relay)
	if err != nil {
		t.Fatal(err)
	}
	if id != "s1" {
		t.Fatalf("expected %q, got %q", "s1", id)
	}
}

func TestDecodeStreamID_WrongType(t *testing.T) {
	relay := globalid.Encode(globalid.TypeVodAsset, "v1")
	_, err := decodeStreamID(relay)
	if err == nil {
		t.Fatal("expected error for wrong relay ID type")
	}
}

func TestResolveVodIdentifier_Empty(t *testing.T) {
	_, err := resolveVodIdentifier(context.Background(), "", nil)
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if err.Error() != "invalid artifact hash" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveVodIdentifier_RawHash(t *testing.T) {
	hash, err := resolveVodIdentifier(context.Background(), "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "abc123" {
		t.Fatalf("expected passthrough, got %q", hash)
	}
}

func TestResolveVodIdentifier_RelayNonUUID(t *testing.T) {
	relay := globalid.Encode(globalid.TypeVodAsset, "myhash")
	hash, err := resolveVodIdentifier(context.Background(), relay, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hash != "myhash" {
		t.Fatalf("expected %q, got %q", "myhash", hash)
	}
}

func TestResolveVodIdentifier_WrongType(t *testing.T) {
	relay := globalid.Encode(globalid.TypeStream, "s1")
	_, err := resolveVodIdentifier(context.Background(), relay, nil)
	if err == nil {
		t.Fatal("expected error for wrong relay ID type")
	}
}

func TestResolveVodIdentifier_UUIDNoClients(t *testing.T) {
	id := uuid.New().String()
	relay := globalid.Encode(globalid.TypeVodAsset, id)
	_, err := resolveVodIdentifier(context.Background(), relay, nil)
	if err == nil {
		t.Fatal("expected error for nil clients")
	}
	if err.Error() != "VOD resolver unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveVodIdentifier_UUIDNilCommodore(t *testing.T) {
	id := uuid.New().String()
	relay := globalid.Encode(globalid.TypeVodAsset, id)
	_, err := resolveVodIdentifier(context.Background(), relay, &clients.ServiceClients{})
	if err == nil {
		t.Fatal("expected error for nil Commodore client")
	}
	if err.Error() != "VOD resolver unavailable" {
		t.Fatalf("unexpected error: %v", err)
	}
}
