package state

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestReconcileVirtualViewers_CleansUpAbandonedViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")
	if viewerID == "" {
		t.Fatal("expected viewer ID to be created")
	}

	// Manually set the viewer to ABANDONED with old timestamp (simulating timeout)
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	if viewer == nil {
		sm.mu.Unlock()
		t.Fatal("expected viewer to exist")
	}
	viewer.State = VirtualViewerAbandoned
	viewer.DisconnectTime = time.Now().Add(-10 * time.Minute) // 10 min ago, older than 5 min retention
	sm.mu.Unlock()

	// Verify viewer exists before reconciliation
	stats := sm.GetVirtualViewerStats()
	if stats.Abandoned != 1 {
		t.Fatalf("expected 1 abandoned viewer, got %v", stats.Abandoned)
	}

	// Call ReconcileVirtualViewers - this should clean up the old abandoned viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was cleaned up
	stats = sm.GetVirtualViewerStats()
	if stats.Abandoned != 0 {
		t.Fatalf("expected 0 abandoned viewers after cleanup, got %v", stats.Abandoned)
	}
	if stats.TotalViewers != 0 {
		t.Fatalf("expected 0 total viewers after cleanup, got %v", stats.TotalViewers)
	}
}

func TestReconcileNodeStreamPresenceClearsMissingStreams(t *testing.T) {
	sm := NewStreamStateManager()
	sm.TouchNode("edge-us-1", true)
	sm.UpdateNodeStats("frameworks-demo", "edge-us-1", 3, 1, 100, 200, true)

	cleared := sm.ReconcileNodeStreamPresence("edge-us-1", map[string]struct{}{})
	if len(cleared) != 1 || cleared[0] != "frameworks-demo" {
		t.Fatalf("cleared = %#v, want frameworks-demo", cleared)
	}

	instances := sm.GetStreamInstances("frameworks-demo")
	inst, ok := instances["edge-us-1"]
	if !ok {
		t.Fatal("expected edge instance")
	}
	if inst.Inputs != 0 || inst.TotalConnections != 0 || inst.Status != "offline" || inst.Replicated {
		t.Fatalf("instance after reconcile = %+v, want offline/non-replicated with zero inputs", inst)
	}

	snapshots := sm.GetBalancerNodeSnapshots()
	if len(snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snapshots))
	}
	stream := snapshots[0].Streams["frameworks-demo"]
	if stream.Inputs != 0 || stream.Replicated {
		t.Fatalf("balancer stream = %+v, want zero-input non-replicated", stream)
	}
}

func TestVirtualViewerAggregatePenaltyPersistsToRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisStateStore(client, "test-cluster")
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	if err := sm.EnableRedisSync(context.Background(), store, "instance-a", logging.NewLogger()); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}

	nodeID := "node-1"
	streamName := "tenant+stream"
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "203.0.113.10")
	if viewerID == "" {
		t.Fatal("expected viewer ID")
	}

	nodes, err := store.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes after create: %v", err)
	}
	node := nodes[nodeID]
	if node == nil {
		t.Fatal("expected node state in Redis after virtual viewer create")
	}
	if node.PendingRedirects != 1 {
		t.Fatalf("expected pending redirects to persist, got %d", node.PendingRedirects)
	}
	if node.AddBandwidth == 0 {
		t.Fatal("expected AddBandwidth penalty to persist")
	}

	if !sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, "203.0.113.10", "mist-session-1") {
		t.Fatal("expected virtual viewer confirm")
	}
	nodes, err = store.GetAllNodes()
	if err != nil {
		t.Fatalf("GetAllNodes after confirm: %v", err)
	}
	node = nodes[nodeID]
	if node.PendingRedirects != 0 {
		t.Fatalf("expected pending redirects to clear after confirm, got %d", node.PendingRedirects)
	}
	if node.AddBandwidth != 0 {
		t.Fatalf("expected AddBandwidth penalty to clear after confirm, got %d", node.AddBandwidth)
	}
}

func TestReconcileVirtualViewers_KeepsRecentAbandonedViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")

	// Manually set the viewer to ABANDONED with recent timestamp
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	viewer.State = VirtualViewerAbandoned
	viewer.DisconnectTime = time.Now().Add(-1 * time.Minute) // 1 min ago, within 5 min retention
	sm.mu.Unlock()

	// Call ReconcileVirtualViewers
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was NOT cleaned up (too recent)
	stats := sm.GetVirtualViewerStats()
	if stats.Abandoned != 1 {
		t.Fatalf("expected 1 abandoned viewer (recent), got %v", stats.Abandoned)
	}
}

