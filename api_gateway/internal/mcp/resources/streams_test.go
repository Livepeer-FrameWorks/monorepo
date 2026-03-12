package resources

import (
	"testing"

	"frameworks/pkg/globalid"
)

func TestDecodeStreamIdentifier_Empty(t *testing.T) {
	_, err := decodeStreamIdentifier("")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if err.Error() != "invalid stream ID" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeStreamIdentifier_RawID(t *testing.T) {
	id, err := decodeStreamIdentifier("stream-abc")
	if err != nil {
		t.Fatal(err)
	}
	if id != "stream-abc" {
		t.Fatalf("expected passthrough, got %q", id)
	}
}

func TestDecodeStreamIdentifier_ValidRelayID(t *testing.T) {
	relay := globalid.Encode(globalid.TypeStream, "s1")
	id, err := decodeStreamIdentifier(relay)
	if err != nil {
		t.Fatal(err)
	}
	if id != "s1" {
		t.Fatalf("expected %q, got %q", "s1", id)
	}
}

func TestDecodeStreamIdentifier_WrongType(t *testing.T) {
	relay := globalid.Encode(globalid.TypeVodAsset, "v1")
	_, err := decodeStreamIdentifier(relay)
	if err == nil {
		t.Fatal("expected error for wrong relay ID type")
	}
}
