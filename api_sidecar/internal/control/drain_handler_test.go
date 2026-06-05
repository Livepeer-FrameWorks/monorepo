package control

import (
	"testing"

	sidecarcfg "frameworks/api_sidecar/internal/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestHandleDrainStream_EmptyRuntimeName_NoOp covers the cheap guard. An
// empty runtime name must not touch Mist or emit a response — sending a
// response without a runtime would leak ambiguity into the Foghorn relay
// table since DrainStreamResponse keys on runtime_name.
func TestHandleDrainStream_EmptyRuntimeName_NoOp(t *testing.T) {
	prev := currentConfig
	currentConfig = nil
	t.Cleanup(func() { currentConfig = prev })

	var sent []*ipcpb.ControlMessage
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{RuntimeName: ""}, func(m *ipcpb.ControlMessage) {
		sent = append(sent, m)
	})

	if len(sent) != 0 {
		t.Fatalf("empty runtime should not respond; got %d messages", len(sent))
	}
}

// TestHandleDrainStream_ConfigMissing_ReportsError covers the bootstrap-
// race case where Foghorn dispatches a drain before the sidecar has
// installed its config. Must emit a response so Foghorn's HA relay can
// release the in-flight request, with an error message that makes the
// failure debuggable.
func TestHandleDrainStream_ConfigMissing_ReportsError(t *testing.T) {
	prev := currentConfig
	currentConfig = nil
	t.Cleanup(func() { currentConfig = prev })

	var got *ipcpb.DrainStreamResponse
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{RuntimeName: "live+abc"}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})

	if got == nil {
		t.Fatal("expected response")
	}
	if got.GetRuntimeName() != "live+abc" {
		t.Errorf("runtime echo = %q, want live+abc", got.GetRuntimeName())
	}
	if got.GetError() == "" {
		t.Error("expected error message when config missing")
	}
	if got.GetUnloaded() {
		t.Error("unloaded must be false when config missing")
	}
}

// TestHandleDrainStream_HappyPath confirms the operational sequence on
// takeover: StopSessions (boot viewers off the stale buffer so they
// reselect via PLAY_REWRITE), NukeStream (the correct API for wildcard
// instances — deletestream is a no-op on runtime-only entries), and
// ClearDVRSourceOverride (so the next start doesn't pull from a gone
// source). All three are required; missing any one is a known
// foot-gun the audit flagged.
func TestHandleDrainStream_HappyPath(t *testing.T) {
	mock := newMockMistServer(t)
	prev := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL}
	t.Cleanup(func() { currentConfig = prev })

	// Pre-seed an override so we can assert it's cleared.
	RegisterDVRSourceOverride("live+drain-target", "dtsc://old-node/live+drain-target")
	t.Cleanup(func() { ClearDVRSourceOverride("live+drain-target") })

	var got *ipcpb.DrainStreamResponse
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{
		RuntimeName: "live+drain-target",
		Reason:      "takeover_test",
	}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})

	if got == nil {
		t.Fatal("expected response")
	}
	if !got.GetUnloaded() {
		t.Errorf("unloaded = false on happy path; want true (response: %+v)", got)
	}
	if got.GetError() != "" {
		t.Errorf("unexpected error: %q", got.GetError())
	}

	// Mist must have received both ops in either order.
	if calls := mock.callsContainingKey("stop_sessions"); len(calls) != 1 {
		t.Errorf("want 1 stop_sessions call, got %d (requests=%+v)", len(calls), mock.requests)
	}
	if calls := mock.callsContainingKey("nuke_stream"); len(calls) != 1 {
		t.Errorf("want 1 nuke_stream call, got %d (requests=%+v)", len(calls), mock.requests)
	}

	// Override must be cleared.
	if _, ok := GetDVRSourceOverride("live+drain-target"); ok {
		t.Error("DVR source override survived drain; takeover would pull from gone source")
	}
}

// TestHandleDVRUpdateSource_EmptyHash_NoOp guards the dispatch boundary.
// An empty dvr_hash is unaddressable — the response would be keyed by
// empty string and ambiguate Foghorn's relay table.
func TestHandleDVRUpdateSource_EmptyHash_NoOp(t *testing.T) {
	var sent []*ipcpb.ControlMessage
	handleDVRUpdateSource(logging.NewLogger(), &ipcpb.DVRUpdateSourceRequest{DvrHash: ""}, func(m *ipcpb.ControlMessage) {
		sent = append(sent, m)
	})
	if len(sent) != 0 {
		t.Fatalf("empty hash should not respond; got %d", len(sent))
	}
}

// TestHandleDVRUpdateSource_MissingJob_Idempotent: Foghorn dispatches one
// DVR-update-source per takeover. If the named DVR isn't running on this
// node (e.g. the artifact was cleaned up between artifact lookup and
// dispatch arrival), refresh=false + no error keeps the takeover path
// from spuriously alarming.
func TestHandleDVRUpdateSource_MissingJob_Idempotent(t *testing.T) {
	prevDM := dvrManager
	dvrManager = &DVRManager{
		logger: logging.NewLogger(),
		jobs:   map[string]*DVRJob{},
	}
	dvrManagerOnce.Do(func() {}) // mark Once as done so initDVRManager() inside handler is a no-op
	t.Cleanup(func() { dvrManager = prevDM })

	var got *ipcpb.DVRUpdateSourceResponse
	handleDVRUpdateSource(logging.NewLogger(), &ipcpb.DVRUpdateSourceRequest{
		DvrHash:           "unknown-hash",
		SourceRuntimeName: "live+x",
		SourceBaseUrl:     "dtsc://new-node/live+x",
	}, func(m *ipcpb.ControlMessage) {
		got = m.GetDvrUpdateSourceResponse()
	})

	if got == nil {
		t.Fatal("expected response")
	}
	if got.GetDvrHash() != "unknown-hash" {
		t.Errorf("hash echo = %q", got.GetDvrHash())
	}
	if got.GetRefreshed() {
		t.Error("Refreshed should be false for unknown job")
	}
	if got.GetError() != "" {
		t.Errorf("unexpected error: %q", got.GetError())
	}
}
