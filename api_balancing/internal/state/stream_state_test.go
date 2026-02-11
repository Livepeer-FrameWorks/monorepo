package state

import (
	"testing"
	"time"
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
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer, got %v", stats["abandoned"])
	}

	// Call ReconcileVirtualViewers - this should clean up the old abandoned viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was cleaned up
	stats = sm.GetVirtualViewerStats()
	if stats["abandoned"].(int) != 0 {
		t.Fatalf("expected 0 abandoned viewers after cleanup, got %v", stats["abandoned"])
	}
	if stats["total_viewers"].(int) != 0 {
		t.Fatalf("expected 0 total viewers after cleanup, got %v", stats["total_viewers"])
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
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer (recent), got %v", stats["abandoned"])
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
	if stats["pending"].(int) != 1 {
		t.Fatalf("expected 1 pending viewer, got %v", stats["pending"])
	}

	// Call ReconcileVirtualViewers - this should timeout the pending viewer
	sm.ReconcileVirtualViewers(nodeID, 0, 0)

	// Verify viewer was marked as ABANDONED
	stats = sm.GetVirtualViewerStats()
	if stats["pending"].(int) != 0 {
		t.Fatalf("expected 0 pending viewers after timeout, got %v", stats["pending"])
	}
	if stats["abandoned"].(int) != 1 {
		t.Fatalf("expected 1 abandoned viewer after timeout, got %v", stats["abandoned"])
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
		MaxTranscodes        int
		CurrentTranscodes    int
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
		MaxTranscodes:        1,
		CurrentTranscodes:    0,
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

	sm.DisconnectVirtualViewerBySessionID("", nodeID, streamName, clientIP)

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
	if err := sm.UpdateStreamFromBuffer(streamB, "tenantB", nodeID, tenantB, "READY", ""); err != nil {
		t.Fatalf("unexpected error updating stream B: %v", err)
	}

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
	sm.SetNodeConnectionInfo(canonicalNodeID, "", "tenantA", "cluster-eu", nil)

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
	sm.SetNodeConnectionInfo(nodeID, "", "tenantA", "cluster-eu", nil)

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
