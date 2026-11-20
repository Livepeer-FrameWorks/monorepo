package mist

import (
	"encoding/json"
	"fmt"
	"testing"
)

func memoryTestLadder(t *testing.T) string {
	t.Helper()
	var processes []map[string]any
	for _, height := range []int{2160, 1440, 1080, 720, 480, 360} {
		processes = append(processes, map[string]any{"process": "AV", "codec": "H264", "resolution": fmt.Sprintf("0x%d", height)})
	}
	processes = append(processes, map[string]any{"process": "Thumbs"}, map[string]any{"process": "AV", "codec": "AAC"}, map[string]any{"process": "AV", "codec": "opus"})
	encoded, err := json.Marshal(processes)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestMemoryPlanKeepsQualityAndParallelismOnAmpleNode(t *testing.T) {
	plan, err := PlanProcessingMemory(memoryTestLadder(t), SourceMediaInfo{Width: 3840, Height: 2160}, 24*1024*processingMiB, 16)
	if err != nil || !plan.Fits {
		t.Fatalf("ample-node plan: %+v %v", plan, err)
	}
	var processes []map[string]any
	if err = json.Unmarshal([]byte(plan.ProcessesJSON), &processes); err != nil {
		t.Fatal(err)
	}
	if processOptionInt(processes[0], "encode_threads", 0) < 8 {
		t.Fatalf("largest rung lost parallelism: %s", plan.ProcessesJSON)
	}
	for _, process := range processes {
		if _, overridden := process["lookahead"]; overridden {
			t.Fatalf("ample memory unnecessarily changed preset quality: %s", plan.ProcessesJSON)
		}
	}
}

func TestMemoryPlanFitsConstrainedNodeWithoutBlanketLookaheadRemoval(t *testing.T) {
	const budget = 2560 * processingMiB
	plan, err := PlanProcessingMemory(memoryTestLadder(t), SourceMediaInfo{Width: 3840, Height: 2160}, budget, 2)
	if err != nil || !plan.Fits || plan.Bytes > budget {
		t.Fatalf("3 GiB local plan: %+v %v", plan, err)
	}
	var processes []map[string]any
	if err = json.Unmarshal([]byte(plan.ProcessesJSON), &processes); err != nil {
		t.Fatal(err)
	}
	if processLookahead(processes[5]) == 0 {
		t.Fatalf("smallest rung lost lookahead despite fitting: %s", plan.ProcessesJSON)
	}
}

func TestMemoryPlanRejectsLocalLadderThatNodeCannotFit(t *testing.T) {
	plan, err := PlanProcessingMemory(memoryTestLadder(t), SourceMediaInfo{Width: 3840, Height: 2160}, 512*processingMiB, 2)
	if err != nil || plan.Fits {
		t.Fatalf("oversized local work admitted: %+v %v", plan, err)
	}
}
