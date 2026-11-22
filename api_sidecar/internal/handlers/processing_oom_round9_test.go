package handlers

import (
	"errors"
	"testing"
)

func TestR9NativeOOMCounterUsesProcessCgroup(t *testing.T) {
	files := map[string]string{"/proc/self/cgroup": "0::/system.slice/frameworks-mistserver.service\n", "/sys/fs/cgroup/memory.events": "oom_kill 999\n", "/sys/fs/cgroup/system.slice/frameworks-mistserver.service/memory.events": "oom_kill 7\n"}
	count, known := readProcessingOOMKills(func(path string) ([]byte, error) {
		if value, ok := files[path]; ok {
			return []byte(value), nil
		}
		return nil, errors.New("missing fixture path")
	})
	if !known || count != 7 {
		t.Fatalf("native Mist OOM counter read the root instead of its service: count=%d known=%v", count, known)
	}
}

func TestR9UnobservedPIDCannotBeAttributedToJobOOM(t *testing.T) {
	old := processingOOMCounter
	processingOOMCounter = func(string) (uint64, bool) { return 9, true }
	t.Cleanup(func() { processingOOMCounter = old })
	const stream = "processing+pid-attribution"
	processingMemoryObservations.Lock()
	processingMemoryObservations.streams[stream] = &processingMemoryObservation{peak: 400 * memoryMiB, processes: map[int]processingOOMProcess{}}
	processingMemoryObservations.Unlock()
	t.Cleanup(func() {
		processingMemoryObservations.Lock()
		delete(processingMemoryObservations.streams, stream)
		processingMemoryObservations.Unlock()
	})
	err := processingExitError(ProcessExitEvent{StreamName: stream, ProcessType: "AV", ExitCode: -9, PID: 404})
	if outputs := processingOOMOutputs(err); outputs != nil {
		t.Fatalf("unobserved PID borrowed another job's cgroup OOM: %v", outputs)
	}
}
