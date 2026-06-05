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
		registry.conns[nodeID] = &conn{stream: stream}
	}
	return func() { registry = prev }
}
