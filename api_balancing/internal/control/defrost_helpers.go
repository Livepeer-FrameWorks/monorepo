package control

import (
	"fmt"

	"frameworks/pkg/logging"
)

func controlLogger() logging.Logger {
	if registry != nil && registry.log != nil {
		return registry.log
	}
	return logging.NewLoggerWithService("foghorn")
}

func pickStorageNodeID() (string, error) {
	if loadBalancerInstance == nil {
		return "", fmt.Errorf("load balancer not available")
	}
	nodes := loadBalancerInstance.GetNodes()
	for _, node := range nodes {
		if node.CapStorage && node.IsHealthy {
			return node.NodeID, nil
		}
	}
	return "", fmt.Errorf("no storage nodes available")
}