func TestReconcileVirtualViewers_TimeoutsPendingViewers(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create a virtual viewer (PENDING)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")

	// Manually set old redirect time (simulating >30s ago)
	sm.mu.Lock()
	viewer := sm.virtualViewers[viewerID]
	viewer.RedirectTime = time.Now().Add(-1 * time.Minute) // 1 min ago, older than 30s timeout
	sm.mu.Unlock()

	// Verify viewer is PENDING before reconciliation
	stats := sm.GetVirtualViewerStats()
	if stats.Pending != 1 {
		t.Fatalf("expected 1 pending viewer, got %v", stats.Pending)
	}

	// Call ReconcileVirtualViewers - this should timeout the pending viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was marked as ABANDONED
	stats = sm.GetVirtualViewerStats()
	if stats.Pending != 0 {
		t.Fatalf("expected 0 pending viewers after timeout, got %v", stats.Pending)
	}
	if stats.Abandoned != 1 {
		t.Fatalf("expected 1 abandoned viewer after timeout, got %v", stats.Abandoned)
	}
}

func TestGetViewerDrift(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "test-node-1"
	streamName := "test-stream"

	// Create a node
	sm.TouchNode(nodeID, true)

	// Create and confirm a virtual viewer (ACTIVE)
	viewerID := sm.CreateVirtualViewer(nodeID, streamName, "192.168.1.1")
	sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, "192.168.1.1", "mist-session-1")

	// No real connections reported by Helmsman yet
	drift := sm.GetViewerDrift()
	if drift[nodeID] != 1 {
		t.Fatalf("expected drift of 1 (1 virtual, 0 real), got %v", drift[nodeID])
	}

	// Simulate Helmsman reporting 1 real connection
	sm.UpdateNodeStats(streamName, nodeID, 1, 0, 0, 0, false)

	drift = sm.GetViewerDrift()
	if drift[nodeID] != 0 {
		t.Fatalf("expected drift of 0 (1 virtual, 1 real), got %v", drift[nodeID])
	}

	// Simulate Helmsman reporting 2 real connections (more real than virtual)
	sm.UpdateNodeStats(streamName, nodeID, 2, 0, 0, 0, false)

	drift = sm.GetViewerDrift()
	if drift[nodeID] != -1 {
		t.Fatalf("expected drift of -1 (1 virtual, 2 real), got %v", drift[nodeID])
	}
}

func TestTouchNodeUpdatesLastHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "heartbeat-node"
	sm.TouchNode(nodeID, true)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if node.LastHeartbeat.IsZero() {
		t.Fatal("expected LastHeartbeat to be set")
	}
}

func TestCheckStaleNodesUsesLastHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "stale-node"
	sm.TouchNode(nodeID, true)

	now := time.Now()
	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.LastHeartbeat = now.Add(-2 * time.Minute)
	node.LastUpdate = now
	node.IsHealthy = true
	node.IsStale = false
	sm.mu.Unlock()

	sm.checkStaleNodes()

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to be stale based on heartbeat")
	}
	if node.IsHealthy {
		t.Fatal("expected node to be unhealthy when stale")
	}
}

func TestMetricsUpdateDoesNotPreventStaleness(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "metrics-node"
	sm.TouchNode(nodeID, true)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	node.IsHealthy = true
	node.IsStale = false
	sm.mu.Unlock()

	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]ClassCapacity
	}{
		CPU:                  10,
		RAMMax:               1024,
		RAMCurrent:           512,
		UpSpeed:              100,
		DownSpeed:            100,
		BWLimit:              1000,
		CapIngest:            true,
		CapEdge:              true,
		CapStorage:           false,
		CapProcessing:        false,
		Roles:                []string{"edge"},
		StorageCapacityBytes: 1024,
		StorageUsedBytes:     256,
		ProcessingClasses: map[string]ClassCapacity{
			"video_transcode": {Total: 1, Used: 0},
		},
	})

	sm.checkStaleNodes()

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to remain stale after metrics update")
	}
}

func TestMarkNodeDisconnected(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "disconnect-node"
	sm.TouchNode(nodeID, true)

	sm.MarkNodeDisconnected(nodeID)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if node.IsHealthy {
		t.Fatal("expected node to be unhealthy after disconnect")
	}
	if !node.IsStale {
		t.Fatal("expected node to be stale after disconnect")
	}
}

