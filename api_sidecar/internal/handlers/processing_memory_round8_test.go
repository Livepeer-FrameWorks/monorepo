package handlers

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestBusyMemoryMustPreserveLookahead(t *testing.T) {
	isolateProcessingOverrideState(t)
	const stream, job = "processing+busy-quality", "busy-quality"
	policy := fourKLadder(t)
	var processes []map[string]any
	if err := json.Unmarshal([]byte(policy), &processes); err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		delete(process, "lookahead")
		process["encode_threads"], process["decode_threads"] = 1, 1
	}
	retained, err := json.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	source := mist.SourceMediaInfo{Width: 3840, Height: 2160}
	minimum, err := mist.EstimateProcessingMemory(string(retained), source)
	if err != nil {
		t.Fatal(err)
	}
	total := uint64(8) * 1024 * memoryMiB
	free := minimum - 64*memoryMiB
	if err = setProcessingProcessOverride("processing+other-quality", "[]", "other", time.Now().Add(time.Hour), processingMemoryBudget(total)-free); err != nil {
		t.Fatal(err)
	}
	if err = setProcessingProcessOverride(stream, processingSourceProbePolicy, job, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	h := &ProcessingJobHandler{memorySnapshot: func() (uint64, uint64, error) { return total, total, nil }}
	admitted, err := h.admitProcessingPolicy(discardLog(), &ipcpb.ProcessingJobRequest{JobId: job}, stream, string(retained), source, nil, func(*ipcpb.ControlMessage) {})
	var capacity *processingCapacityError
	if !errors.As(err, &capacity) || capacity.nodeTooSmall || admitted != "" {
		t.Fatalf("busy memory must return temporary capacity wait: policy=%s err=%v", admitted, err)
	}
}
