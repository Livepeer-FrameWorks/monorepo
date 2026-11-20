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

// A cluster identity arriving after stream presence can complete the source's
// federation proof, so re-evaluate the streams this node already carries.
func (sm *StreamStateManager) notifyNodeStreamsChanged(nodeID string) {
	sm.mu.RLock()
	var names []string
	for name, instances := range sm.streamInstances {
		if instances[nodeID] != nil {
			names = append(names, name)
		}
	}
	sm.mu.RUnlock()
	for _, name := range names {
		sm.notifyStreamChanged(name)
	}
}
