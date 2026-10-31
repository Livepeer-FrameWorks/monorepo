package state

// SetNodeClusterObserver installs a nonblocking observer for peer-replicated
// node identities. Identity is a discovery hint; assignment authority remains
// with Quartermaster.
func (sm *StreamStateManager) SetNodeClusterObserver(observer func(string)) {
	sm.mu.Lock()
	sm.nodeClusterObserver = observer
	sm.mu.Unlock()
}

func (sm *StreamStateManager) notifyNodeCluster(nodeID string) {
	sm.mu.RLock()
	observer := sm.nodeClusterObserver
	clusterID := ""
	if node := sm.nodes[nodeID]; node != nil {
		clusterID = node.ClusterID
	}
	sm.mu.RUnlock()
	if observer != nil && clusterID != "" {
		observer(clusterID)
	}
}
