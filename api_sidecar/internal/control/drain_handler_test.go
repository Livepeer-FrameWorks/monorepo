package control

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	RecordAdmittedIngestGeneration("live+abc", "prior-err", 101)
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{RuntimeName: "live+abc", SourceGeneration: "gen-err", PriorOwnerSourceGeneration: "prior-err"}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})

	if got == nil {
		t.Fatal("expected response")
	}
	if got.GetRuntimeName() != "live+abc" {
		t.Errorf("runtime echo = %q, want live+abc", got.GetRuntimeName())
	}
	// The obligation identity must be echoed even on the error path — a response without it cannot
	// be correlated to its durable drain leg and the obligation would redispatch forever.
	if got.GetSourceGeneration() != "gen-err" {
		t.Errorf("source_generation echo = %q, want gen-err", got.GetSourceGeneration())
	}
	if got.GetError() == "" {
		t.Error("expected error message when config missing")
	}
	if got.GetUnloaded() {
		t.Error("unloaded must be false when config missing")
	}
}

// TestHandleDrainStream_HappyPath confirms the operational sequence when a
// replacement publisher session displaces the prior owner: StopSessions
// (boot viewers off the stale buffer so they reselect via PLAY_REWRITE),
// NukeStream (the correct API for wildcard instances — deletestream is a
// no-op on runtime-only entries), and ClearDVRSourceOverride (so the next
// start doesn't pull from a gone source). All three are required; missing
// any one leaves stale state.
func TestHandleDrainStream_HappyPath(t *testing.T) {
	mock := newMockMistServer(t)
	prev := currentConfig
	currentConfig = &sidecarcfg.HelmsmanConfig{MistServerURL: mock.srv.URL}
	t.Cleanup(func() { currentConfig = prev })

	// Pre-seed an override so we can assert it's cleared.
	RegisterDVRSourceOverride("live+drain-target", "dtsc://old-node/live+drain-target")
	t.Cleanup(func() { ClearDVRSourceOverride("live+drain-target") })

	var got *ipcpb.DrainStreamResponse
	RecordAdmittedIngestGeneration("live+drain-target", "prior-ok", 102)
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{
		RuntimeName:                "live+drain-target",
		Reason:                     "publisher_replaced_test",
		SourceGeneration:           "gen-ok",
		PriorOwnerSourceGeneration: "prior-ok",
	}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})

	if got == nil {
		t.Fatal("expected response")
	}
	if !got.GetUnloaded() {
		t.Errorf("unloaded = false on happy path; want true (response: %+v)", got)
	}
	if got.GetSourceGeneration() != "gen-ok" {
		t.Errorf("source_generation echo = %q, want gen-ok (the drain leg cannot complete without it)", got.GetSourceGeneration())
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

func TestHandleDrainStream_UnknownGenerationFailsClosed(t *testing.T) {
	const runtimeName = "live+drain-unknown-generation"
	var got *ipcpb.DrainStreamResponse
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{
		RuntimeName:                runtimeName,
		SourceGeneration:           "replacement-obligation",
		PriorOwnerSourceGeneration: "unproven-prior-generation",
	}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})
	if got == nil || got.GetError() == "" || got.GetUnloaded() {
		t.Fatalf("unknown local generation must fail closed without touching Mist, got %+v", got)
	}
}

func TestHandleDrainStream_SuccessorGenerationFencesLateCommand(t *testing.T) {
	const runtimeName = "live+drain-successor-fence"
	RecordAdmittedIngestGeneration(runtimeName, "successor-generation", 103)
	var got *ipcpb.DrainStreamResponse
	handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{
		RuntimeName:                runtimeName,
		SourceGeneration:           "replacement-obligation",
		PriorOwnerSourceGeneration: "retired-generation",
	}, func(m *ipcpb.ControlMessage) {
		got = m.GetDrainStreamResponse()
	})
	if got == nil || got.GetError() != "" || got.GetUnloaded() {
		t.Fatalf("generation-fenced drain should complete without touching Mist, got %+v", got)
	}
	if got.GetSourceGeneration() != "replacement-obligation" {
		t.Fatalf("source generation echo = %q", got.GetSourceGeneration())
	}
}

func TestHandleDrainStream_SerializesSuccessorAdmissionWithMistMutation(t *testing.T) {
	const runtimeName = "live+drain-generation-serialization"
	RecordAdmittedIngestGeneration(runtimeName, "prior-generation", 104)
	nukeStarted := make(chan struct{})
	releaseNuke := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var command map[string]any
		if raw := r.URL.Query().Get("command"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &command)
		} else {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &command)
		}
		if _, ok := command["authorize"]; ok {
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		}
		if _, ok := command["nuke_stream"]; ok {
			close(nukeStarted)
			<-releaseNuke
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	withConfig(t, &sidecarcfg.HelmsmanConfig{MistServerURL: srv.URL})

	drainDone := make(chan struct{})
	go func() {
		handleDrainStream(logging.NewLogger(), &ipcpb.DrainStreamRequest{
			RuntimeName:                runtimeName,
			SourceGeneration:           "replacement-obligation",
			PriorOwnerSourceGeneration: "prior-generation",
		}, func(*ipcpb.ControlMessage) {})
		close(drainDone)
	}()
	select {
	case <-nukeStarted:
	case <-time.After(5 * time.Second):
		close(releaseNuke)
		t.Fatal("drain never reached the destructive Mist operation")
	}

	recordDone := make(chan struct{})
	go func() {
		RecordAdmittedIngestGeneration(runtimeName, "successor-generation", 105)
		close(recordDone)
	}()
	select {
	case <-recordDone:
		close(releaseNuke)
		<-drainDone
		t.Fatal("successor generation was recorded while prior-generation drain still mutated Mist")
	case <-time.After(100 * time.Millisecond):
		close(releaseNuke)
	}
	<-drainDone
	<-recordDone
	if current, known := admittedIngestGeneration(runtimeName); !known || current != "successor-generation" {
		t.Fatalf("current generation = %q, known=%v", current, known)
	}
}
