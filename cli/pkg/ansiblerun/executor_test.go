package ansiblerun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestTailWriterRetainsBoundedSuffix(t *testing.T) {
	w := newTailWriter(5)
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := w.Write([]byte("defg")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := w.String(); got != "cdefg" {
		t.Fatalf("tail = %q, want %q", got, "cdefg")
	}
}

func TestTailWriterSupportsConcurrentStreams(t *testing.T) {
	w := newTailWriter(1024)
	var wg sync.WaitGroup
	for _, line := range []string{"stdout\n", "stderr\n"} {
		line := line
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = w.Write([]byte(line))
		}()
	}
	wg.Wait()
	got := w.String()
	if !strings.Contains(got, "stdout\n") || !strings.Contains(got, "stderr\n") {
		t.Fatalf("tail did not retain both streams: %q", got)
	}
}

func TestExecutorIncludesOutputTailOnFailure(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ansible-playbook")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho stdout-detail\necho stderr-detail >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write fake ansible-playbook: %v", err)
	}

	err := (&Executor{Binary: binary}).Execute(context.Background(), ExecuteOptions{
		Playbook:  filepath.Join(dir, "playbook.yml"),
		Inventory: filepath.Join(dir, "inventory.yml"),
	})
	if err == nil {
		t.Fatal("expected command failure")
	}
	for _, detail := range []string{"Ansible output tail:", "stdout-detail", "stderr-detail"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("error missing %q: %v", detail, err)
		}
	}
}

func TestExecutorIncludesOutputTailWithCustomOutputer(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ansible-playbook")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho failed-task-detail\nexit 2\n"), 0o755); err != nil {
		t.Fatalf("write fake ansible-playbook: %v", err)
	}

	err := (&Executor{Binary: binary}).Execute(context.Background(), ExecuteOptions{
		Playbook:  filepath.Join(dir, "playbook.yml"),
		Inventory: filepath.Join(dir, "inventory.yml"),
		Outputer:  &RecapOutputer{},
	})
	if err == nil {
		t.Fatal("expected command failure")
	}
	if !strings.Contains(err.Error(), "Ansible output tail:") || !strings.Contains(err.Error(), "failed-task-detail") {
		t.Fatalf("custom outputer failure omitted Ansible details: %v", err)
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
