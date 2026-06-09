package clips

import "testing"

func TestParseClipStoragePathBoundaries(t *testing.T) {
	t.Run("length exactly 7 with clips prefix lacks stream separator", func(t *testing.T) {
		_, _, _, err := ParseClipStoragePath("clips/x")
		if err == nil {
			t.Fatal("expected error")
		}
		if err.Error() != "invalid clip storage path format: no stream separator" {
			t.Fatalf("error = %q, want no-stream-separator error", err.Error())
		}
	})

	t.Run("length exactly 6 is the bare prefix and rejected", func(t *testing.T) {
		_, _, _, err := ParseClipStoragePath("clips/")
		if err == nil {
			t.Fatal("expected error")
		}
		if err.Error() != "invalid clip storage path format" {
			t.Fatalf("error = %q, want format error", err.Error())
		}
	})

	t.Run("valid prefix but no inner slash is rejected", func(t *testing.T) {
		_, _, _, err := ParseClipStoragePath("clips/abc.mp4")
		if err == nil {
			t.Fatal("expected error for missing stream separator")
		}
		if err.Error() != "invalid clip storage path format: no stream separator" {
			t.Fatalf("error = %q, want no-stream-separator error", err.Error())
		}
	})

	t.Run("slash at first remainder index yields empty stream name", func(t *testing.T) {
		stream, hash, format, err := ParseClipStoragePath("clips//abc.mp4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream != "" {
			t.Fatalf("stream = %q, want empty", stream)
		}
		if hash != "abc" {
			t.Fatalf("hash = %q, want %q", hash, "abc")
		}
		if format != "mp4" {
			t.Fatalf("format = %q, want %q", format, "mp4")
		}
	})

	t.Run("dot at first filename index yields empty clip hash", func(t *testing.T) {
		stream, hash, format, err := ParseClipStoragePath("clips/stream/.mp4")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stream != "stream" {
			t.Fatalf("stream = %q, want %q", stream, "stream")
		}
		if hash != "" {
			t.Fatalf("hash = %q, want empty", hash)
		}
		if format != "mp4" {
			t.Fatalf("format = %q, want %q", format, "mp4")
		}
	})
}
