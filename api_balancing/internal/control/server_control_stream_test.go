package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestCleanupControlDisconnectDoesNotRemoveNewerLocalStream(t *testing.T) {
	currentStream := &captureStream{}
	staleStream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", currentStream)
	t.Cleanup(cleanup)

	cleanupControlDisconnect("node-1", "", staleStream, logging.NewLogger())

	registry.mu.RLock()
	got := registry.conns["node-1"]
	registry.mu.RUnlock()
	if got == nil {
		t.Fatal("current stream was removed by stale cleanup")
	}
	if got.stream != currentStream {
		t.Fatal("stale cleanup replaced the current stream")
	}
}

func TestCleanupControlDisconnectRemovesOnlyCurrentStream(t *testing.T) {
	currentStream := &captureStream{}
	cleanup := SetupTestRegistry("node-1", currentStream)
	t.Cleanup(cleanup)

	cleanupControlDisconnect("node-1", "", currentStream, logging.NewLogger())

	registry.mu.RLock()
	got := registry.conns["node-1"]
	registry.mu.RUnlock()
	if got != nil {
		t.Fatal("current stream was not removed")
	}
}
