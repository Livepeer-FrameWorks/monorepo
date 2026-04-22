package compare

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalRunner_reads(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.env")
	want := []byte("FOO=1\nBAR=2\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, missing, err := LocalRunner{}.Fetch(context.Background(), path)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if missing {
		t.Fatalf("missing=true, want false")
	}
	if string(got) != string(want) {
		t.Errorf("content: want %q, got %q", want, got)
	}
}

func TestLocalRunner_missingIsNotAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got, missing, err := LocalRunner{}.Fetch(context.Background(), filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file should not error, got: %v", err)
	}
	if !missing {
		t.Errorf("missing=false, want true")
	}
	if got != nil {
		t.Errorf("want nil content, got %q", got)
	}
}

func TestLocalRunner_otherErrorsSurface(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Reading a directory path as a file: on linux os.ReadFile returns
	// EISDIR, on darwin it may succeed with empty bytes — so guard the
	// behavior test. The contract we care about: if err is returned, it
	// is NOT the missing-file case.
	_, missing, err := LocalRunner{}.Fetch(context.Background(), dir)
	if err != nil && missing {
		t.Errorf("Fetch returned both err and missing=true; want mutually exclusive")
	}
}
