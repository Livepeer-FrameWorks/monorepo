package handlers

import (
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestOOMRequeueUsesObservedPeakWithoutTenantRetry(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+oom-observed", "oom-observed"
	policy := `[{"process":"AV","codec":"H264","resolution":"x720"}]`
	if err := setProcessingProcessOverride(stream, policy, job, time.Now().Add(time.Hour), 256*memoryMiB); err != nil {
		t.Fatal(err)
	}
	old := processingOOMCounter
	processingOOMCounter = func(string) (uint64, bool) { return 9, true }
	t.Cleanup(func() { processingOOMCounter = old })
	observation := &processingMemoryObservation{peak: 400 * memoryMiB, processes: map[int]processingOOMProcess{123: {eventsPath: "fixture/memory.events", initialKills: 8, killsKnown: true}}}
	processingMemoryObservations.Lock()
	processingMemoryObservations.streams[stream] = observation
	processingMemoryObservations.Unlock()
	t.Cleanup(func() {
		processingMemoryObservations.Lock()
		delete(processingMemoryObservations.streams, stream)
		processingMemoryObservations.Unlock()
	})
	for _, process := range []string{"AV", "Thumbs"} {
		evt := ProcessExitEvent{StreamName: stream, ProcessType: process, Config: policy, ExitCode: -9, PID: 123}
		err := processingExitError(evt)
		out := processingReadinessFailureOutputs(err)
		if !retryableProcessingExit(evt) || out[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryPressure || out[mist.ProcessingRequiredMemoryBytes] != strconv.FormatUint(564*memoryMiB, 10) || out[mist.ProcessingMemoryPolicy] != policy {
			t.Fatalf("%s OOM spends tenant retries or loses measured peak: error=%v outputs=%v", process, err, out)
		}
	}
	processingOOMCounter = func(string) (uint64, bool) { return 8, true }
	if out := processingReadinessFailureOutputs(processingExitError(ProcessExitEvent{StreamName: stream, ProcessType: "AV", ExitCode: -9, PID: 123})); out != nil {
		t.Fatalf("ordinary SIGKILL was misclassified as OOM: %v", out)
	}
}
func TestProcessingRSSMeasurementUsesExactSourceIdentity(t *testing.T) {
	root := t.TempDir()
	for i, source := range []string{"processing+owned", "processing+owned-other"} {
		dir := filepath.Join(root, strconv.Itoa(i+101))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		config, _ := json.Marshal(map[string]string{"source": source, "other": "processing+owned"})
		for name, data := range map[string]string{"comm": "MistProcAV\n", "cmdline": "MistProcAV\x00" + string(config) + "\x00", "status": "VmRSS:\t2048 kB\nRssAnon:\t512 kB\nRssFile:\t1024 kB\nRssShmem:\t512 kB\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if bytes := processingResidentBytes(root, "processing+owned"); bytes != memoryMiB {
		t.Fatalf("measured another job's process: %d", bytes)
	}
}
func TestOOMMemoryFloorSurvivesReplanning(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+oom-floor", "oom-floor"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return 3 * 1024 * memoryMiB, 3 * 1024 * memoryMiB, nil }}
	req := &ipcpb.ProcessingJobRequest{JobId: job, Params: map[string]string{mist.ProcessingRequiredMemoryBytes: strconv.FormatUint(4*1024*memoryMiB, 10)}}
	_, err := h.admitProcessingPolicy(discardLog(), req, stream, `[{"process":"AV","codec":"H264","resolution":"x360"}]`, mist.SourceMediaInfo{Width: 640, Height: 360}, nil)
	out := processingMemoryFailureOutputs(err)
	if err == nil || out[mist.ProcessingRequiredMemoryBytes] != req.Params[mist.ProcessingRequiredMemoryBytes] || out[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryNodeTooSmall {
		t.Fatalf("planner lowered OOM's measured floor: err=%v outputs=%v", err, out)
	}
}
