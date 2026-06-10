package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// handleDVRStart/handleDVRStop are the Foghorn-facing dispatchers over the DVR
// manager. The contract under test is the failure surface: when the manager
// can't start or stop a recording, the handler MUST emit a terminal
// DVRStopped{status:"failed"} back to Foghorn (echoing dvr_hash + request_id)
// so Foghorn's HA relay releases the in-flight request instead of hanging.

// withTestDVRManager swaps in a test manager. It arms the sync.Once first so
// the handler's own initDVRManager() call is a no-op and can't replace it.
func withTestDVRManager(t *testing.T, dm *DVRManager) {
	t.Helper()
	dvrManagerOnce.Do(func() {})
	prev := dvrManager
	dvrManager = dm
	t.Cleanup(func() { dvrManager = prev })
}

func TestHandleDVRStartAlreadyActiveReportsFailure(t *testing.T) {
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        map[string]*DVRJob{"dvr-1": {DVRHash: "dvr-1", Status: "recording"}},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	var got *ipcpb.DVRStopped
	handleDVRStart(logging.NewLogger(), &ipcpb.DVRStartRequest{
		DvrHash:   "dvr-1",
		StreamId:  "stream-1",
		RequestId: "req-1",
		Config:    &ipcpb.DVRConfig{Format: "hls"},
	}, func(m *ipcpb.ControlMessage) { got = m.GetDvrStopped() })

	if got == nil {
		t.Fatal("a failed start must emit a terminal DVRStopped so Foghorn can release the request")
	}
	if got.GetStatus() != "failed" || got.GetDvrHash() != "dvr-1" || got.GetRequestId() != "req-1" {
		t.Fatalf("failure notification mismatch: %+v", got)
	}
	if got.GetError() == "" {
		t.Fatal("failure must carry a diagnostic error")
	}
}

// A nil sender (no control stream) must not panic on the failure path.
func TestHandleDVRStartNilSenderNoPanic(t *testing.T) {
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        map[string]*DVRJob{"dvr-1": {DVRHash: "dvr-1", Status: "recording"}},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	handleDVRStart(logging.NewLogger(), &ipcpb.DVRStartRequest{DvrHash: "dvr-1", RequestId: "req-1"}, nil)
}

func TestHandleDVRStopUnknownHashReportsFailure(t *testing.T) {
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        map[string]*DVRJob{},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	var got *ipcpb.DVRStopped
	handleDVRStop(logging.NewLogger(), &ipcpb.DVRStopRequest{
		DvrHash:   "dvr-nonexistent-xyz",
		RequestId: "req-2",
	}, func(m *ipcpb.ControlMessage) { got = m.GetDvrStopped() })

	if got == nil {
		t.Fatal("stopping an unknown, unrecoverable recording must still emit a terminal DVRStopped")
	}
	if got.GetStatus() != "failed" || got.GetDvrHash() != "dvr-nonexistent-xyz" || got.GetRequestId() != "req-2" {
		t.Fatalf("failure notification mismatch: %+v", got)
	}
}

func TestHandleDVRStopNilSenderNoPanic(t *testing.T) {
	dm := &DVRManager{
		logger:      logging.NewLogger(),
		jobs:        map[string]*DVRJob{},
		storagePath: t.TempDir(),
	}
	withTestDVRManager(t, dm)

	handleDVRStop(logging.NewLogger(), &ipcpb.DVRStopRequest{DvrHash: "dvr-nonexistent-xyz", RequestId: "req-2"}, nil)
}
