package globalid

import (
	"encoding/base64"
	"testing"
)

func TestEncodeDecode(t *testing.T) {
	tests := []struct {
		name string
		typ  string
		id   string
	}{
		{"stream", TypeStream, "abc123"},
		{"clip", TypeClip, "def456"},
		{"uuid", TypeVodAsset, "550e8400-e29b-41d4-a716-446655440000"},
		{"with special chars", TypeMessage, "foo/bar:baz"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := Encode(tt.typ, tt.id)
			typ, id, ok := Decode(encoded)
			if !ok {
				t.Fatalf("Decode failed for %s", encoded)
			}
			if typ != tt.typ {
				t.Errorf("type mismatch: got %q, want %q", typ, tt.typ)
			}
			if id != tt.id {
				t.Errorf("id mismatch: got %q, want %q", id, tt.id)
			}
		})
	}
}

func TestDecodeInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"not base64", "!!!invalid!!!"},
		{"empty string", ""},
		{"no colon", base64.StdEncoding.EncodeToString([]byte("StreamABC"))},
		{"empty type", base64.StdEncoding.EncodeToString([]byte(":abc"))},
		{"empty id", base64.StdEncoding.EncodeToString([]byte("Stream:"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := Decode(tt.input)
			if ok {
				t.Errorf("Decode should have failed for %q", tt.input)
			}
		})
	}
}

func TestDecodeExpected(t *testing.T) {
	encoded := Encode(TypeStream, "mystream")

	t.Run("matching type", func(t *testing.T) {
		id, err := DecodeExpected(encoded, TypeStream)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "mystream" {
			t.Errorf("got %q, want %q", id, "mystream")
		}
	})

	t.Run("mismatched type", func(t *testing.T) {
		_, err := DecodeExpected(encoded, TypeClip)
		if err == nil {
			t.Error("expected error for type mismatch")
		}
	})

	t.Run("not a global id returns as-is", func(t *testing.T) {
		id, err := DecodeExpected("raw-id-value", TypeStream)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if id != "raw-id-value" {
			t.Errorf("got %q, want %q", id, "raw-id-value")
		}
	})
}

func TestEncodeComposite(t *testing.T) {
	tests := []struct {
		name        string
		typ         string
		parts       []string
		expectParts int
	}{
		{"two parts", TypeViewerSession, []string{"tenant1", "session1"}, 2},
		{"three parts", TypeStreamEvent, []string{"tenant", "stream", "event"}, 3},
		{"single part", TypeCluster, []string{"cluster1"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := EncodeComposite(tt.typ, tt.parts...)
			decoded, err := DecodeCompositeExpected(encoded, tt.typ, tt.expectParts)
			if err != nil {
				t.Fatalf("DecodeCompositeExpected failed: %v", err)
			}
			if len(decoded) != tt.expectParts {
				t.Errorf("got %d parts, want %d", len(decoded), tt.expectParts)
			}
			for i, part := range tt.parts {
				if decoded[i] != part {
					t.Errorf("part %d: got %q, want %q", i, decoded[i], part)
				}
			}
		})
	}
}

func TestDecodeCompositeExpectedErrors(t *testing.T) {
	t.Run("wrong type", func(t *testing.T) {
		encoded := EncodeComposite(TypeStream, "a", "b")
		_, err := DecodeCompositeExpected(encoded, TypeClip, 2)
		if err == nil {
			t.Error("expected error for type mismatch")
		}
	})

	t.Run("wrong part count", func(t *testing.T) {
		encoded := EncodeComposite(TypeStream, "a", "b")
		_, err := DecodeCompositeExpected(encoded, TypeStream, 3)
		if err == nil {
			t.Error("expected error for part count mismatch")
		}
	})
}
