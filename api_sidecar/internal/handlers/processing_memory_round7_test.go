package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestMemoryAdmissionReducesPlanToFreeReservationPool(t *testing.T) {
	previous := runtime.GOMAXPROCS(4)
	t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	isolateProcessingOverrideState(t)
	const stream, job = "processing+remaining-pool", "remaining-pool"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(3072) * memoryMiB
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	policy := `[{"process":"AV","codec":"H264","resolution":"0x2160"},{"process":"AV","codec":"H264","resolution":"0x720"},{"process":"AV","codec":"H264","resolution":"0x360"}]`
	source := mist.SourceMediaInfo{Width: 3840, Height: 2160}
	full, err := mist.PlanProcessingMemory(policy, source, processingMemoryBudget(total), 4)
	if err != nil {
		t.Fatal(err)
	}
	minimum, err := mist.PlanProcessingMemoryThreads(full.ProcessesJSON, source, 0, 1)
	if err != nil || minimum.Bytes >= full.Bytes {
		t.Fatalf("fixture has no concurrency to reduce: full=%d minimum=%d err=%v", full.Bytes, minimum.Bytes, err)
	}
	free := minimum.Bytes + (full.Bytes-minimum.Bytes)/2
	if reserveErr := setProcessingProcessOverride("processing+occupied", "[]", "occupied", time.Now().Add(time.Hour), processingMemoryBudget(total)-free); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	actual, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, policy, source, nil, func(*ipcpb.ControlMessage) {})
	if err != nil {
		t.Fatalf("job that fits a reduced plan was queued: %v", err)
	}
	estimate, err := mist.EstimateProcessingMemory(actual, source)
	if err != nil || estimate > free || estimate >= full.Bytes {
		t.Fatalf("pool overcommitted: %d %v", estimate, err)
	}
}

func TestMemoryRefusalReportsEffectiveBudget(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+budget-report", "budget-report"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(1536) * memoryMiB
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	_, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, fourKLadder(t), mist.SourceMediaInfo{Width: 3840, Height: 2160}, nil, func(*ipcpb.ControlMessage) {})
	if got := processingMemoryFailureOutputs(err)["processing_memory_budget_bytes"]; got != "1073741824" {
		t.Fatalf("node refusal lost effective 1 GiB budget: %q (%v)", got, err)
	}
}

func TestMissingActiveStreamsCannotReleaseGeneration(t *testing.T) {
	err := drainProcessingGenerationFromActiveStreams(discardLog(), "processing+unknown", func() (map[string]interface{}, error) { return map[string]interface{}{}, nil }, 10*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatal("missing active_streams was accepted as proof the generation stopped")
	}
}

func TestMemorySnapshotFailureRemainsRetryable(t *testing.T) {
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return 0, 0, fmt.Errorf("temporary cgroup read failure") }}
	_, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{}, "processing+snapshot", "[]", mist.SourceMediaInfo{}, nil, func(*ipcpb.ControlMessage) {})
	if processingMemoryFailureStatus(err) != processingResultRetryable {
		t.Fatalf("temporary memory read failed permanently: %v", err)
	}
}

func TestMemoryLedgerWriteFailureRemainsRetryable(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+ledger-failure", "ledger-failure"
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A file in place of the persistence directory makes every write fail,
	// independently of the test runner's filesystem permissions.
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	processingProcessOverridesMu.Lock()
	processingOverrideStateDir = blocked
	processingProcessOverridesMu.Unlock()
	total := uint64(3072) * memoryMiB
	admitted, err := reserveProcessingMemory(stream, job, "[]", 256*memoryMiB, total, total)
	if admitted || err == nil || processingMemoryFailureStatus(err) != processingResultRetryable {
		t.Fatalf("ledger write failure became terminal or admitted: %v %v", admitted, err)
	}
	processingProcessOverridesMu.Lock()
	defer processingProcessOverridesMu.Unlock()
	if processingOverrideRecords[stream].MemoryBytes != 0 {
		t.Fatal("failed persistence published a reservation")
	}
}
