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

func TestRecapOutputerParsesChangedCounts(t *testing.T) {
	out := &RecapOutputer{}
	src := strings.NewReader("PLAY RECAP *********************************************************************\n" +
		"central-eu-1              : ok=20   changed=0    unreachable=0    failed=0\n" +
		"edge-eu-1                 : ok=21   changed=2    unreachable=0    failed=0\n")
	if err := out.Print(context.Background(), src, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !out.HasRecap() {
		t.Fatal("expected recap")
	}
	if !out.Changed() {
		t.Fatal("expected changed=true")
	}
	if got := out.Hosts["central-eu-1"].Changed; got != 0 {
		t.Fatalf("central changed=%d, want 0", got)
	}
	if got := out.Hosts["edge-eu-1"].Changed; got != 2 {
		t.Fatalf("edge changed=%d, want 2", got)
	}
}

func TestRecapOutputerNoChangedIsNoop(t *testing.T) {
	out := &RecapOutputer{}
	src := strings.NewReader("TASK [x] *********************************************************************\n")
	if err := out.Print(context.Background(), src, nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if out.HasRecap() {
		t.Fatal("expected no recap")
	}
	if out.Changed() {
		t.Fatal("expected changed=false")
	}
}
