package handlers

import (
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A kernel OOM kills the processor, the recording output then fails, and Mist posts the
// processor's PROCESS_EXIT asynchronously after the push failure was handled. The attempt
// must still settle as memory pressure, not as an ordinary failure that spends a retry.
func TestFailedPushWaitsForLateOOMProcessExit(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, policy = "processing+oom-late-exit", `[{"process":"AV","codec":"H264","resolution":"x720"}]`
	if err := setProcessingProcessOverride(stream, policy, "oom-late-exit", time.Now().Add(time.Hour), 256*memoryMiB); err != nil {
		t.Fatal(err)
	}
	old := processingOOMCounter
	processingOOMCounter = func(string) (uint64, bool) { return 9, true }
	t.Cleanup(func() { processingOOMCounter = old })
	processingMemoryObservations.Lock()
	processingMemoryObservations.streams[stream] = &processingMemoryObservation{peak: 400 * memoryMiB, processes: map[int]processingOOMProcess{123: {eventsPath: "fixture/memory.events", initialKills: 8, killsKnown: true}}}
	processingMemoryObservations.Unlock()
	t.Cleanup(func() {
		processingMemoryObservations.Lock()
		delete(processingMemoryObservations.streams, stream)
		processingMemoryObservations.Unlock()
	})
	RegisterProcessExitListener(stream)
	t.Cleanup(func() { UnregisterProcessExitListener(stream) })

	go func() {
		time.Sleep(300 * time.Millisecond)
		RouteProcessExit(ProcessExitEvent{StreamName: stream, ProcessType: "AV", Config: policy, ExitCode: -9, PID: 123})
	}()
	var results []*ipcpb.ProcessingJobResult
	send := func(msg *ipcpb.ControlMessage) { results = append(results, msg.GetProcessingJobResult()) }
	h := &ProcessingJobHandler{logger: discardLog().Logger, storagePath: t.TempDir()}
	client := fakeMistServer(t, map[string]any{"active_streams": nil})
	h.reportFailedPushWithRecording(discardLog(), client, send, "oom-late-exit", stream, "", ProcessingPushEndEvent{PushID: 1, PushStatus: "failed"}, ProcessingRecordingEndEvent{ExitReason: "input ended"})

	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	got := results[0]
	if got.GetStatus() != processingResultRetryable || got.GetOutputs()[mist.ProcessingResultRetryCause] != mist.ProcessingRetryCauseMemoryPressure {
		t.Fatalf("late OOM exit was not reported as memory pressure: status=%q outputs=%v error=%q", got.GetStatus(), got.GetOutputs(), got.GetError())
	}
}

// Without an OOM exit the push failure keeps its own classification after the short grace.
func TestFailedPushWithoutOOMExitKeepsItsClassification(t *testing.T) {
	old := processingExitGrace
	processingExitGrace = 200 * time.Millisecond
	t.Cleanup(func() { processingExitGrace = old })
	const stream = "processing+no-oom-exit"
	RegisterProcessExitListener(stream)
	t.Cleanup(func() { UnregisterProcessExitListener(stream) })
	var results []*ipcpb.ProcessingJobResult
	send := func(msg *ipcpb.ControlMessage) { results = append(results, msg.GetProcessingJobResult()) }
	h := &ProcessingJobHandler{logger: discardLog().Logger, storagePath: t.TempDir()}
	client := fakeMistServer(t, map[string]any{"active_streams": nil})
	h.reportFailedPushWithRecording(discardLog(), client, send, "no-oom-exit", stream, "", ProcessingPushEndEvent{PushID: 1, PushStatus: "failed"}, ProcessingRecordingEndEvent{ExitReason: "input ended"})
	if len(results) != 1 || results[0].GetOutputs()[mist.ProcessingResultRetryCause] == mist.ProcessingRetryCauseMemoryPressure {
		t.Fatalf("results = %v", results)
	}
}