func TestCheckStaleNodes_EvictsDisconnectedAfterThreshold(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "evict-me"
	sm.TouchNode(nodeID, true)
	sm.MarkNodeDisconnected(nodeID)

	// Right after disconnect the node should still exist
	sm.checkStaleNodes()
	if sm.GetNodeState(nodeID) == nil {
		t.Fatal("expected node to still exist immediately after disconnect")
	}

	// Backdate LastUpdate so the node exceeds the removal threshold
	sm.mu.Lock()
	if n := sm.nodes[nodeID]; n != nil {
		n.LastUpdate = time.Now().Add(-(nodeRemovalThreshold + time.Minute))
	}
	sm.mu.Unlock()

	sm.checkStaleNodes()
	if sm.GetNodeState(nodeID) != nil {
		t.Fatal("expected node to be evicted after exceeding removal threshold")
	}
}

func TestSetNodeInfoDoesNotReviveStaleNode(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "rehydrate-node"
	sm.TouchNode(nodeID, true)

	sm.mu.Lock()
	node := sm.nodes[nodeID]
	node.IsStale = true
	node.IsHealthy = false
	node.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	sm.mu.Unlock()

	sm.SetNodeInfo(nodeID, "http://example.com", true, nil, nil, "us-east", "", nil)

	node = sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected node to remain stale after SetNodeInfo")
	}
}

func TestApplyNodeLifecyclePreservesZeroCoordinateWithValidPair(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	if err := sm.ApplyNodeLifecycle(context.Background(), &ipcpb.NodeLifecycleUpdate{
		NodeId:    "node-zero",
		BaseUrl:   "http://node-zero.example",
		IsHealthy: true,
		Latitude:  52.3676,
		Longitude: 0,
	}); err != nil {
		t.Fatalf("ApplyNodeLifecycle failed: %v", err)
	}

	node := sm.GetNodeState("node-zero")
	if node == nil {
		t.Fatal("expected node state")
	}
	if node.Latitude == nil || *node.Latitude != 52.3676 {
		t.Fatalf("expected latitude 52.3676, got %#v", node.Latitude)
	}
	if node.Longitude == nil || *node.Longitude != 0 {
		t.Fatalf("expected longitude 0, got %#v", node.Longitude)
	}
}

func TestApplyNodeLifecycleKeepsDegradedHeartbeatFresh(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	if err := sm.ApplyNodeLifecycle(context.Background(), &ipcpb.NodeLifecycleUpdate{
		NodeId:    "node-degraded",
		BaseUrl:   "http://node-degraded.example",
		IsHealthy: false,
	}); err != nil {
		t.Fatalf("ApplyNodeLifecycle failed: %v", err)
	}

	node := sm.GetNodeState("node-degraded")
	if node == nil {
		t.Fatal("expected node state")
	}
	if node.LastHeartbeat.IsZero() {
		t.Fatal("expected lifecycle update to refresh LastHeartbeat")
	}
	if node.IsStale {
		t.Fatal("expected fresh degraded node not to be marked stale")
	}
	if node.IsHealthy {
		t.Fatal("expected degraded health to remain visible")
	}
}

func TestNewNodeStartsStaleUntilHeartbeat(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	nodeID := "new-node"
	sm.SetNodeInfo(nodeID, "http://example.com", true, nil, nil, "us-west", "", nil)

	node := sm.GetNodeState(nodeID)
	if node == nil {
		t.Fatal("expected node state to exist")
	}
	if !node.IsStale {
		t.Fatal("expected new node to start stale before heartbeat")
	}
	if node.LastHeartbeat.IsZero() == false {
		t.Fatal("expected LastHeartbeat to be zero before heartbeat")
	}
}

func TestConfirmVirtualViewerByID_MultiTabCorrelation(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-multi-tab"
	streamName := "stream-multi-tab"
	clientIP := "10.10.0.5"

	viewerA := sm.CreateVirtualViewer(nodeID, streamName, clientIP)
	viewerB := sm.CreateVirtualViewer(nodeID, streamName, clientIP)

	confirmed := sm.ConfirmVirtualViewerByID(viewerB, nodeID, streamName, clientIP, "mist-session-b")
	if !confirmed {
		t.Fatal("expected viewerB to be confirmed by ID")
	}

	sm.mu.RLock()
	viewerAState := sm.virtualViewers[viewerA].State
	viewerBState := sm.virtualViewers[viewerB].State
	pending := sm.nodes[nodeID].PendingRedirects
	sm.mu.RUnlock()

	if viewerBState != VirtualViewerActive {
		t.Fatalf("expected viewerB to be active, got %s", viewerBState)
	}
	if viewerAState != VirtualViewerPending {
		t.Fatalf("expected viewerA to remain pending, got %s", viewerAState)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending redirect remaining, got %d", pending)
	}
}

