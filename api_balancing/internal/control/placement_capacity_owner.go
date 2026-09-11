package control

import "frameworks/api_balancing/internal/balancer"

// PlacementCapacityOwner follows Quartermaster reconnects through the atomic
// client holder; one preview retains one owner client for both required reads.
func PlacementCapacityOwner() balancer.PlacementCapacityOwner {
	client := servedClustersClient.Load()
	if client == nil {
		return nil
	}
	return client
}
