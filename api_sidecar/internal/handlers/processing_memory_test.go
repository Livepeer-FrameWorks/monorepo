package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func fourKLadder(t *testing.T) string {
	t.Helper()
	var processes []map[string]interface{}
	for _, height := range []int{2160, 1440, 1080, 720, 480, 360} {
		processes = append(processes, map[string]interface{}{"process": "AV", "codec": "H264", "resolution": "0x" + strconv.Itoa(height)})
	}
	processes = append(processes,
		map[string]interface{}{"process": "Thumbs"},
		map[string]interface{}{"process": "AV", "codec": "AAC"},
		map[string]interface{}{"process": "AV", "codec": "opus"})
	encoded, err := json.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := mist.PlanProcessingMemory(string(encoded), mist.SourceMediaInfo{Width: 3840, Height: 2160}, processingMemoryBudget(3*1024*memoryMiB), 4)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Fits {
		t.Fatalf("full 4K ladder cannot fit constrained node: %d", plan.Bytes)
	}
	return plan.ProcessesJSON
}

func TestProcessingReadinessResourceExitRetryable(t *testing.T) {
	exits := make(chan ProcessExitEvent, 1)
	exits <- ProcessExitEvent{ProcessType: "AV", Config: `{"codec":"H264"}`, ExitCode: 1, Status: "unrecoverable", Reason: "decode runtime failure"}
	// Exercise the event received after the loop's sticky-exit check.
	handler := &ProcessingJobHandler{}
	_, _, err := handler.waitForProcessingStreamReady(context.Background(), discardLog(), nil, &ipcpb.ProcessingJobRequest{}, "processing+readiness-resource-race", "[]", exits, nil, nil, nil)
	if processingReadinessFailureStatus(err) != processingResultRetryable {
		t.Fatalf("transient readiness exit became permanent: %v", err)
	}
}