func TestStartVirtualViewerByID_UsesCorrelationID(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-play-rewrite"
	streamName := "stream-play-rewrite"
	clientIP := "10.10.0.6"

	viewerA := sm.CreateVirtualViewer(nodeID, streamName, clientIP)
	viewerB := sm.CreateVirtualViewer(nodeID, streamName, clientIP)

	startedID, started := sm.StartVirtualViewerByID(viewerB, nodeID, streamName, clientIP)
	if !started {
		t.Fatal("expected correlated playback intent to start viewerB")
	}
	if startedID != viewerB {
		t.Fatalf("expected viewerB to start, got %s", startedID)
	}

	_, duplicate := sm.StartVirtualViewerByID(viewerB, nodeID, streamName, clientIP)
	if duplicate {
		t.Fatal("expected duplicate playback intent to be ignored")
	}

	sm.mu.RLock()
	viewerAState := sm.virtualViewers[viewerA].State
	viewerBState := sm.virtualViewers[viewerB].State
	pending := sm.nodes[nodeID].PendingRedirects
	sm.mu.RUnlock()

	if viewerAState != VirtualViewerPending {
		t.Fatalf("expected viewerA to remain pending, got %s", viewerAState)
	}
	if viewerBState != VirtualViewerActive {
		t.Fatalf("expected viewerB to be active, got %s", viewerBState)
	}
	if pending != 1 {
		t.Fatalf("expected 1 pending redirect remaining, got %d", pending)
	}
}

func TestStartVirtualViewerByID_DeduplicatesDirectPlaybackByIP(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-direct-playback"
	streamName := "stream-direct-playback"
	clientIP := "198.51.100.10"

	viewerID, started := sm.StartVirtualViewerByID("", nodeID, streamName, clientIP)
	if !started {
		t.Fatal("expected direct playback intent to start")
	}
	if viewerID == "" {
		t.Fatal("expected generated viewer ID")
	}

	againID, duplicate := sm.StartVirtualViewerByID("", nodeID, streamName, clientIP)
	if duplicate {
		t.Fatal("expected duplicate direct playback intent to be ignored")
	}
	if againID != viewerID {
		t.Fatalf("expected duplicate to return existing viewer ID %s, got %s", viewerID, againID)
	}
}

func TestConfirmVirtualViewer_OldestPendingWins(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-oldest"
	streamName := "stream-oldest"
	clientIP := "192.0.2.10"

	viewerOld := sm.CreateVirtualViewer(nodeID, streamName, clientIP)
	viewerNew := sm.CreateVirtualViewer(nodeID, streamName, clientIP)

	sm.mu.Lock()
	sm.virtualViewers[viewerOld].RedirectTime = time.Now().Add(-2 * time.Minute)
	sm.virtualViewers[viewerNew].RedirectTime = time.Now().Add(-1 * time.Minute)
	sm.mu.Unlock()

	confirmed := sm.ConfirmVirtualViewer(nodeID, streamName, clientIP)
	if !confirmed {
		t.Fatal("expected confirmation for oldest pending viewer")
	}

	sm.mu.RLock()
	oldState := sm.virtualViewers[viewerOld].State
	newState := sm.virtualViewers[viewerNew].State
	sm.mu.RUnlock()

	if oldState != VirtualViewerActive {
		t.Fatalf("expected oldest viewer to be active, got %s", oldState)
	}
	if newState != VirtualViewerPending {
		t.Fatalf("expected newest viewer to remain pending, got %s", newState)
	}
}

func TestConfirmVirtualViewer_DuplicateUserNewDoesNotUnderflow(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-duplicate"
	streamName := "stream-duplicate"
	clientIP := "198.51.100.9"

	viewerID := sm.CreateVirtualViewer(nodeID, streamName, clientIP)

	first := sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, clientIP, "mist-session-dup")
	if !first {
		t.Fatal("expected first confirmation to succeed")
	}
	second := sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, clientIP, "mist-session-dup")
	if second {
		t.Fatal("expected duplicate confirmation to be ignored")
	}

	sm.mu.RLock()
	pending := sm.nodes[nodeID].PendingRedirects
	state := sm.virtualViewers[viewerID].State
	sm.mu.RUnlock()

	if pending != 0 {
		t.Fatalf("expected pending redirects to remain at 0, got %d", pending)
	}
	if state != VirtualViewerActive {
		t.Fatalf("expected viewer to remain active, got %s", state)
	}
}

func TestDisconnectVirtualViewer_OutOfOrderUserEndAbandonsPending(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-out-of-order"
	streamName := "stream-out-of-order"
	clientIP := "203.0.113.7"

	viewerID := sm.CreateVirtualViewer(nodeID, streamName, clientIP)

	if disconnected := sm.DisconnectVirtualViewerBySessionID("", nodeID, streamName, clientIP); disconnected {
		t.Fatal("expected out-of-order USER_END to abandon pending viewer without reporting a disconnect")
	}

	sm.mu.RLock()
	state := sm.virtualViewers[viewerID].State
	pending := sm.nodes[nodeID].PendingRedirects
	sm.mu.RUnlock()

	if state != VirtualViewerAbandoned {
		t.Fatalf("expected pending viewer to be abandoned, got %s", state)
	}
	if pending != 0 {
		t.Fatalf("expected pending redirects to decrement to 0, got %d", pending)
	}
}

