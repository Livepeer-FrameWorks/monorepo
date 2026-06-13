package control

import (
	"os"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// EventNodeRestarting is the NodeLifecycleUpdate event_type Helmsman sends
// before a planned exit (signal-driven shutdown or self-update restart).
// IsHealthy stays true: the data plane (MistServer, Caddy) keeps serving
// while the process restarts, so Foghorn holds the node's health for a
// bounded reconnect window instead of publishing unhealthy to DNS. A crash
// sends nothing, which keeps the immediate-unhealthy disconnect path.
const EventNodeRestarting = "node_restarting"

// AnnounceRestart tells Foghorn this Helmsman is about to exit and expects
// to reconnect shortly. Callers should give the stream a brief drain pause
// (~500ms) before exiting.
func AnnounceRestart(logger logging.Logger) error {
	nodeID := GetCurrentNodeID()
	if nodeID == "" {
		nodeID = os.Getenv("NODE_ID")
		if nodeID == "" {
			nodeID = "unknown-node"
		}
	}

	trigger := &ipcpb.MistTrigger{
		TriggerType: "NODE_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		TriggerPayload: &ipcpb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: &ipcpb.NodeLifecycleUpdate{
				NodeId:    nodeID,
				IsHealthy: true,
				EventType: EventNodeRestarting,
				Timestamp: time.Now().Unix(),
			},
		},
	}

	_, err := SendMistTrigger(trigger, logger)
	return err
}
