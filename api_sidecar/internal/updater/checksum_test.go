package updater

import (
	"strings"
	"testing"
)

func TestValidateChecksumDigest(t *testing.T) {
	const sha256HexLen = 64

	tests := []struct {
		name     string
		expected string
		hexLen   int
		wantErr  bool
	}{
		{"valid_sha256", strings.Repeat("a", 64), sha256HexLen, false},
		{"valid_sha256_uppercase", strings.Repeat("AB", 32), sha256HexLen, false},
		{"trims_whitespace", "  " + strings.Repeat("a", 64) + "\n", sha256HexLen, false},
		{"valid_sha512", strings.Repeat("a", 128), 128, false},
		{"too_short", "abc", sha256HexLen, true},
		{"too_long", strings.Repeat("a", 65), sha256HexLen, true},
		{"empty", "", sha256HexLen, true},
		// Correct length but contains a non-hex rune.
		{"non_hex", strings.Repeat("g", 64), sha256HexLen, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChecksumDigest(tt.expected, tt.hexLen)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateChecksumDigest(%q, %d) err = %v, wantErr %v", tt.expected, tt.hexLen, err, tt.wantErr)
			}
		})
	}
}

// TestChecksumDispatch pins the integrity gate that sits in front of artifact
// installs: algorithm selection, the bare-digest sha256 default, and rejection
// of malformed/unknown inputs.
func TestChecksumDispatch(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"bare_digest_defaults_sha256", strings.Repeat("a", 64), false},
		{"explicit_sha256", "sha256:" + strings.Repeat("a", 64), false},
		{"explicit_sha512", "sha512:" + strings.Repeat("a", 128), false},
		{"case_insensitive_algo", "SHA256:" + strings.Repeat("a", 64), false},
		{"empty_required", "", true},
		{"unknown_algo", "md5:" + strings.Repeat("a", 32), true},
		{"bad_digest_for_algo", "sha256:deadbeef", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, expected, err := checksum(tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checksum(%q) err = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if h == nil {
				t.Fatal("checksum returned nil hash on success")
			}
			if expected == "" {
				t.Fatal("checksum returned empty expected digest on success")
			}
		})
	}
}
