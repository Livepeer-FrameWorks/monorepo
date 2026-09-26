//go:build linux

package updater

import (
	"context"
	"errors"
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
	writeProc(400, 1, "MistController")
	foreignStatus := fmt.Sprintf("Name:\tMistController\nPPid:\t1\nUid:\t%d\t%d\t%d\t%d\n", os.Geteuid()+1, os.Geteuid()+1, os.Geteuid()+1, os.Geteuid()+1)
	if err := os.WriteFile(filepath.Join(procRoot, "400", "status"), []byte(foreignStatus), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := mistControllerParentPIDs(procRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{100, 200}
	if os.Geteuid() == 0 {
		want = append(want, 400)
	}
	if !slices.Equal(got, want) {
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
	testBinary, executableErr := os.Executable()
	if executableErr != nil {
		t.Fatal(executableErr)
	}
	for _, path := range []string{filepath.Join(root, "bin", "MistController"), filepath.Join(staged, "bin", "MistController")} {
		if err := copyFile(testBinary, path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ready := filepath.Join(parent, "ready")
	marker := filepath.Join(parent, "marker")
	observed := filepath.Join(parent, "observed")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "bin", "MistController"), "-test.run=^TestReplaceMistPayloadInPlacePreservesExecutableLookupPath$")
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

// A controller wedged in shutdown ignores SIGTERM; the restart must still
// clear it so the supervisor can start a new one.
func TestStopMistControllersKillsControllerThatIgnoresTerm(t *testing.T) {
	const helperEnv = "FW_MIST_WEDGED_HELPER"
	if os.Getenv(helperEnv) != "" {
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(time.Minute)
		return
	}

	helperCtx, cancelHelper := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelHelper()
	cmd := exec.CommandContext(helperCtx, os.Args[0], "-test.run=^TestStopMistControllersKillsControllerThatIgnoresTerm$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	pid := cmd.Process.Pid
	time.Sleep(200 * time.Millisecond)

	originalGrace := mistStopGrace
	mistStopGrace = 500 * time.Millisecond
	defer func() { mistStopGrace = originalGrace }()

	scans := 0
	err := stopMistControllers(context.Background(), []int{pid}, func() ([]int, error) {
		scans++
		return []int{pid}, nil
	})
	if err != nil {
		t.Fatalf("stopMistControllers: %v", err)
	}
	if scans != 1 {
		t.Fatalf("escalation scans = %d, want 1 (SIGTERM alone must not have ended the helper)", scans)
	}
	select {
	case waitErr := <-waited:
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) || exitErr.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("helper exit = %v, want SIGKILL", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper still running")
	}
}
