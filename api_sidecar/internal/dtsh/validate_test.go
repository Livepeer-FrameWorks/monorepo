package dtsh

import (
	"encoding/binary"
	"errors"
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