func TestProcessingMemoryQueuesConcurrent4KLaddersOnThreeGiB(t *testing.T) {
	isolateProcessingOverrideState(t)
	dir := t.TempDir()
	if err := configureProcessingOverridePersistence(dir, discardLog().Logger); err != nil {
		t.Fatal(err)
	}
	policy := fourKLadder(t)
	bytes, err := processingMemoryEstimate(policy, mist.SourceMediaInfo{Width: 3840, Height: 2160})
	if err != nil {
		t.Fatal(err)
	}
	total := uint64(3) * 1024 * memoryMiB
	for _, name := range []string{"processing+first", "processing+second"} {
		if err := setProcessingProcessOverride(name, processingSourceProbePolicy, name, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := reserveProcessingMemory("processing+first", "processing+first", policy, bytes, total, total); err != nil || !ok {
		t.Fatalf("one 4K ladder was not admitted: bytes=%d err=%v", bytes, err)
	}
	if ok, err := reserveProcessingMemory("processing+second", "processing+second", policy, bytes, total, total); err != nil || ok {
		t.Fatalf("second 4K ladder bypassed memory queue: admitted=%v err=%v", ok, err)
	}
	if got, _ := getProcessingProcessOverride("processing+second"); got != processingSourceProbePolicy {
		t.Fatal("queued job published encoder policy before admission")
	}
	// Restore only durable state, as a new Helmsman would while Mist remains alive.
	processingProcessOverridesMu.Lock()
	processingOverrideRecords = map[string]processingOverrideRecord{}
	processingProcessOverrides = map[string]string{}
	processingProcessOverridesMu.Unlock()
	if err := configureProcessingOverridePersistence(dir, discardLog().Logger); err != nil {
		t.Fatal(err)
	}
	if ok, err := reserveProcessingMemory("processing+second", "processing+second", policy, bytes, total, total); err != nil || ok {
		t.Fatalf("restart forgot running ladder's reservation: admitted=%v err=%v", ok, err)
	}
	clearProcessingProcessOverride("processing+first", "processing+first")
	if _, ok := getProcessingProcessOverride("processing+first"); !ok {
		t.Fatal("owner cleanup released memory before its stream stopped")
	}
	if err := setProcessingProcessOverride("processing+first", policy, "processing+first", time.Now().Add(time.Hour), 0); err != nil {
		t.Fatal(err)
	}
	clearProcessingProcessOverride("processing+first", "processing+first")
	if ok, err := reserveProcessingMemory("processing+second", "processing+second", policy, bytes, total, total); err != nil || !ok {
		t.Fatalf("queued ladder did not acquire released memory: admitted=%v err=%v", ok, err)
	}
}

func TestProcessingMemoryRefusesOversizeOrPressure(t *testing.T) {
	isolateProcessingOverrideState(t)
	if err := setProcessingProcessOverride("processing+memory", processingSourceProbePolicy, "owner", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	total := uint64(3) * 1024 * memoryMiB
	if _, err := reserveProcessingMemory("processing+memory", "owner", "[]", total, total, total); err == nil {
		t.Fatal("oversized ladder was admitted")
	}
	if ok, err := reserveProcessingMemory("processing+memory", "owner", "[]", 256*memoryMiB, total, 128*memoryMiB); err != nil || ok {
		t.Fatalf("node memory pressure was ignored: %v %v", ok, err)
	}
	if _, err := reserveProcessingMemory("processing+memory", "other-owner", "[]", 256*memoryMiB, total, total); err == nil {
		t.Fatal("another job took the reservation")
	}
}

func TestProcessingMemoryOwnerReplacementKeepsRunningReservation(t *testing.T) {
	isolateProcessingOverrideState(t)
	stream := "processing+replacement"
	if err := setProcessingProcessOverride(stream, fourKLadder(t), "old", time.Now().Add(time.Hour), 1024*memoryMiB); err != nil {
		t.Fatal(err)
	}
	if err := setProcessingProcessOverride(stream, processingSourceProbePolicy, "new", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	clearProcessingProcessOverride(stream, "old")
	processingProcessOverridesMu.Lock()
	record := processingOverrideRecords[stream]
	processingProcessOverridesMu.Unlock()
	if record.JobID != "new" || record.MemoryBytes != 1024*memoryMiB {
		t.Fatalf("replacement forgot the running decoder: %+v", record)
	}
}

func TestProcessingMemorySnapshotHonorsContainerPressure(t *testing.T) {
	for _, capacity := range []uint64{3 * 1024 * memoryMiB, 64 * 1024 * memoryMiB} {
		t.Run(strconv.FormatUint(capacity, 10), func(t *testing.T) {
			files := map[string]string{
				"/proc/meminfo":                 "MemAvailable: 60000000 kB\n",
				"/sys/fs/cgroup/memory.max":     strconv.FormatUint(3*1024*memoryMiB, 10),
				"/sys/fs/cgroup/memory.current": strconv.FormatUint(2*1024*memoryMiB, 10),
			}
			total, available, err := readProcessingMemorySnapshot(capacity, func(path string) ([]byte, error) {
				if value, ok := files[path]; ok {
					return []byte(value), nil
				}
				return nil, os.ErrNotExist
			})
			if err != nil || total != 3*1024*memoryMiB || available != 1024*memoryMiB {
				t.Fatalf("container memory pressure ignored: total=%d available=%d err=%v", total, available, err)
			}
		})
	}
}

func TestMemoryAdmissionPreservesPermanentSourceFailure(t *testing.T) {
	for name, input := range map[string]error{
		"invalid media":  errors.New("processing source has no playable tracks"),
		"invalid policy": errors.New("invalid processes JSON"),
	} {
		t.Run(name, func(t *testing.T) {
			if got := processingMemoryResultStatus(input); got != "failed" {
				t.Fatalf("permanent admission failure was made retryable: got %s", got)
			}
		})
	}
	if got := processingMemoryResultStatus(&processingCapacityError{reason: "busy now"}); got != processingResultRetryable {
		t.Fatalf("transient capacity refusal was not retryable: %s", got)
	}
}
