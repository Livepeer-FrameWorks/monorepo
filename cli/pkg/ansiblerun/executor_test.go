package ansiblerun

import (
	"os"
	"strings"
	"testing"
)

func TestWriteExtraVarsFileUsesPrivateAtFile(t *testing.T) {
	files, cleanup, err := writeExtraVarsFile(map[string]any{
		"secret": "not-on-command-line",
	})
	if err != nil {
		t.Fatalf("writeExtraVarsFile: %v", err)
	}
	defer cleanup()

	if len(files) != 1 || !strings.HasPrefix(files[0], "@") {
		t.Fatalf("files = %#v, want one @file", files)
	}
	path := strings.TrimPrefix(files[0], "@")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat extra vars file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 0600", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extra vars file: %v", err)
	}
	if !strings.Contains(string(raw), "not-on-command-line") {
		t.Fatalf("extra vars content missing value: %s", raw)
	}
}

func TestPreviewDoesNotRenderExtraVarsValues(t *testing.T) {
	exec := &Executor{}
	argv, err := exec.Preview(ExecuteOptions{
		Playbook:  "/tmp/playbook.yml",
		Inventory: "/tmp/inventory.yml",
		ExtraVars: map[string]any{
			"secret": "do-not-render",
		},
	})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	cmd := strings.Join(argv, " ")
	if strings.Contains(cmd, "do-not-render") {
		t.Fatalf("preview leaked extra vars value: %s", cmd)
	}
	if !strings.Contains(cmd, "--extra-vars=@<extra-vars-file>") {
		t.Fatalf("preview did not show vars-file shape: %s", cmd)
	}
}
