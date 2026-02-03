package clips

import (
	"strings"
	"testing"
)

func TestGenerateClipHash(t *testing.T) {
	t.Run("generates 32 character hex string", func(t *testing.T) {
		hash, err := GenerateClipHash("stream1", 1000, 5000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(hash) != 32 {
			t.Errorf("hash length = %d, want 32", len(hash))
		}
		if !ValidateClipHash(hash) {
			t.Error("generated hash failed validation")
		}
	})

	t.Run("generates unique hashes", func(t *testing.T) {
		hash1, err1 := GenerateClipHash("stream1", 1000, 5000)
		if err1 != nil {
			t.Fatalf("first hash generation failed: %v", err1)
		}
		hash2, err2 := GenerateClipHash("stream1", 1000, 5000)
		if err2 != nil {
			t.Fatalf("second hash generation failed: %v", err2)
		}
		if hash1 == hash2 {
			t.Error("hashes should differ due to random salt (collision is astronomically unlikely)")
		}
	})
}

func TestValidateClipHash(t *testing.T) {
	tests := []struct {
		name  string
		hash  string
		valid bool
	}{
		{"valid hash", "abcdef0123456789abcdef0123456789", true},
		{"too short", "abcdef0123456789", false},
		{"too long", "abcdef0123456789abcdef0123456789ab", false},
		{"invalid hex", "ghijklmnopqrstuv0123456789abcdef", false},
		{"empty", "", false},
		{"uppercase valid", "ABCDEF0123456789ABCDEF0123456789", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateClipHash(tt.hash); got != tt.valid {
				t.Errorf("ValidateClipHash(%q) = %v, want %v", tt.hash, got, tt.valid)
			}
		})
	}
}

func TestBuildClipStoragePath(t *testing.T) {
	tests := []struct {
		stream string
		hash   string
		format string
		want   string
	}{
		{"mystream", "abc123", "mp4", "clips/mystream/abc123.mp4"},
		{"tenant/stream", "def456", "ts", "clips/tenant/stream/def456.ts"},
	}

	for _, tt := range tests {
		t.Run(tt.stream, func(t *testing.T) {
			got := BuildClipStoragePath(tt.stream, tt.hash, tt.format)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseClipStoragePath(t *testing.T) {
	t.Run("valid path", func(t *testing.T) {
		stream, hash, format, err := ParseClipStoragePath("clips/mystream/abc123.mp4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream != "mystream" {
			t.Errorf("stream = %q, want %q", stream, "mystream")
		}
		if hash != "abc123" {
			t.Errorf("hash = %q, want %q", hash, "abc123")
		}
		if format != "mp4" {
			t.Errorf("format = %q, want %q", format, "mp4")
		}
	})

	t.Run("nested stream name", func(t *testing.T) {
		stream, hash, format, err := ParseClipStoragePath("clips/tenant/stream/abc123.mp4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream != "tenant/stream" {
			t.Errorf("stream = %q, want %q", stream, "tenant/stream")
		}
		if hash != "abc123" {
			t.Errorf("hash = %q, want %q", hash, "abc123")
		}
		if format != "mp4" {
			t.Errorf("format = %q, want %q", format, "mp4")
		}
	})

	t.Run("roundtrip", func(t *testing.T) {
		original := BuildClipStoragePath("mystream", "abc123def456", "ts")
		stream, hash, format, err := ParseClipStoragePath(original)
		if err != nil {
			t.Fatalf("roundtrip failed: %v", err)
		}
		rebuilt := BuildClipStoragePath(stream, hash, format)
		if rebuilt != original {
			t.Errorf("roundtrip mismatch: %q != %q", rebuilt, original)
		}
	})
}

func TestParseClipStoragePathErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"wrong prefix", "videos/mystream/abc.mp4"},
		{"no separator", "clipsabc.mp4"},
		{"no extension", "clips/mystream/abc123"},
		{"too short", "clips"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := ParseClipStoragePath(tt.path)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestDefaultClipStorageConfig(t *testing.T) {
	cfg := DefaultClipStorageConfig()
	if cfg.LocalPath == "" {
		t.Error("LocalPath should not be empty")
	}
	if cfg.DefaultFormat != "mp4" {
		t.Errorf("DefaultFormat = %q, want %q", cfg.DefaultFormat, "mp4")
	}
	if !strings.HasPrefix(cfg.LocalPath, "/") {
		t.Error("LocalPath should be absolute")
	}
}
