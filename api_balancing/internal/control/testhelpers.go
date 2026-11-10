package control

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

// SetupTestRegistry creates a temporary connection registry with an optional
// fake stream for the given nodeID. Returns a cleanup function that restores
// the previous registry. Exported for cross-package tests (grpc package).
func SetupTestRegistry(nodeID string, stream ipcpb.HelmsmanControl_ConnectServer) func() {
	streams := make(map[string]ipcpb.HelmsmanControl_ConnectServer)
	if nodeID != "" && stream != nil {
		streams[nodeID] = stream
	}
	return SetupTestRegistryNodes(streams)
}

// SetupTestRegistryNodes installs current-protocol streams for multi-node RPC tests.
func SetupTestRegistryNodes(streams map[string]ipcpb.HelmsmanControl_ConnectServer) func() {
	prev := registry
	registry = &Registry{conns: make(map[string]*conn), log: logging.NewLogger()}
	for nodeID, stream := range streams {
		// Register the fake connection as a CURRENT-protocol sidecar so staged-freeze dispatch (gated at the
		// final owning send) is delivered; tests exercising an old sidecar set protocolVersion explicitly.
		registry.conns[nodeID] = &conn{stream: stream, rawNodeID: nodeID, protocolVersion: StopSessionsAckProtocolMin}
	}
	return func() { registry = prev }
}

// CompleteTestStopSessions delivers the edge result through the same correlation
// and connection fences as a registered control stream, for cross-package tests.
func CompleteTestStopSessions(nodeID string, req *ipcpb.StopSessionsRequest, resultError string) bool {
	session, ok := currentNodeSession(nodeID)
	if !ok {
		return false
	}
	if req.GetAuthorizationNonce() == "" && req.GetRemainingTimeoutMs() > 0 {
		return acceptStopSessionsResult(&ipcpb.StopSessionsResult{RequestId: req.GetRequestId(), TenantId: req.GetTenantId(), StreamNames: req.GetStreamNames(), AuthorizationNonce: uuid.NewString()}, session)
	}
	return acceptStopSessionsResult(&ipcpb.StopSessionsResult{RequestId: req.GetRequestId(), TenantId: req.GetTenantId(), StreamNames: req.GetStreamNames(), Error: resultError}, session)
}
