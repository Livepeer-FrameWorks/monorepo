package state

import (
	"testing"
)

// TestSetStreamInstanceInputs_PreservesOtherFields locks the contract
// that the narrow setter doesn't clobber telemetry-fed fields like
// TotalConnections / BytesUp / BytesDown the way UpdateNodeStats would.
// Admission-side and drain-side callers don't know those values; they
// only want to flip presence.
func TestSetStreamInstanceInputs_PreservesOtherFields(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(func() { ResetDefaultManagerForTests() })

	sm.UpdateNodeStats("stream-x", "node-A", 17, 1, 1024, 2048, false)

	sm.SetStreamInstanceInputs("stream-x", "node-A", 0)

	inst := sm.GetStreamInstances("stream-x")["node-A"]
	if inst.Inputs != 0 {
		t.Errorf("Inputs = %d, want 0", inst.Inputs)
	}
	if inst.TotalConnections != 17 {
		t.Errorf("TotalConnections clobbered: %d, want 17", inst.TotalConnections)
	}
	if inst.BytesUp != 1024 {
		t.Errorf("BytesUp clobbered: %d, want 1024", inst.BytesUp)
	}
	if inst.BytesDown != 2048 {
		t.Errorf("BytesDown clobbered: %d, want 2048", inst.BytesDown)
	}
}

// TestSetStreamInstanceInputs_CreatesInstanceIfMissing covers the admit-
// side first-time case: AdmitAndReserve accepts a PUSH_REWRITE on a new
// owner before any STREAM_BUFFER has created the instance row. The
// setter must create it so the balancer can see Inputs=1.
func TestSetStreamInstanceInputs_CreatesInstanceIfMissing(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(func() { ResetDefaultManagerForTests() })

	sm.SetStreamInstanceInputs("stream-y", "node-B", 1)

	inst, ok := sm.GetStreamInstances("stream-y")["node-B"]
	if !ok {
		t.Fatal("instance not created")
	}
	if inst.Inputs != 1 {
		t.Errorf("Inputs = %d, want 1", inst.Inputs)
	}
}

// TestSetStreamInstanceInputs_EmptyArgsNoOp guards the cheap input
// validation — the callers (admit/drain) protect against zero strings
// but the setter must also not blow up if a bad call sneaks through.
func TestSetStreamInstanceInputs_EmptyArgsNoOp(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(func() { ResetDefaultManagerForTests() })
	sm.SetStreamInstanceInputs("", "node-A", 1)
	sm.SetStreamInstanceInputs("stream", "", 1)
	if len(sm.GetAllStreamInstances()) != 0 {
		t.Error("empty-arg calls created state")
	}
}
