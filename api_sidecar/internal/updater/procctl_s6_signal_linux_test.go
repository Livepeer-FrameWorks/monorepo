//go:build linux

package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMistControllerParentPIDsExcludesAngelChildren(t *testing.T) {
	t.Parallel()

	procRoot := t.TempDir()
	writeProc := func(pid, ppid int, comm string) {
		dir := filepath.Join(procRoot, fmt.Sprint(pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		status := fmt.Sprintf("Name:\t%s\nPPid:\t%d\n", comm, ppid)
		if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeProc(100, 1, "MistController")
	writeProc(101, 100, "MistController")
	writeProc(200, 1, "MistController")
	writeProc(201, 200, "MistController")
	writeProc(300, 1, "frameworks")

	got, err := mistControllerParentPIDs(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{100, 200}; !slices.Equal(got, want) {
		t.Fatalf("parent PIDs = %v, want %v", got, want)
	}
}

func TestReplaceMistPayloadInPlacePreservesExecutableLookupPath(t *testing.T) {
	const helperEnv = "FW_MIST_EXEC_HELPER"
	if stage := os.Getenv(helperEnv); stage != "" {
		marker := os.Getenv("FW_MIST_EXEC_MARKER")
		if stage == "new" {
			if err := os.WriteFile(marker, []byte("re-execed"), 0o644); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			return
		}
		ready := os.Getenv("FW_MIST_EXEC_READY")
		observed := os.Getenv("FW_MIST_EXEC_PATH")
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGUSR1)
		defer signal.Stop(signals)
		if err := os.WriteFile(ready, []byte("ready"), 0o644); err != nil {
			t.Fatal(err)
		}
		<-signals
		executable, err := os.Readlink("/proc/self/exe")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(observed, []byte(executable), 0o644); err != nil {
			t.Fatal(err)
		}
		restart := filepath.Join(filepath.Dir(strings.TrimSuffix(executable, " (deleted)")), "MistController")
		env := os.Environ()
		for i := range env {
			if strings.HasPrefix(env[i], helperEnv+"=") {
				env[i] = helperEnv + "=new"
			}
		}
		if err := syscall.Exec(restart, os.Args, env); err != nil {
			t.Fatal(err)
		}
		return
	}

	parent := t.TempDir()
	root := filepath.Join(parent, "mistserver")
	staged := filepath.Join(parent, "staged")
	for _, dir := range []string{filepath.Join(root, "bin"), filepath.Join(staged, "bin")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "bin", "MistController"), filepath.Join(staged, "bin", "MistController")} {
		if err := copyFile(testBinary, path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ready := filepath.Join(parent, "ready")
	marker := filepath.Join(parent, "marker")
	observed := filepath.Join(parent, "observed")
	cmd := exec.Command(filepath.Join(root, "bin", "MistController"), "-test.run=^TestReplaceMistPayloadInPlacePreservesExecutableLookupPath$")
	cmd.Env = append(os.Environ(),
		helperEnv+"=old",
		"FW_MIST_EXEC_READY="+ready,
		"FW_MIST_EXEC_MARKER="+marker,
		"FW_MIST_EXEC_PATH="+observed,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	previousTimeout, previousPoll := mistReloadTimeout, mistReloadPoll
	mistReloadTimeout, mistReloadPoll = 5*time.Second, 10*time.Millisecond
	defer func() { mistReloadTimeout, mistReloadPoll = previousTimeout, previousPoll }()
	if err := replaceMistPayloadInPlace(staged, root, func() error {
		return cmd.Process.Signal(syscall.SIGUSR1)
	}); err != nil {
		t.Fatal(err)
	}
	if err := waitMistControllerReload(context.Background(), filepath.Join(root, "bin", "MistController")); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper re-exec failed: %v", err)
	}
	markerBytes, err := os.ReadFile(marker)
	if err != nil || string(markerBytes) != "re-execed" {
		t.Fatalf("replacement executable did not run: marker=%q err=%v", markerBytes, err)
	}
	observedBytes, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(root, "bin", "MistController")
	if !strings.HasPrefix(string(observedBytes), wantPrefix) {
		t.Fatalf("running executable moved away from canonical path: got %q, want prefix %q", observedBytes, wantPrefix)
	}
}
