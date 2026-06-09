package dtsh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateFileReturnsReadError(t *testing.T) {
	err := ValidateFile(filepath.Join(t.TempDir(), "missing.dtsh"))
	if err == nil {
		t.Fatal("ValidateFile(missing) = nil, want read error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ValidateFile(missing) error = %v, want os.ErrNotExist", err)
	}
	if strings.Contains(err.Error(), "too small") {
		t.Fatalf("ValidateFile(missing) returned Validate error %q, want os read error", err)
	}
}

func TestValidateTruncatedPayloadReportsHaveBytes(t *testing.T) {
	data := []byte{'D', 'T', 'S', 'C', 0x00, 0x00, 0x00, 0x10, 0xe0}
	err := Validate(data)
	if err == nil {
		t.Fatal("Validate(truncated) = nil, want error")
	}
	if !strings.Contains(err.Error(), "have 1 bytes") {
		t.Fatalf("Validate(truncated) error = %q, want it to report \"have 1 bytes\"", err)
	}
	if !strings.Contains(err.Error(), "need 16") {
		t.Fatalf("Validate(truncated) error = %q, want it to report \"need 16\"", err)
	}
}

func TestValidateSkipsLeadingMembersThenAcceptsTracks(t *testing.T) {
	tracks := packedMember("tracks", packedObject(
		packedMember("1", packedObject(
			packedMember("type", packedString("video")),
		)),
	))
	tests := []struct {
		name    string
		leading []byte
	}{
		{
			name:    "zero_length_string_member",
			leading: packedMember("a", packedString("")),
		},
		{
			name: "nested_object_with_member",
			leading: packedMember("a", packedObject(
				packedMember("b", packedInt(7)),
			)),
		},
		{
			name:    "array_with_values",
			leading: packedMember("a", packedArray(packedInt(1), packedString("x"))),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := append([]byte{0xe0}, tt.leading...)
			payload = append(payload, tracks...)
			payload = append(payload, 0x00, 0x00, 0xee)
			if err := Validate(dtscPacket(payload)); err != nil {
				t.Fatalf("Validate(%s) = %v, want nil", tt.name, err)
			}
		})
	}
}

func TestValidateObjectBoundaryErrors(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr error
		wantSub string
	}{
		{
			name:    "empty_object_only_terminator",
			payload: []byte{0xe0, 0x00, 0x00, 0xee},
			wantErr: ErrNoTracks,
		},
		{
			name:    "object_marker_only",
			payload: []byte{0xe0},
			wantSub: "object missing terminator",
		},
		{
			name:    "key_overflows_payload",
			payload: []byte{0xe0, 0x00, 0x05, 'x'},
			wantSub: "key overflows payload",
		},
		{
			name:    "non_tracks_member_missing_value",
			payload: []byte{0xe0, 0x00, 0x01, 'x'},
			wantSub: "packed value missing",
		},
		{
			name:    "tracks_member_missing_value",
			payload: []byte{0xe0, 0x00, 0x06, 't', 'r', 'a', 'c', 'k', 's'},
			wantSub: "object member value missing",
		},
		{
			name: "tracks_empty_nested_object_at_end",
			payload: []byte{
				0xe0, 0x00, 0x06, 't', 'r', 'a', 'c', 'k', 's',
				0xe0, 0x00, 0x00, 0xee,
			},
			wantErr: ErrNoTracks,
		},
		{
			name: "tracks_nested_object_marker_truncated",
			payload: []byte{
				0xe0, 0x00, 0x06, 't', 'r', 'a', 'c', 'k', 's',
				0xe0,
			},
			wantSub: "nested object truncated",
		},
		{
			name: "numeric_value_complete_then_missing_terminator",
			payload: []byte{
				0xe0, 0x00, 0x01, 'x',
				0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			},
			wantSub: "object missing terminator",
		},
		{
			name: "numeric_value_truncated",
			payload: []byte{
				0xe0, 0x00, 0x01, 'x',
				0x01, 0x00, 0x00, 0x00,
			},
			wantSub: "numeric value truncated",
		},
		{
			name: "string_header_truncated",
			payload: []byte{
				0xe0, 0x00, 0x01, 'x',
				0x02, 0x00, 0x00,
			},
			wantSub: "string header truncated",
		},
		{
			name: "skipped_string_value_overflows",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0x02, 0x00, 0x00, 0x00, 0x0a,
			},
			wantSub: "string value overflows",
		},
		{
			name: "skipped_string_value_exact_fill",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0x02, 0x00, 0x00, 0x00, 0x01, 'b',
			},
			wantSub: "object missing terminator",
		},
		{
			name: "skipped_nested_object_truncated",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0xe0,
			},
			wantSub: "object missing terminator",
		},
		{
			name: "skipped_nested_object_key_overflows",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0xe0, 0x00, 0x05, 'k',
			},
			wantSub: "key overflows payload",
		},
		{
			name: "skipped_nested_object_key_fills_no_value",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0xe0, 0x00, 0x01, 'k',
			},
			wantSub: "packed value missing",
		},
		{
			name: "skipped_array_truncated",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0x0a,
			},
			wantSub: "array missing terminator",
		},
		{
			name: "skipped_array_terminator_exact_end",
			payload: []byte{
				0xe0, 0x00, 0x01, 'a',
				0x0a, 0x00, 0x00, 0xee,
			},
			wantSub: "object missing terminator",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(dtscPacket(tt.payload))
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Validate(%s) error = %v, want %v", tt.name, err, tt.wantErr)
				}
			default:
				if err == nil {
					t.Fatalf("Validate(%s) = nil, want error containing %q", tt.name, tt.wantSub)
				}
				if !strings.Contains(err.Error(), tt.wantSub) {
					t.Fatalf("Validate(%s) error = %q, want substring %q", tt.name, err, tt.wantSub)
				}
			}
		})
	}
}
