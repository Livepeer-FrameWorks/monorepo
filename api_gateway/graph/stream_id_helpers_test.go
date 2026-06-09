package graph

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
)

func TestEncodeStreamID(t *testing.T) {
	// Empty raw is an error — a Stream node must never get a global ID that
	// decodes to an empty backing ID.
	if _, err := encodeStreamID(""); err == nil {
		t.Error("encodeStreamID(\"\") err = nil, want error")
	}

	got, err := encodeStreamID("stream-123")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	typ, id, ok := globalid.Decode(got)
	if !ok || typ != globalid.TypeStream || id != "stream-123" {
		t.Errorf("encodeStreamID round-trip = (%q,%q,%v), want (Stream, stream-123, true)", typ, id, ok)
	}
}

func TestEncodeStreamIDOptional(t *testing.T) {
	// Empty raw → nil pointer, no error (optional field absent).
	if p, err := encodeStreamIDOptional(""); err != nil || p != nil {
		t.Errorf("encodeStreamIDOptional(\"\") = (%v,%v), want (nil,nil)", p, err)
	}
	p, err := encodeStreamIDOptional("s-1")
	if err != nil || p == nil {
		t.Fatalf("encodeStreamIDOptional(\"s-1\") = (%v,%v), want non-nil ptr", p, err)
	}
	if _, id, ok := globalid.Decode(*p); !ok || id != "s-1" {
		t.Errorf("decoded id = %q (ok=%v), want s-1", id, ok)
	}
}

func TestEncodeStreamIDOptionalPtr(t *testing.T) {
	// Both nil pointer and pointer-to-empty collapse to nil output.
	if p, err := encodeStreamIDOptionalPtr(nil); err != nil || p != nil {
		t.Errorf("encodeStreamIDOptionalPtr(nil) = (%v,%v), want (nil,nil)", p, err)
	}
	empty := ""
	if p, err := encodeStreamIDOptionalPtr(&empty); err != nil || p != nil {
		t.Errorf("encodeStreamIDOptionalPtr(&\"\") = (%v,%v), want (nil,nil)", p, err)
	}
	raw := "s-2"
	p, err := encodeStreamIDOptionalPtr(&raw)
	if err != nil || p == nil {
		t.Fatalf("encodeStreamIDOptionalPtr(&\"s-2\") = (%v,%v), want non-nil ptr", p, err)
	}
	if _, id, ok := globalid.Decode(*p); !ok || id != "s-2" {
		t.Errorf("decoded id = %q (ok=%v), want s-2", id, ok)
	}
}
