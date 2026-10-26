package ansiblerun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/watchdog"
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestExecutorStreamsToContextOutputWriter(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "ansible-playbook")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho play-output\necho play-error >&2\n"), 0o755); err != nil {
		t.Fatalf("write fake ansible-playbook: %v", err)
	}

	var out lockedBuffer
	ctx := WithOutputWriter(context.Background(), &out)
	if err := (&Executor{Binary: binary}).Execute(ctx, ExecuteOptions{
		Playbook:  filepath.Join(dir, "playbook.yml"),
		Inventory: filepath.Join(dir, "inventory.yml"),
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{"play-output", "play-error"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("context writer missing %q: %q", want, out.String())
		}
	}
}

// Under a watchdog context a playbook that keeps printing runs past the stall window, and one that goes silent is
// cancelled with the stall as the cause.
func TestExecutorOutputFeedsWatchdog(t *testing.T) {
	dir := t.TempDir()
	chatty := filepath.Join(dir, "chatty")
	script := "#!/bin/sh\nfor i in 1 2 3 4 5 6 7 8; do echo \"TASK [step $i]\"; sleep 0.05; done\n"
	if err := os.WriteFile(chatty, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ansible-playbook: %v", err)
	}
	silent := filepath.Join(dir, "silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\necho \"TASK [last task]\"\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatalf("write fake ansible-playbook: %v", err)
	}
	opts := ExecuteOptions{Playbook: filepath.Join(dir, "playbook.yml"), Inventory: filepath.Join(dir, "inventory.yml")}

	ctx, w := watchdog.Start(WithOutputWriter(context.Background(), io.Discard), "chatty playbook", 200*time.Millisecond)
	defer w.Stop()
	if err := (&Executor{Binary: chatty}).Execute(ctx, opts); err != nil {
		t.Fatalf("playbook printing every 50ms under a 200ms stall window failed: %v", w.Err(err))
	}

	ctx, w = watchdog.Start(WithOutputWriter(context.Background(), io.Discard), "silent playbook", 200*time.Millisecond)
	defer w.Stop()
	started := time.Now()
	err := w.Err((&Executor{Binary: silent}).Execute(ctx, opts))
	if err == nil {
		t.Fatal("silent playbook passed under a 200ms stall window")
	}
	if time.Since(started) > 4*time.Second {
		t.Fatalf("silent playbook ran %s; the watchdog did not stop it", time.Since(started))
	}
	if !strings.Contains(err.Error(), "silent playbook stalled") || !strings.Contains(err.Error(), "TASK [last task]") {
		t.Fatalf("stall error %q does not name the playbook and its last task", err)
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
