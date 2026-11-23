package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// The shell keeps the container's cgroup alive after its processor is killed,
// matching a native Mist service whose controller outlives its child.
func TestProcessingObserverAttributesRealChildCgroupOOM(t *testing.T) {
	isolateProcessingOverrideState(t)
	root := t.TempDir()
	stream := fmt.Sprintf("processing+kernel-oom-%d", time.Now().UnixNano())
	name := fmt.Sprintf("fw-processing-oom-%d-%d", os.Getpid(), time.Now().UnixNano())
	image := os.Getenv("FRAMEWORKS_PROCESS_OOM_TEST_IMAGE")
	if image == "" {
		image = "python:3.13-slim"
	}
	docker := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %s: %v\n%s", args[0], err, output)
		}
		return output
	}
	config, err := json.Marshal(map[string]string{"source": stream})
	if err != nil {
		t.Fatal(err)
	}
	processor := `import ctypes, pathlib, time
ctypes.CDLL(None).prctl(15, b"MistProcAV", 0, 0, 0)
pathlib.Path("/control/ready").touch()
while not pathlib.Path("/control/start").exists(): time.sleep(.01)
blocks=[]
while True:
 blocks.append(bytearray(4*1024*1024))
 time.sleep(.01)
`
	shell := `python3 -c "$1" "$2"; code=$?; printf '%s\n' "$code" > /control/exit; while :; do sleep 1; done`
	docker("run", "-d", "--name", name, "--network", "none", "--cpus", "1", "--memory", "64m", "--memory-swap", "64m", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-v", root+":/control", "--entrypoint", "sh", image, "-c", shell, "--", processor, string(config))
	t.Cleanup(func() { docker("rm", "-f", name) })
	wait := func(description string, ready func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if ready() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s; container logs: %s", description, docker("logs", name))
	}
	wait("processor setup", func() bool { _, statErr := os.Stat(filepath.Join(root, "ready")); return statErr == nil })
	_, pids := processingResidentSnapshot("/proc", stream)
	if len(pids) != 1 {
		t.Fatalf("owned processor identity: %v", pids)
	}
	pid := pids[0]
	childPath := processingOOMEventsPath(strconv.Itoa(pid), os.ReadFile)
	selfPath := processingOOMEventsPath("self", os.ReadFile)
	if childPath == "" || childPath == selfPath {
		t.Fatalf("test needs distinct processor and observer cgroups: child=%q self=%q", childPath, selfPath)
	}
	policy := `[{"process":"AV","codec":"H264","resolution":"x720"}]`
	if overrideErr := setProcessingProcessOverride(stream, policy, "kernel-oom", time.Now().Add(time.Minute), 128*memoryMiB); overrideErr != nil {
		t.Fatal(overrideErr)
	}
	stop := observeProcessingMemory(stream)
	t.Cleanup(stop)
	var initial uint64
	wait("child-cgroup observation", func() bool {
		processingMemoryObservations.Lock()
		defer processingMemoryObservations.Unlock()
		observation := processingMemoryObservations.streams[stream]
		process, ok := observation.processes[pid]
		if !ok || !process.killsKnown {
			return false
		}
		if process.eventsPath != childPath {
			t.Fatalf("observer borrowed its own cgroup: got %q want %q", process.eventsPath, childPath)
		}
		initial = process.initialKills
		return observation.peak > 0
	})
	if startErr := os.WriteFile(filepath.Join(root, "start"), nil, 0600); startErr != nil {
		t.Fatal(startErr)
	}
	wait("kernel OOM kill", func() bool { _, statErr := os.Stat(filepath.Join(root, "exit")); return statErr == nil })
	exit, err := os.ReadFile(filepath.Join(root, "exit"))
	if err != nil || strings.TrimSpace(string(exit)) != "137" {
		t.Fatalf("processor was not SIGKILLed: exit=%q err=%v", exit, err)
	}
	kills, known := readProcessingOOMKills(os.ReadFile, childPath)
	if !known || kills <= initial {
		t.Fatalf("kernel kill did not advance child counter: initial=%d final=%d known=%v", initial, kills, known)
	}
	outputs := processingReadinessFailureOutputs(processingExitError(ProcessExitEvent{StreamName: stream, ProcessType: "AV", ExitCode: -9, PID: pid}))
	required, err := strconv.ParseUint(outputs[mist.ProcessingRequiredMemoryBytes], 10, 64)
	if err != nil || outputs[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryPressure || required < 224*memoryMiB {
		t.Fatalf("real child OOM did not retain the admitted floor and requeue cause: outputs=%v err=%v", outputs, err)
	}
	t.Logf("observed PID %d in %s; kernel oom_kill %d -> %d; retry requires %d bytes", pid, childPath, initial, kills, required)
}
