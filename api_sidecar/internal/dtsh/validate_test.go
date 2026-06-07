package dtsh

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateRejectsEmptyTracks(t *testing.T) {
	err := Validate(dtscPacket(packedObject(
		packedMember("version", packedInt(1)),
		packedMember("tracks", packedObject()),
	)))
	if !errors.Is(err, ErrNoTracks) {
		t.Fatalf("error = %v, want ErrNoTracks", err)
	}
}

func TestValidateAcceptsTrackMetadata(t *testing.T) {
	err := Validate(dtscPacket(packedObject(
		packedMember("version", packedInt(1)),
		packedMember("tracks", packedObject(
			packedMember("1", packedObject(
				packedMember("type", packedString("video")),
			)),
		)),
	)))
	if err != nil {
		t.Fatalf("Validate failed: %v", err)
	}
}

func TestValidateRejectsObservedPoisonedSidecar(t *testing.T) {
	poisoned := []byte{
		0x44, 0x54, 0x53, 0x43, 0x00, 0x00, 0x00, 0x44, 0xe0, 0x00, 0x07, 0x76,
		0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x04, 0x00, 0x0e, 0x69, 0x6e, 0x70, 0x75, 0x74, 0x4c, 0x6f,
		0x63, 0x61, 0x6c, 0x56, 0x61, 0x72, 0x73, 0x02, 0x00, 0x00, 0x00, 0x0d,
		0x7b, 0x22, 0x76, 0x65, 0x72, 0x73, 0x69, 0x6f, 0x6e, 0x22, 0x3a, 0x32,
		0x7d, 0x00, 0x06, 0x74, 0x72, 0x61, 0x63, 0x6b, 0x73, 0xe0, 0x00, 0x00,
		0xee, 0x00, 0x00, 0xee,
	}
	err := Validate(poisoned)
	if !errors.Is(err, ErrNoTracks) {
		t.Fatalf("error = %v, want ErrNoTracks", err)
	}
}

// TestValidateMalformedInputs exercises the defensive branches of the DTSC
// parser. Each case is untrusted-input shaped: the parser must reject it with a
// descriptive error rather than reading out of bounds or trusting bad metadata.
func TestValidateMalformedInputs(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		wantSub string // substring expected in the error message
	}{
		{
			name:    "too_small",
			data:    []byte{0x44, 0x54, 0x53, 0x43},
			wantSub: "too small",
		},
		{
			name:    "missing_header",
			data:    []byte{'X', 'X', 'X', 'X', 0x00, 0x00, 0x00, 0x01, 0xe0},
			wantSub: "missing DTSC header",
		},
		{
			name:    "zero_payload_length",
			data:    []byte{'D', 'T', 'S', 'C', 0x00, 0x00, 0x00, 0x00, 0xe0},
			wantSub: "invalid payload length",
		},
		{
			name:    "truncated_payload",
			data:    []byte{'D', 'T', 'S', 'C', 0x00, 0x00, 0x00, 0x10, 0xe0},
			wantSub: "truncated payload",
		},
		{
			name:    "payload_not_object",
			data:    dtscPacket([]byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}),
			wantSub: "not a packed object",
		},
		{
			// String member value whose declared size runs past the payload end.
			name: "string_value_overflow",
			data: dtscPacket(append([]byte{0xe0},
				append(memberKey("x"), 0x02, 0xFF, 0xFF, 0xFF, 0xFF)...)),
			wantSub: "string value overflows",
		},
		{
			// Unknown packed-value type byte must fail closed, not be skipped.
			name: "unsupported_value_type",
			data: dtscPacket(append([]byte{0xe0},
				append(memberKey("x"), 0x07)...)),
			wantSub: "unsupported packed value type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.data)
			if err == nil {
				t.Fatalf("Validate(%s) = nil, want error containing %q", tt.name, tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("Validate(%s) error = %q, want substring %q", tt.name, err, tt.wantSub)
			}
		})
	}
}

// TestValidateSkipsArrayMemberBeforeTracks pins that a packed array sitting
// before the tracks member is walked correctly (skipPackedArray) rather than
// derailing the scan.
func TestValidateSkipsArrayMemberBeforeTracks(t *testing.T) {
	err := Validate(dtscPacket(packedObject(
		packedMember("frags", packedArray(packedInt(1), packedString("a"))),
		packedMember("tracks", packedObject(
			packedMember("1", packedObject(
				packedMember("type", packedString("video")),
			)),
		)),
	)))
	if err != nil {
		t.Fatalf("Validate with array member before tracks failed: %v", err)
	}
}

func TestValidateFile(t *testing.T) {
	valid := dtscPacket(packedObject(
		packedMember("tracks", packedObject(
			packedMember("1", packedObject(
				packedMember("type", packedString("audio")),
			)),
		)),
	))
	path := filepath.Join(t.TempDir(), "valid.dtsh")
	if err := os.WriteFile(path, valid, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := ValidateFile(path); err != nil {
		t.Fatalf("ValidateFile(valid) = %v, want nil", err)
	}

	if err := ValidateFile(filepath.Join(t.TempDir(), "does-not-exist.dtsh")); err == nil {
		t.Fatal("ValidateFile(missing) = nil, want read error")
	}
}

// memberKey encodes the BigEndian uint16 length prefix + key bytes used by
// packed-object members, for hand-crafting malformed values.
func memberKey(name string) []byte {
	out := make([]byte, 2, len(name)+2)
	binary.BigEndian.PutUint16(out, uint16(len(name)))
	return append(out, []byte(name)...)
}

func packedArray(values ...[]byte) []byte {
	out := []byte{0x0a}
	for _, v := range values {
		out = append(out, v...)
	}
	return append(out, 0x00, 0x00, 0xee)
}

func dtscPacket(payload []byte) []byte {
	out := make([]byte, 8, len(payload)+8)
	copy(out, "DTSC")
	binary.BigEndian.PutUint32(out[4:8], uint32(len(payload)))
	return append(out, payload...)
}

func packedObject(members ...[]byte) []byte {
	out := []byte{0xe0}
	for _, member := range members {
		out = append(out, member...)
	}
	return append(out, 0x00, 0x00, 0xee)
}

func packedMember(name string, value []byte) []byte {
	out := make([]byte, 2, len(name)+len(value)+2)
	binary.BigEndian.PutUint16(out, uint16(len(name)))
	out = append(out, []byte(name)...)
	return append(out, value...)
}

func packedInt(v uint64) []byte {
	out := make([]byte, 9)
	out[0] = 0x01
	binary.BigEndian.PutUint64(out[1:], v)
	return out
}

func packedString(v string) []byte {
	out := make([]byte, 5, len(v)+5)
	out[0] = 0x02
	binary.BigEndian.PutUint32(out[1:], uint32(len(v)))
	return append(out, []byte(v)...)
}
