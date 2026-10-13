package detect

import (
	"os"
	"os/exec"
	"path/filepath"
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

// A service env file is mode 0600 for the service user. The probe must not
// report it missing when the SSH user cannot read it directly: it reads it
// through passwordless sudo, or reports it unreadable.
func TestSHA256ProbeNeverReportsAnUnreadableFileMissing(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not installed")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, "livepeer-gateway.env")
	if err := os.WriteFile(envFile, []byte("FRAMEWORKS_ENVIRONMENT='staging'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(envFile, 0o600) })
	absent := filepath.Join(dir, "absent.env")
	out, err := exec.CommandContext(t.Context(), "sh", "-c", BuildSHA256ProbeScript([]string{envFile, absent})).Output()
	if err != nil {
		t.Fatalf("probe script: %v", err)
	}
	got := ParseSHA256ProbeOutput([]string{envFile, absent}, string(out))
	if got[envFile] == "" {
		t.Fatalf("an existing env file the SSH user cannot read was reported missing: %q", out)
	}
	if got[absent] != "" {
		t.Fatalf("absent file = %q, want missing", got[absent])
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
