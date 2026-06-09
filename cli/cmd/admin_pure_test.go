package cmd

import (
	"os"
	"testing"
)

func TestResolveServiceTypeLabel(t *testing.T) {
	cases := map[string]string{
		"transcoder": "transcoder",
		"  ingest  ": "ingest",
		"":           "service",
		"   ":        "service",
	}
	for in, want := range cases {
		if got := resolveServiceTypeLabel(in); got != want {
			t.Errorf("resolveServiceTypeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveUserPassword_Flag(t *testing.T) {
	got, err := resolveUserPassword("s3cret", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("got %q, want s3cret", got)
	}
}

func TestResolveUserPassword_Stdin(t *testing.T) {
	withStdin(t, "from-stdin\n")
	got, err := resolveUserPassword("", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "from-stdin" {
		t.Errorf("got %q, want from-stdin", got)
	}
}

func TestResolveUserPassword_StdinEmpty(t *testing.T) {
	withStdin(t, "")
	if _, err := resolveUserPassword("", true); err == nil {
		t.Fatal("expected error for empty stdin password")
	}
}

func TestResolveUserPassword_NoSourceNonTTY(t *testing.T) {
	// A pipe is never a TTY, so with no flag and no --password-stdin the
	// function must refuse rather than block on an interactive prompt.
	withStdin(t, "")
	if _, err := resolveUserPassword("", false); err == nil {
		t.Fatal("expected error when no password source and stdin is not a TTY")
	}
}

// withStdin replaces os.Stdin with a pipe carrying content for the duration of
// the test, restoring the original afterwards.
func withStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()
}
