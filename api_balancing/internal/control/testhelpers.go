package control

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// SetupTestRegistry creates a temporary connection registry with an optional
// fake stream for the given nodeID. Returns a cleanup function that restores
// the previous registry. Exported for cross-package tests (grpc package).
func SetupTestRegistry(nodeID string, stream ipcpb.HelmsmanControl_ConnectServer) func() {
	prev := registry
	registry = &Registry{conns: make(map[string]*conn), log: logging.NewLogger()}
	if nodeID != "" && stream != nil {
		// Register the fake connection as a CURRENT-protocol sidecar so staged-freeze dispatch (gated at the
		// final owning send) is delivered; tests exercising an old sidecar set protocolVersion explicitly.
		registry.conns[nodeID] = &conn{stream: stream, protocolVersion: FreezeStagedProtocolMin}
	}
	return func() { registry = prev }
}
