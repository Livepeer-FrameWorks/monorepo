package triggers

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// installRegistryForTest swaps a fresh StreamRegistry into the package
// global and restores the previous one on cleanup. Required because
// handleStreamEnd reads control.StreamRegistryInstance directly.
func installRegistryForTest(t *testing.T) *control.StreamRegistry {
	t.Helper()
	prev := control.StreamRegistryInstance
	reg := control.NewStreamRegistry(nil, "cluster-A", time.Minute)
	control.StreamRegistryInstance = reg
	t.Cleanup(func() { control.StreamRegistryInstance = prev })
	return reg
}

// minimalProcessorForStreamEnd builds a Processor wired only with the
// pieces handleStreamEnd needs: logger, streamCache (via NewProcessor),
// no decklog (nil-tolerant), no peerNotifier (nil-checked), no
// quartermaster (cluster-owner cache nil-safe).
func minimalProcessorForStreamEnd(t *testing.T) *Processor {
	t.Helper()
	return NewProcessor(logging.NewLogger(), nil, nil, nil, nil)
}

// TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode locks the
// belt-and-suspenders behavior: when PUSH_INPUT_CLOSE is lost
// (Helmsman crash, transport blip, parse error), STREAM_END must
// still flip SourceActive=false on the registry so the next
// PUSH_REWRITE on this stream is admitted via the resume/takeover
// path instead of rejected as a duplicate.
func TestHandleStreamEnd_FlipsSourceInactiveOnMatchingNode(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "stream-end-1"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

	p := minimalProcessorForStreamEnd(t)
	_, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId: "node-A",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamEnd returned err: %v", err)
	}

	// SourceActive must be false now, so a subsequent PUSH_REWRITE
	// from node-A succeeds via the resume path.
	r := reg.AdmitAndReserve(internal, "node-A", nil)
	if r.Decision != control.AdmissionAcceptResume {
		t.Errorf("post-STREAM_END admit decision = %v, want AdmissionAcceptResume", r.Decision)
	}
}

// TestHandleStreamEnd_StaleNodeDoesNotClearLiveOwner guards the
// node-match invariant: a STREAM_END originating from a node that's
// not the recorded owner must not clear that owner's live state.
// Without the guard, a stale/misrouted STREAM_END could let a second
// publisher steal admission via AcceptResume.
func TestHandleStreamEnd_StaleNodeDoesNotClearLiveOwner(t *testing.T) {
	reg := installRegistryForTest(t)
	state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	const internal = "stream-end-2"
	if r := reg.AdmitAndReserve(internal, "node-A", nil); r.Decision != control.AdmissionAcceptNew {
		t.Fatalf("seed admit: %v", r.Decision)
	}

	p := minimalProcessorForStreamEnd(t)
	// STREAM_END from a DIFFERENT node than the recorded owner.
	_, _, err := p.handleStreamEnd(&ipcpb.MistTrigger{
		NodeId: "node-B",
		TriggerPayload: &ipcpb.MistTrigger_StreamEnd{
			StreamEnd: &ipcpb.StreamEndTrigger{StreamName: "live+" + internal},
		},
	})
	if err != nil {
		t.Fatalf("handleStreamEnd returned err: %v", err)
	}

	// Node-A is still the live owner; a takeover attempt from
	// node-C must still see the live owner and reject.
	r := reg.AdmitAndReserve(internal, "node-C", nil)
	if r.Decision != control.AdmissionRejectDuplicate {
		t.Errorf("post-stale-STREAM_END admit decision = %v, want AdmissionRejectDuplicate (live owner must be preserved)", r.Decision)
	}
}
