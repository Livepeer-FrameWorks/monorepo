package ansiblerun

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestLineOutputer_PrefixesEachLine(t *testing.T) {
	var buf bytes.Buffer
	out := &LineOutputer{W: &buf, Prefix: "[ansible] "}

	src := strings.NewReader("first line\nsecond line\n")
	if err := out.Print(context.Background(), src, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}

	got := buf.String()
	want := "[ansible] first line\n[ansible] second line\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLineOutputer_PassthroughWithoutPrefix(t *testing.T) {
	var buf bytes.Buffer
	out := &LineOutputer{W: &buf}

	src := strings.NewReader("PLAY [yugabyte] **\nTASK [install] **\n")
	if err := out.Print(context.Background(), src, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}

	got := buf.String()
	want := "PLAY [yugabyte] **\nTASK [install] **\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLineOutputer_NilWriterIsNoop(t *testing.T) {
	out := &LineOutputer{}
	src := strings.NewReader("anything\n")
	if err := out.Print(context.Background(), src, nil); err != nil {
		t.Errorf("Print should be a noop, got error: %v", err)
	}
}
