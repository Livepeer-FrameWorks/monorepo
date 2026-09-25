package jobs

import (
	"encoding/json"
	"testing"
)

type recordingGatewayResolver struct{ broadcasters, workload int }

func (r *recordingGatewayResolver) ApplyLivepeerBroadcasters(processesJSON string, _ []string) string {
	r.broadcasters++
	return processesJSON
}

func (r *recordingGatewayResolver) ApplyLivepeerWorkload(processesJSON, _ string) string {
	r.workload++
	return processesJSON
}

// A chapter finalize runs its processes once over a drained source. Mist's
// default restart policy restarts a Thumbs that finished cleanly; the new
// instance re-adds JPEG/thumbvtt tracks after the EBML header was written
// ("EBML track 5 (JPEG) was not declared in the recording header") or raises
// the output expectation to tracks that never come, and the finalize fails.
// Every process the queue sends must therefore carry restart_type "disabled",
// even when the tenant policy asks for another restart type.
func TestFinalizeProcessesJSONDisablesRestarts(t *testing.T) {
	policy := `[{"process":"Thumbs","inconsequential":true,"exit_unmask":true},` +
		`{"process":"AV","codec":"opus","restart_type":"fixed"}]`
	resolver := &recordingGatewayResolver{}
	q := &ChapterFinalizationQueue{gatewayResolver: resolver}

	var procs []map[string]any
	if err := json.Unmarshal([]byte(q.finalizeProcessesJSON(policy)), &procs); err != nil {
		t.Fatalf("finalize config is not a process list: %v", err)
	}
	if len(procs) != 2 {
		t.Fatalf("expected both processes to survive, got %d", len(procs))
	}
	for _, proc := range procs {
		if proc["restart_type"] != "disabled" {
			t.Fatalf("process %v would be restarted by the Mist supervisor after finishing its pass", proc["process"])
		}
	}
	if resolver.broadcasters != 1 || resolver.workload != 1 {
		t.Fatalf("gateway resolution must still run once each, got broadcasters=%d workload=%d", resolver.broadcasters, resolver.workload)
	}
	if got := q.finalizeProcessesJSON(""); got != "" {
		t.Fatalf("an empty policy must stay empty, got %q", got)
	}
}
