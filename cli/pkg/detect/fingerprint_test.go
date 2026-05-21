package detect

import (
	"strings"
	"testing"
)

func TestBuildSHA256ProbeScriptKeepsPathOutOfAwkProgram(t *testing.T) {
	script := BuildSHA256ProbeScript([]string{"/opt/frameworks/example path/bin"})
	if strings.Contains(script, `awk '{printf`) {
		t.Fatalf("script embeds path formatting inside awk program: %s", script)
	}
	if !strings.Contains(script, `awk '{print $1}'`) {
		t.Fatalf("script should only use awk to print field 1: %s", script)
	}
	if !strings.Contains(script, "'/opt/frameworks/example path/bin'") {
		t.Fatalf("script did not shell-quote path with spaces: %s", script)
	}
}

func TestParseSHA256ProbeOutput(t *testing.T) {
	paths := []string{"/a", "/b", "/c"}
	got := ParseSHA256ProbeOutput(paths, "ABC\t/a\nMISSING\t/b\n")
	if got["/a"] != "abc" {
		t.Fatalf("/a hash got %q, want abc", got["/a"])
	}
	if got["/b"] != "" {
		t.Fatalf("/b hash got %q, want empty missing marker", got["/b"])
	}
	if got["/c"] != "" {
		t.Fatalf("/c hash got %q, want empty absent-line fallback", got["/c"])
	}
}
