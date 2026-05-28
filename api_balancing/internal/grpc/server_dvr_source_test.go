package grpc

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestWaitForStreamSourceWithHintUsesHealthyTriggerNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeInfo("edge-node-1", "http://edge.example/view", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-node-1", true)

	nodeID, baseURL, ok := waitForStreamSourceWithHint(context.Background(), "stream-not-yet-in-lifecycle", "edge-node-1", time.Millisecond)
	if !ok {
		t.Fatal("expected source-node hint to be accepted")
	}
	if nodeID != "edge-node-1" {
		t.Fatalf("expected edge-node-1, got %q", nodeID)
	}
	if baseURL != "http://edge.example/view" {
		t.Fatalf("expected hinted base URL, got %q", baseURL)
	}
}

func TestWaitForStreamSourceWithHintFallsBackToObservedSource(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeInfo("edge-node-2", "http://edge2.example/view", true, nil, nil, "", "", nil)
	sm.TouchNode("edge-node-2", true)
	sm.UpdateNodeStats("demo_stream", "edge-node-2", 1, 1, 0, 0, false)

	nodeID, baseURL, ok := waitForStreamSourceWithHint(context.Background(), "demo_stream", "missing-node", time.Millisecond)
	if !ok {
		t.Fatal("expected observed stream source fallback")
	}
	if nodeID != "edge-node-2" {
		t.Fatalf("expected edge-node-2, got %q", nodeID)
	}
	if baseURL != "http://edge2.example/view" {
		t.Fatalf("expected observed base URL, got %q", baseURL)
	}
}

func TestDVRSourceStreamNameReturnsBareWhenSourceIsMistNative(t *testing.T) {
	// Bare observed name + no registry entry → bare runtime name. The
	// previous behavior silently defaulted to live+<internal>, which
	// mis-routed DVR for mist_native sources (their canonical Mist source
	// is the bare internal name, not live+<internal>). This is the
	// central bug the runtime-name refactor exists to fix.
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateStreamFromBuffer("demo_stream", "demo_stream", "edge-node-1", "tenant-1", "FULL", "")

	got := dvrSourceStreamName("demo_stream")
	if got != "demo_stream" {
		t.Fatalf("dvrSourceStreamName() = %q, want bare demo_stream (no silent live+ default)", got)
	}
}

func TestDVRSourceStreamNamePreservesConcretePrefixedObservation(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.UpdateStreamFromBuffer("pull+demo_stream", "demo_stream", "edge-node-1", "tenant-1", "FULL", "")

	got := dvrSourceStreamName("demo_stream")
	if got != "pull+demo_stream" {
		t.Fatalf("dvrSourceStreamName() = %q, want pull+demo_stream", got)
	}

	if fallback := control.MistSourceNameForIngestMode("demo_stream", "push"); fallback != "live+demo_stream" {
		t.Fatalf("fallback source = %q", fallback)
	}
}

// TestClipLiveSourceStreamNameUsesRegistryRuntimeName covers the happy
// path: when the registry holds a hydrated entry for the source stream,
// clipLiveSourceStreamName returns its derived runtime name without going
// back through Commodore.
func TestClipLiveSourceStreamNameUsesRegistryRuntimeName(t *testing.T) {
	priorRegistry := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })
	r.UpsertLocalSource(control.StreamEntry{
		InternalName: "demo_stream",
		IngestMode:   control.IngestPush,
	})

	got, err := clipLiveSourceStreamName(context.Background(), &pb.CreateClipRequest{StreamInternalName: "demo_stream"})
	if err != nil {
		t.Fatalf("clipLiveSourceStreamName() error = %v", err)
	}
	if got != "live+demo_stream" {
		t.Fatalf("clipLiveSourceStreamName() = %q, want live+demo_stream", got)
	}
}

// TestClipLiveSourceStreamNameMistNativeRegistry covers the mist-native
// case: registry resolves to the bare internal name, clip uses it as
// the source. A silent push default here would mis-route the clip to
// a non-existent live+<x> stream.
func TestClipLiveSourceStreamNameMistNativeRegistry(t *testing.T) {
	priorRegistry := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })
	r.UpsertLocalSource(control.StreamEntry{
		InternalName: "60546679b497415db2338cd5cae54992",
		IngestMode:   control.IngestMistNative,
	})

	got, err := clipLiveSourceStreamName(context.Background(), &pb.CreateClipRequest{
		StreamInternalName: "60546679b497415db2338cd5cae54992",
	})
	if err != nil {
		t.Fatalf("clipLiveSourceStreamName() error = %v", err)
	}
	if got != "60546679b497415db2338cd5cae54992" {
		t.Fatalf("clipLiveSourceStreamName() = %q, want bare internal_name", got)
	}
}

// TestClipLiveSourceStreamNameIgnoresArtifactPlaybackID locks the prod-
// incident regression: CreateClipRequest.playback_id carries the new clip
// artifact's playback id, NOT the source stream's. Source routing must
// consult stream_internal_name only — passing an artifact playback_id
// must never reach a Commodore artifact lookup or affect source routing.
func TestClipLiveSourceStreamNameIgnoresArtifactPlaybackID(t *testing.T) {
	priorRegistry := control.StreamRegistryInstance
	r := control.NewStreamRegistry(nil, "cluster-a", time.Minute)
	control.SetStreamRegistry(r)
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })
	r.UpsertLocalSource(control.StreamEntry{
		InternalName: "demo_stream",
		IngestMode:   control.IngestPush,
	})

	playbackID := "clip-artifact-playback"
	got, err := clipLiveSourceStreamName(context.Background(), &pb.CreateClipRequest{
		StreamInternalName: "demo_stream",
		PlaybackId:         &playbackID,
	})
	if err != nil {
		t.Fatalf("clipLiveSourceStreamName() error = %v", err)
	}
	if got != "live+demo_stream" {
		t.Fatalf("clipLiveSourceStreamName() = %q, want live+demo_stream (playback_id must be ignored)", got)
	}
}

// TestClipLiveSourceStreamNameFailsClosedOnUnresolvableSource locks the
// no-silent-push-default contract: when neither the registry nor a
// Commodore client can resolve the source stream, the clip dispatch must
// refuse rather than guess live+<internal>. The prior behaviour silently
// mis-routed mist-native sources to live+.
func TestClipLiveSourceStreamNameFailsClosedOnUnresolvableSource(t *testing.T) {
	priorRegistry := control.StreamRegistryInstance
	control.SetStreamRegistry(control.NewStreamRegistry(nil, "cluster-a", time.Minute))
	t.Cleanup(func() { control.SetStreamRegistry(priorRegistry) })

	_, err := clipLiveSourceStreamName(context.Background(), &pb.CreateClipRequest{StreamInternalName: "demo_stream"})
	if err == nil {
		t.Fatal("expected fail-closed error when source cannot be resolved")
	}
}
