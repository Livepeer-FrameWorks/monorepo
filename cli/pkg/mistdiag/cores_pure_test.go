package mistdiag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// safeBundleName lowercases and reduces a service name to a filename-safe slug
// (the bundle tarball / journal log are named from it), collapsing disallowed
// runes to '-' and trimming leading/trailing dashes.
func TestSafeBundleName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"mistserver", "mistserver"},
		{"Mist Server", "mist-server"},
		{"edge_01.node", "edge_01.node"},
		{"a/b:c", "a-b-c"},
		{"!!!", ""},
		{"  spaced  ", "spaced"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := safeBundleName(tt.in); got != tt.want {
				t.Fatalf("safeBundleName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// shellWord single-quotes a value for safe interpolation into a remote sh -s
// script; embedded single quotes use the '\” close/escape/reopen idiom.
func TestShellWord(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"plain", "'plain'"},
		{"two words", "'two words'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := shellWord(tt.in); got != tt.want {
				t.Fatalf("shellWord(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSSHArgs(t *testing.T) {
	base := sshArgs("edge@host", "")
	for _, want := range []string{"BatchMode=yes", "StrictHostKeyChecking=accept-new", "ConnectTimeout=15"} {
		if !strings.Contains(strings.Join(base, " "), want) {
			t.Errorf("base args missing %q: %v", want, base)
		}
	}
	if strings.Contains(strings.Join(base, " "), "-i") {
		t.Errorf("no key path should not add -i: %v", base)
	}

	keyed := sshArgs("edge@host", "/tmp/id_ed25519")
	joined := strings.Join(keyed, " ")
	if !strings.Contains(joined, "-i") || !strings.Contains(joined, "/tmp/id_ed25519") {
		t.Errorf("key path should add -i <path>: %v", keyed)
	}
}

func TestDebugSearchPath(t *testing.T) {
	dir := "/opt/debug"
	got := debugSearchPath(dir)
	want := strings.Join([]string{dir, filepath.Join(dir, "bin")}, string(os.PathListSeparator))
	if got != want {
		t.Fatalf("debugSearchPath(%q) = %q, want %q", dir, got, want)
	}
}

// buildCoreCollectScript names the journal log from the sanitized service and
// quotes the raw service/since values where they are interpolated into sh.
func TestBuildCoreCollectScript(t *testing.T) {
	script := buildCoreCollectScript("2024-01-02", "mist edge")
	for _, want := range []string{
		"coredumpctl dump MistController",
		"tar -czf",
		"journal-mist-edge.log", // safeBundleName("mist edge")
		"'mist edge'",           // shellWord(service)
		"'2024-01-02'",          // shellWord(since)
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n---\n%s", want, script)
		}
	}

	// Empty service falls back to "mistserver" for the log filename.
	fallback := buildCoreCollectScript("now", "")
	if !strings.Contains(fallback, "journal-mistserver.log") {
		t.Errorf("empty service should fall back to mistserver: %s", fallback)
	}
}