func TestDisconnectVirtualViewerBySessionID_ReturnsTrueForActiveViewer(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-active-disconnect"
	streamName := "stream-active-disconnect"
	clientIP := "203.0.113.8"

	viewerID := sm.CreateVirtualViewer(nodeID, streamName, clientIP)
	if confirmed := sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, clientIP, "mist-session-active"); !confirmed {
		t.Fatal("expected viewer confirmation")
	}

	if disconnected := sm.DisconnectVirtualViewerBySessionID("mist-session-active", nodeID, streamName, clientIP); !disconnected {
		t.Fatal("expected active viewer disconnect to be reported")
	}

	sm.mu.RLock()
	viewerState := sm.virtualViewers[viewerID].State
	sm.mu.RUnlock()

	if viewerState != VirtualViewerDisconnected {
		t.Fatalf("expected viewer to be disconnected, got %s", viewerState)
	}
}

func TestDisconnectVirtualViewerBySessionID_WaitsForLastMistSession(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-multi-session"
	streamName := "stream-multi-session"
	clientIP := "203.0.113.9"

	viewerID := sm.CreateVirtualViewer(nodeID, streamName, clientIP)
	if confirmed := sm.ConfirmVirtualViewerByID(viewerID, nodeID, streamName, clientIP, "mist-session-a"); !confirmed {
		t.Fatal("expected viewer confirmation")
	}
	if attached := sm.AttachVirtualViewerSession(viewerID, nodeID, streamName, clientIP, "mist-session-b"); !attached {
		t.Fatal("expected second Mist session attachment")
	}

	if got := sm.ActiveVirtualViewerIDForSession("mist-session-a", nodeID, streamName); got != viewerID {
		t.Fatalf("session-a viewer id = %q, want %q", got, viewerID)
	}
	if got := sm.ActiveVirtualViewerIDForSession("mist-session-b", nodeID, streamName); got != viewerID {
		t.Fatalf("session-b viewer id = %q, want %q", got, viewerID)
	}

	if disconnected := sm.DisconnectVirtualViewerBySessionID("mist-session-a", nodeID, streamName, clientIP); disconnected {
		t.Fatal("first session end must not disconnect the virtual viewer")
	}
	sm.mu.RLock()
	viewerState := sm.virtualViewers[viewerID].State
	sm.mu.RUnlock()
	if viewerState != VirtualViewerActive {
		t.Fatalf("expected viewer to stay active after first session end, got %s", viewerState)
	}

	if disconnected := sm.DisconnectVirtualViewerBySessionID("mist-session-b", nodeID, streamName, clientIP); !disconnected {
		t.Fatal("last session end must disconnect the virtual viewer")
	}
	sm.mu.RLock()
	viewerState = sm.virtualViewers[viewerID].State
	sm.mu.RUnlock()
	if viewerState != VirtualViewerDisconnected {
		t.Fatalf("expected viewer to be disconnected, got %s", viewerState)
	}
}

func TestStreamStateTransitionsAndTenantIsolation(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "node-streams"
	streamA := "live+tenantA"
	streamB := "live+tenantB"
	tenantA := "tenant-a"
	tenantB := "tenant-b"

	if err := sm.UpdateStreamFromBuffer(streamA, "tenantA", nodeID, tenantA, "BUFFERING", ""); err != nil {
		t.Fatalf("unexpected error updating stream A: %v", err)
	}
	sm.UpdateNodeStats("tenantA", nodeID, 1, 1, 1024, 0, false)
	if err := sm.UpdateStreamFromBuffer(streamB, "tenantB", nodeID, tenantB, "READY", ""); err != nil {
		t.Fatalf("unexpected error updating stream B: %v", err)
	}
	sm.UpdateNodeStats("tenantB", nodeID, 1, 1, 1024, 0, false)

	streamsForA := sm.GetStreamsByTenant(tenantA)
	if len(streamsForA) != 1 {
		t.Fatalf("expected 1 stream for tenant A, got %d", len(streamsForA))
	}
	if streamsForA[0].TenantID != tenantA {
		t.Fatalf("expected tenant A stream, got %s", streamsForA[0].TenantID)
	}

	sm.SetOffline("tenantA", nodeID)
	streamsForA = sm.GetStreamsByTenant(tenantA)
	if len(streamsForA) != 0 {
		t.Fatalf("expected no live streams for tenant A after offline, got %d", len(streamsForA))
	}
}

