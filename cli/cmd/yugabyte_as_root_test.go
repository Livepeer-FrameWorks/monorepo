package cmd

import (
	"os/exec"
	"strings"
	"testing"
)

func TestYugabyteAsRootRunsTheScriptWithRootOrSudo(t *testing.T) {
	wrapped := yugabyteAsRoot(`echo "state=$(id -u)"`)
	if !strings.Contains(wrapped, `sudo -n bash -c `) || !strings.Contains(wrapped, `if [ "$(id -u)" = 0 ]; then bash -c `) {
		t.Fatalf("wrapper must run the script directly as root and through sudo otherwise: %s", wrapped)
	}
	out, err := exec.Command("bash", "-c", strings.Replace(wrapped, "sudo -n bash -c", "bash -c", 1)).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapped script did not run: %v: %s", err, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(out)), "state=") {
		t.Fatalf("wrapped script output = %q", out)
	}
}
