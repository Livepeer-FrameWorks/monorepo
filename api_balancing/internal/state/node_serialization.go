package state

import (
	"encoding/json"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func marshalNodeState(node *NodeState) []byte {
	payload, err := json.Marshal(node)
	if err != nil {
		logging.NewLogger().WithError(err).WithField("node_id", node.NodeID).Warn("Could not serialize node state")
		return nil
	}
	return payload
}