func TestGetStreamsByTenantRequiresFreshActiveInput(t *testing.T) {
	sm := NewStreamStateManager()

	tenantID := "tenant-cap"
	nodeID := "edge-1"
	freshInput := "fresh-input"
	staleInput := "stale-input"
	playbackOnly := "playback-only"

	sm.UpdateNodeStats(freshInput, nodeID, 1, 1, 1024, 0, false)
	sm.mu.Lock()
	sm.streams[freshInput].TenantID = tenantID
	sm.mu.Unlock()
	sm.UpdateNodeStats(staleInput, nodeID, 1, 1, 1024, 0, false)
	sm.mu.Lock()
	sm.streams[staleInput].TenantID = tenantID
	sm.mu.Unlock()
	sm.UpdateNodeStats(playbackOnly, nodeID, 1, 0, 0, 1024, false)
	sm.mu.Lock()
	sm.streams[playbackOnly].TenantID = tenantID
	sm.streams[playbackOnly].Status = "live"
	sm.streams[staleInput].LastUpdate = time.Now().Add(-2 * time.Minute)
	sm.mu.Unlock()

	got := sm.GetStreamsByTenant(tenantID)
	if len(got) != 1 {
		t.Fatalf("GetStreamsByTenant returned %d streams, want 1: %#v", len(got), got)
	}
	if got[0].InternalName != freshInput {
		t.Fatalf("GetStreamsByTenant returned %q, want %q", got[0].InternalName, freshInput)
	}
}

func TestUpdateStreamFromBuffer_ParsesDetailsAndIssues(t *testing.T) {
	sm := NewStreamStateManager()

	internalName := "internal-1"
	streamName := "stream-1"
	nodeID := "node-1"
	tenantID := "tenant-1"
	detailsJSON := `{
		"video1": {"codec": "H264", "kbits": 1500, "fpks": 30000, "width": 1920, "height": 1080},
		"audio1": {"codec": "AAC", "channels": 2, "rate": 48000},
		"issues": "signal_warning"
	}`

	if err := sm.UpdateStreamFromBuffer(streamName, internalName, nodeID, tenantID, "FULL", detailsJSON); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := sm.GetStreamState(internalName)
	if state == nil {
		t.Fatal("expected stream state")
	}
	if state.Status != "live" {
		t.Fatalf("expected live status, got %s", state.Status)
	}
	if state.BufferState != "FULL" {
		t.Fatalf("expected buffer state FULL, got %s", state.BufferState)
	}
	if state.HasIssues != true || state.Issues != "signal_warning" {
		t.Fatalf("expected issues to be set, got hasIssues=%v issues=%q", state.HasIssues, state.Issues)
	}
	if state.StartedAt == nil {
		t.Fatal("expected StartedAt to be set")
	}
	if len(state.Tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(state.Tracks))
	}

	var videoTrack, audioTrack *StreamTrack
	for i := range state.Tracks {
		track := &state.Tracks[i]
		if track.Type == "video" {
			videoTrack = track
		}
		if track.Type == "audio" {
			audioTrack = track
		}
	}
	if videoTrack == nil || audioTrack == nil {
		t.Fatalf("expected both video and audio tracks, got video=%v audio=%v", videoTrack, audioTrack)
	}
	if videoTrack.FPS != 30 {
		t.Fatalf("expected 30 fps, got %v", videoTrack.FPS)
	}
	if videoTrack.Bitrate != 1500 {
		t.Fatalf("expected video bitrate 1500, got %d", videoTrack.Bitrate)
	}
	if audioTrack.Channels != 2 {
		t.Fatalf("expected audio channels 2, got %d", audioTrack.Channels)
	}
	if audioTrack.SampleRate != 48000 {
		t.Fatalf("expected audio sample rate 48000, got %d", audioTrack.SampleRate)
	}

	instances := sm.GetStreamInstances(internalName)
	inst, ok := instances[nodeID]
	if !ok {
		t.Fatal("expected stream instance state")
	}
	if inst.Status != "live" {
		t.Fatalf("expected instance status live, got %s", inst.Status)
	}
	if inst.BufferState != "FULL" {
		t.Fatalf("expected instance buffer state FULL, got %s", inst.BufferState)
	}
}

func TestUpdateNodeStatsStartsLiveIntervalOnInput(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	internalName := "internal-input"
	nodeID := "node-input"

	sm.UpdateNodeStats(internalName, nodeID, 0, 1, 0, 0, false)

	state := sm.GetStreamState(internalName)
	if state == nil {
		t.Fatal("expected stream state")
	}
	if state.Status != "live" {
		t.Fatalf("expected live status, got %q", state.Status)
	}
	if state.StartedAt == nil {
		t.Fatal("expected StartedAt to be set when inputs are present")
	}

	instances := sm.GetStreamInstances(internalName)
	inst, ok := instances[nodeID]
	if !ok {
		t.Fatal("expected stream instance state")
	}
	if inst.Status != "live" {
		t.Fatalf("expected instance status live, got %q", inst.Status)
	}
}

func TestUpdateStreamFromBufferStartsExistingStatsOnlyState(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	internalName := "internal-buffer-after-stats"
	nodeID := "node-buffer-after-stats"

	sm.UpdateNodeStats(internalName, nodeID, 0, 0, 0, 0, false)
	state := sm.GetStreamState(internalName)
	if state == nil {
		t.Fatal("expected stream state")
	}
	if state.StartedAt != nil {
		t.Fatal("expected zero-input stats state to have no StartedAt")
	}

	if err := sm.UpdateStreamFromBuffer("stream-buffer-after-stats", internalName, nodeID, "tenant-buffer", "FULL", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	state = sm.GetStreamState(internalName)
	if state == nil {
		t.Fatal("expected stream state after buffer update")
	}
	if state.Status != "live" {
		t.Fatalf("expected live status, got %q", state.Status)
	}
	if state.StartedAt == nil {
		t.Fatal("expected buffer update to set StartedAt on existing state")
	}
}

func TestUpdateNodeStatsStartsNewIntervalAfterOffline(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	internalName := "internal-restart"
	nodeID := "node-restart"

	sm.UpdateNodeStats(internalName, nodeID, 0, 1, 0, 0, false)
	first := sm.GetStreamState(internalName).StartedAt
	if first == nil {
		t.Fatal("expected first StartedAt")
	}

	time.Sleep(time.Millisecond)
	sm.SetOffline(internalName, nodeID)
	time.Sleep(time.Millisecond)
	sm.UpdateNodeStats(internalName, nodeID, 0, 1, 0, 0, false)

	state := sm.GetStreamState(internalName)
	if state == nil || state.StartedAt == nil {
		t.Fatal("expected restarted stream state with StartedAt")
	}
	if !state.StartedAt.After(*first) {
		t.Fatalf("expected restart StartedAt %s to be after first %s", state.StartedAt, first)
	}
}

// TestSetOfflinePerNodeSemantics locks the per-node contract: SetOffline
// zeroes the reporting node's presence counters (the balancer and union
// derivation read Inputs without checking Status, so stale Inputs>0 would
// keep a dead node source-eligible), and the union only goes offline when
// no other node still actively carries the stream.
func TestSetOfflinePerNodeSemantics(t *testing.T) {
	sm := NewStreamStateManager()
	defer sm.Shutdown()

	internalName := "multi-node"
	sm.UpdateNodeStats(internalName, "node-A", 5, 1, 100, 200, false)
	sm.UpdateNodeStats(internalName, "node-B", 3, 1, 100, 200, true)

	sm.SetOffline(internalName, "node-A")

	instA := sm.GetStreamInstances(internalName)["node-A"]
	if instA.Status != "offline" || instA.Inputs != 0 || instA.TotalConnections != 0 {
		t.Fatalf("expected node-A instance offline with zeroed presence, got %#v", instA)
	}
	union := sm.GetStreamState(internalName)
	if union == nil || union.Status == "offline" {
		t.Fatalf("union must stay non-offline while node-B carries the stream, got %#v", union)
	}
	if union.Inputs != 1 {
		t.Fatalf("union inputs must exclude the offline node, got %d", union.Inputs)
	}

	// Last carrier gone → union goes offline.
	sm.SetOffline(internalName, "node-B")
	union = sm.GetStreamState(internalName)
	if union == nil || union.Status != "offline" {
		t.Fatalf("expected union offline after last carrier left, got %#v", union)
	}
	if union.BufferState != "EMPTY" {
		t.Fatalf("expected EMPTY union buffer state, got %q", union.BufferState)
	}
	if union.Inputs != 0 {
		t.Fatalf("expected zero union inputs, got %d", union.Inputs)
	}
}

func TestUpdateStreamFromBuffer_IgnoresNonStringIssues(t *testing.T) {
	sm := NewStreamStateManager()

	detailsJSON := `{"issues": {"code": 42}}`
	if err := sm.UpdateStreamFromBuffer("stream", "internal", "node", "tenant", "READY", detailsJSON); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	state := sm.GetStreamState("internal")
	if state == nil {
		t.Fatal("expected stream state")
	}
	if state.HasIssues {
		t.Fatal("expected HasIssues to be false when issues is not a string")
	}
}

func TestUpdateStreamFromBuffer_InvalidJSONReturnsError(t *testing.T) {
	sm := NewStreamStateManager()

	err := sm.UpdateStreamFromBuffer("stream", "internal", "node", "tenant", "READY", "{invalid")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNodeActiveViewersUsesConfirmedViewersNotRawConnections(t *testing.T) {
	sm := NewStreamStateManager()

	internalName := "internal-viewers"
	nodeID := "node-viewers"
	tenantID := "tenant-viewers"

	sm.SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
	sm.UpdateNodeStats(internalName, nodeID, 17, 1, 0, 0, false)
	sm.UpdateUserConnection(internalName, nodeID, tenantID, 1)
	sm.UpdateUserConnection(internalName, nodeID, tenantID, 1)

	if got := sm.GetNodeActiveViewers(nodeID); got != 2 {
		t.Fatalf("expected 2 confirmed viewers, got %d", got)
	}

	snapshot := sm.GetAllNodesSnapshot()
	if snapshot == nil || len(snapshot.Nodes) != 1 {
		t.Fatalf("expected one node snapshot, got %#v", snapshot)
	}
	stream := snapshot.Nodes[0].Streams[internalName]
	if stream.Total != 17 {
		t.Fatalf("expected raw total connections to remain 17, got %d", stream.Total)
	}
	if stream.Viewers != 2 {
		t.Fatalf("expected snapshot viewers to use confirmed viewers, got %d", stream.Viewers)
	}
}

func TestUpdateTrackListAndOfflineState(t *testing.T) {
	sm := NewStreamStateManager()

	internalName := "internal-2"
	nodeID := "node-2"
	tenantID := "tenant-2"

	sm.UpdateTrackList(internalName, nodeID, tenantID, "tracklist-json")
	sm.SetOffline(internalName, nodeID)

	state := sm.GetStreamState(internalName)
	if state == nil {
		t.Fatal("expected stream state")
	}
	if state.Status != "offline" {
		t.Fatalf("expected status offline, got %s", state.Status)
	}
	if state.BufferState != "EMPTY" {
		t.Fatalf("expected buffer state EMPTY, got %s", state.BufferState)
	}
	if state.LastTrackList != "tracklist-json" {
		t.Fatalf("expected tracklist to remain, got %s", state.LastTrackList)
	}

	instances := sm.GetStreamInstances(internalName)
	inst, ok := instances[nodeID]
	if !ok {
		t.Fatal("expected stream instance state")
	}
	if inst.Status != "offline" {
		t.Fatalf("expected instance status offline, got %s", inst.Status)
	}
	if inst.BufferState != "EMPTY" {
		t.Fatalf("expected instance buffer state EMPTY, got %s", inst.BufferState)
	}
}

func TestCanonicalNodeID_TenantPropagation(t *testing.T) {
	sm := NewStreamStateManager()

	nodeID := "edge-abc-temp"
	canonicalNodeID := "edge-abc"

	// Simulate Connect: SetNodeInfo creates the nodeID entry (no tenant)
	sm.SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)

	// Simulate Quartermaster resolution: tenant written to canonicalNodeID only
	sm.SetNodeConnectionInfo(context.Background(), canonicalNodeID, "", "tenantA", "cluster-eu", nil)

	// Heartbeats keep nodeID alive
	sm.TouchNode(nodeID, true)

	// Bug scenario: nodeID entry has no TenantID
	sm.mu.RLock()
	nActive := sm.nodes[nodeID]
	nCanonical := sm.nodes[canonicalNodeID]
	sm.mu.RUnlock()

	if nActive == nil {
		t.Fatal("expected nodeID entry to exist")
	}
	if nCanonical == nil {
		t.Fatal("expected canonicalNodeID entry to exist")
	}
	if nCanonical.TenantID != "tenantA" {
		t.Fatalf("expected canonical TenantID=tenantA, got %q", nCanonical.TenantID)
	}
	// Before the fix, this would be empty; after the fix both entries have the tenant.
	// The fix stamps nodeID too, so simulate that:
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "", "tenantA", "cluster-eu", nil)

	sm.mu.RLock()
	nActive = sm.nodes[nodeID]
	sm.mu.RUnlock()
	if nActive.TenantID != "tenantA" {
		t.Fatalf("expected active node TenantID=tenantA, got %q", nActive.TenantID)
	}
	if nActive.ClusterID != "cluster-eu" {
		t.Fatalf("expected active node ClusterID=cluster-eu, got %q", nActive.ClusterID)
	}
}
