package federation

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

func testLogger() *logrus.Logger {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return l
}

func setupTestCache(t *testing.T) (*RemoteEdgeCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cache := NewRemoteEdgeCache(client, "cluster-a", testLogger())
	return cache, mr
}

func TestRemoteEdge_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteEdgeEntry{
		StreamName:  "tenant1+stream1",
		NodeID:      "node-1",
		BaseURL:     "edge1.example.com",
		BWAvailable: 500_000_000,
		ViewerCount: 10,
		CPUPercent:  25.5,
		RAMUsed:     4_000_000_000,
		RAMMax:      8_000_000_000,
		GeoLat:      52.52,
		GeoLon:      13.40,
		UpdatedAt:   time.Now().Unix(),
	}

	if err := cache.SetRemoteEdge(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteEdge: %v", err)
	}

	edges, err := cache.GetRemoteEdges(ctx, "cluster-b")
	if err != nil {
		t.Fatalf("GetRemoteEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	if edges[0].NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", edges[0].NodeID, "node-1")
	}
	if edges[0].BWAvailable != 500_000_000 {
		t.Errorf("BWAvailable = %d, want %d", edges[0].BWAvailable, 500_000_000)
	}
}

func TestRemoteEdge_TTLExpiry(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteEdgeEntry{
		NodeID:      "node-1",
		BWAvailable: 100,
		UpdatedAt:   time.Now().Unix(),
	}
	if err := cache.SetRemoteEdge(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteEdge: %v", err)
	}

	// Fast-forward past TTL
	mr.FastForward(remoteEdgeTTL + time.Second)

	edges, err := cache.GetRemoteEdges(ctx, "cluster-b")
	if err != nil {
		t.Fatalf("GetRemoteEdges after expiry: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges after TTL, got %d", len(edges))
	}
}

func TestRemoteReplication_SetGetDelete(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteReplicationEntry{
		StreamName: "tenant1+stream1",
		NodeID:     "node-2",
		ClusterID:  "cluster-b",
		BaseURL:    "edge2.example.com",
		DTSCURL:    "dtsc://edge2.example.com:4200/tenant1+stream1",
		Available:  true,
		UpdatedAt:  time.Now().Unix(),
	}
	if err := cache.SetRemoteReplication(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteReplication: %v", err)
	}

	reps, err := cache.GetRemoteReplications(ctx, "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetRemoteReplications: %v", err)
	}
	if len(reps) != 1 {
		t.Fatalf("expected 1 replication, got %d", len(reps))
	}
	if reps[0].DTSCURL != entry.DTSCURL {
		t.Errorf("DTSCURL = %q, want %q", reps[0].DTSCURL, entry.DTSCURL)
	}

	// Mark unavailable → should delete the key
	entry.Available = false
	if err = cache.SetRemoteReplication(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteReplication (unavailable): %v", err)
	}
	reps, err = cache.GetRemoteReplications(ctx, "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetRemoteReplications after delete: %v", err)
	}
	if len(reps) != 0 {
		t.Fatalf("expected 0 replications after unavailable, got %d", len(reps))
	}
}

func TestActiveReplication_CRUD(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	record := &ActiveReplicationRecord{
		StreamName:    "tenant1+stream1",
		SourceNodeID:  "src-node",
		SourceCluster: "cluster-b",
		DestCluster:   "cluster-a",
		DestNodeID:    "dest-node",
		DTSCURL:       "dtsc://src-edge:4200/tenant1+stream1",
		BaseURL:       "dest-edge.example.com",
		CreatedAt:     time.Now(),
	}

	if err := cache.SetActiveReplication(ctx, record); err != nil {
		t.Fatalf("SetActiveReplication: %v", err)
	}

	got, err := cache.GetActiveReplication(ctx, "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetActiveReplication: %v", err)
	}
	if got == nil {
		t.Fatal("expected active replication record, got nil")
	}
	if got.SourceCluster != "cluster-b" {
		t.Errorf("SourceCluster = %q, want %q", got.SourceCluster, "cluster-b")
	}

	all, err := cache.GetAllActiveReplications(ctx)
	if err != nil {
		t.Fatalf("GetAllActiveReplications: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 active replication, got %d", len(all))
	}

	if err = cache.DeleteActiveReplication(ctx, "tenant1+stream1"); err != nil {
		t.Fatalf("DeleteActiveReplication: %v", err)
	}
	got, err = cache.GetActiveReplication(ctx, "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetActiveReplication after delete: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after delete")
	}
}

func TestEdgeSummary_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	record := &EdgeSummaryRecord{
		Edges: []*EdgeSummaryEntry{
			{
				NodeID:         "node-1",
				BaseURL:        "edge1.peer.com",
				GeoLat:         48.85,
				GeoLon:         2.35,
				BWAvailableAvg: 800_000_000,
				CPUPercentAvg:  30.0,
				RAMUsed:        2_000_000_000,
				RAMMax:         8_000_000_000,
				TotalViewers:   50,
				Roles:          []string{"edge", "ingest"},
			},
			{
				NodeID:         "node-2",
				BaseURL:        "edge2.peer.com",
				GeoLat:         48.86,
				GeoLon:         2.36,
				BWAvailableAvg: 600_000_000,
				CPUPercentAvg:  45.0,
				RAMUsed:        3_000_000_000,
				RAMMax:         8_000_000_000,
				TotalViewers:   80,
				Roles:          []string{"edge"},
			},
		},
		Timestamp: time.Now().Unix(),
	}

	if err := cache.SetEdgeSummary(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetEdgeSummary: %v", err)
	}

	got, err := cache.GetEdgeSummary(ctx, "cluster-b")
	if err != nil {
		t.Fatalf("GetEdgeSummary: %v", err)
	}
	if got == nil {
		t.Fatal("expected edge summary, got nil")
	}
	if len(got.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(got.Edges))
	}
	if got.Edges[0].NodeID != "node-1" {
		t.Errorf("Edges[0].NodeID = %q, want %q", got.Edges[0].NodeID, "node-1")
	}

	// Non-existent peer returns nil
	got, err = cache.GetEdgeSummary(ctx, "cluster-z")
	if err != nil {
		t.Fatalf("GetEdgeSummary for missing peer: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for non-existent peer")
	}
}

func TestLeaderLease_AcquireRenewRelease(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	// Acquire lease
	if !cache.TryAcquireLeaderLease(ctx, "test-role", "instance-1") {
		t.Fatal("expected to acquire lease")
	}

	// Same instance re-acquires
	if !cache.TryAcquireLeaderLease(ctx, "test-role", "instance-1") {
		t.Fatal("expected re-entrant acquire")
	}

	// Different instance cannot acquire
	if cache.TryAcquireLeaderLease(ctx, "test-role", "instance-2") {
		t.Fatal("expected instance-2 to fail acquiring")
	}

	// Renew succeeds for holder
	if !cache.RenewLeaderLease(ctx, "test-role", "instance-1") {
		t.Fatal("expected renew to succeed for holder")
	}

	// Renew fails for non-holder
	if cache.RenewLeaderLease(ctx, "test-role", "instance-2") {
		t.Fatal("expected renew to fail for non-holder")
	}

	// Release
	cache.ReleaseLeaderLease(ctx, "test-role", "instance-1")

	// Now instance-2 can acquire
	if !cache.TryAcquireLeaderLease(ctx, "test-role", "instance-2") {
		t.Fatal("expected instance-2 to acquire after release")
	}
}

func TestRenewLeaderLease_ExpiredLease(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	cache.TryAcquireLeaderLease(ctx, "test", "instance-A")
	mr.FastForward(leaderLeaseTTL + time.Second)

	if cache.RenewLeaderLease(ctx, "test", "instance-A") {
		t.Fatal("expected renew to fail after TTL expiry")
	}
}

func TestReleaseLeaderLease_StolenLease(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	// A acquires, lease expires, B acquires
	cache.TryAcquireLeaderLease(ctx, "test", "instance-A")
	mr.FastForward(leaderLeaseTTL + time.Second)
	if !cache.TryAcquireLeaderLease(ctx, "test", "instance-B") {
		t.Fatal("expected B to acquire after A's TTL expiry")
	}

	// A's stale release must NOT delete B's lease (atomic compare-and-delete)
	cache.ReleaseLeaderLease(ctx, "test", "instance-A")

	if !cache.RenewLeaderLease(ctx, "test", "instance-B") {
		t.Fatal("expected B to still hold lease after A's stale release")
	}
}

func TestSetGetPeerAddresses(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	addrs := map[string]string{
		"cluster-1": "foghorn.c1.example.com:18019",
		"cluster-2": "foghorn.c2.example.com:18019",
	}
	if err := cache.SetPeerAddresses(ctx, addrs); err != nil {
		t.Fatalf("SetPeerAddresses: %v", err)
	}

	got, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(got))
	}
	if got["cluster-1"] != addrs["cluster-1"] || got["cluster-2"] != addrs["cluster-2"] {
		t.Fatalf("address mismatch: got %v", got)
	}
}

func TestGetPeerAddresses_EmptyOnMiss(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	got, err := cache.GetPeerAddresses(ctx)
	if err != nil {
		t.Fatalf("GetPeerAddresses: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestSetPeerAddresses_OverwritesPrevious(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	first := map[string]string{"cluster-1": "addr-old", "cluster-2": "addr-2"}
	cache.SetPeerAddresses(ctx, first)

	second := map[string]string{"cluster-1": "addr-new", "cluster-3": "addr-3"}
	cache.SetPeerAddresses(ctx, second)

	got, _ := cache.GetPeerAddresses(ctx)
	if len(got) != 2 {
		t.Fatalf("expected 2 addresses after overwrite, got %d: %v", len(got), got)
	}
	if got["cluster-1"] != "addr-new" {
		t.Fatalf("expected cluster-1 updated to addr-new, got %s", got["cluster-1"])
	}
	if _, ok := got["cluster-2"]; ok {
		t.Fatal("expected cluster-2 removed after overwrite")
	}
	if got["cluster-3"] != "addr-3" {
		t.Fatalf("expected cluster-3 present, got %s", got["cluster-3"])
	}
}

func TestSetPeerAddresses_EmptyMapClearsHash(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	cache.SetPeerAddresses(ctx, map[string]string{"cluster-1": "addr-1"})
	cache.SetPeerAddresses(ctx, map[string]string{})

	got, _ := cache.GetPeerAddresses(ctx)
	if len(got) != 0 {
		t.Fatalf("expected empty map after clearing, got %v", got)
	}
}

func TestPeerClusterIDFromKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{"remote_edges", "{c1}:remote_edges:c2:node-1", "c2"},
		{"remote_replications", "{c1}:remote_replications:stream1:c2", "c2"},
		{"short key", "{c1}:remote_edges:c2", ""},
		{"empty string", "", ""},
		{"unknown type", "{c1}:something:a:b", ""},
		{"edge_summary", "{c1}:edge_summary:c2", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PeerClusterIDFromKey(tt.key)
			if got != tt.want {
				t.Errorf("PeerClusterIDFromKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestRemoteArtifact_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteArtifactEntry{
		ArtifactHash: "abc123",
		ArtifactType: "clip",
		NodeID:       "node-1",
		BaseURL:      "edge1.peer.com",
		SizeBytes:    1_500_000,
		AccessCount:  42,
		LastAccessed: time.Now().Unix(),
		GeoLat:       52.52,
		GeoLon:       13.40,
	}

	if err := cache.SetRemoteArtifact(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteArtifact: %v", err)
	}

	hits, err := cache.GetRemoteArtifacts(ctx, "abc123")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 artifact entry, got %d", len(hits))
	}
	if hits[0].NodeID != "node-1" {
		t.Errorf("NodeID = %q, want %q", hits[0].NodeID, "node-1")
	}
	if hits[0].PeerCluster != "cluster-b" {
		t.Errorf("PeerCluster = %q, want %q", hits[0].PeerCluster, "cluster-b")
	}
	if hits[0].SizeBytes != 1_500_000 {
		t.Errorf("SizeBytes = %d, want %d", hits[0].SizeBytes, 1_500_000)
	}
	if hits[0].AccessCount != 42 {
		t.Errorf("AccessCount = %d, want %d", hits[0].AccessCount, 42)
	}
}

func TestRemoteArtifact_MultiPeerLookup(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	entryB := &RemoteArtifactEntry{
		ArtifactHash: "shared-clip",
		ArtifactType: "clip",
		NodeID:       "node-b1",
		BaseURL:      "edge1.cluster-b.com",
		SizeBytes:    2_000_000,
		AccessCount:  10,
		GeoLat:       48.85,
		GeoLon:       2.35,
	}
	entryC := &RemoteArtifactEntry{
		ArtifactHash: "shared-clip",
		ArtifactType: "clip",
		NodeID:       "node-c1",
		BaseURL:      "edge1.cluster-c.com",
		SizeBytes:    2_000_000,
		AccessCount:  5,
		GeoLat:       40.71,
		GeoLon:       -74.01,
	}

	if err := cache.SetRemoteArtifact(ctx, "cluster-b", entryB); err != nil {
		t.Fatalf("SetRemoteArtifact cluster-b: %v", err)
	}
	if err := cache.SetRemoteArtifact(ctx, "cluster-c", entryC); err != nil {
		t.Fatalf("SetRemoteArtifact cluster-c: %v", err)
	}

	hits, err := cache.GetRemoteArtifacts(ctx, "shared-clip")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 artifact entries from 2 peers, got %d", len(hits))
	}

	peers := map[string]bool{}
	for _, h := range hits {
		peers[h.PeerCluster] = true
	}
	if !peers["cluster-b"] || !peers["cluster-c"] {
		t.Errorf("expected both cluster-b and cluster-c, got peers: %v", peers)
	}
}

func TestRemoteArtifact_TTLExpiry(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteArtifactEntry{
		ArtifactHash: "expires-soon",
		ArtifactType: "dvr",
		NodeID:       "node-1",
		BaseURL:      "edge-egress.peer.com",
	}
	if err := cache.SetRemoteArtifact(ctx, "cluster-b", entry); err != nil {
		t.Fatalf("SetRemoteArtifact: %v", err)
	}

	mr.FastForward(remoteArtifactTTL + time.Second)

	hits, err := cache.GetRemoteArtifacts(ctx, "expires-soon")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts after expiry: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 entries after TTL, got %d", len(hits))
	}
}

func TestRemoteArtifact_NoMatchReturnsEmpty(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	hits, err := cache.GetRemoteArtifacts(ctx, "nonexistent-hash")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected 0 entries for unknown hash, got %d", len(hits))
	}
}

func TestRemoteArtifact_OverwriteSamePeer(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	entry := &RemoteArtifactEntry{
		ArtifactHash: "clip-1",
		ArtifactType: "clip",
		NodeID:       "node-1",
		BaseURL:      "edge-egress.peer.com",
		AccessCount:  5,
	}
	cache.SetRemoteArtifact(ctx, "cluster-b", entry)

	// Update with higher access count
	entry.AccessCount = 50
	cache.SetRemoteArtifact(ctx, "cluster-b", entry)

	hits, err := cache.GetRemoteArtifacts(ctx, "clip-1")
	if err != nil {
		t.Fatalf("GetRemoteArtifacts: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 entry (overwrite, not duplicate), got %d", len(hits))
	}
	if hits[0].AccessCount != 50 {
		t.Errorf("AccessCount = %d, want 50 (updated)", hits[0].AccessCount)
	}
}

func TestStreamAd_SetGetWithdraw(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	record := &StreamAdRecord{
		InternalName:    "tenant1+stream1",
		TenantID:        "tenant1",
		PlaybackID:      "play-123",
		OriginClusterID: "cluster-b",
		IsLive:          true,
		Edges: []*StreamAdEdge{
			{
				NodeID:      "node-1",
				BaseURL:     "edge1.cluster-b.com",
				DTSCURL:     "dtsc://edge1:4200/tenant1+stream1",
				IsOrigin:    true,
				BWAvailable: 500_000_000,
				ViewerCount: 10,
				GeoLat:      52.52,
				GeoLon:      13.40,
			},
		},
		Timestamp: time.Now().Unix(),
	}

	if err := cache.SetStreamAd(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetStreamAd: %v", err)
	}

	got, err := cache.GetStreamAd(ctx, "cluster-b", "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetStreamAd: %v", err)
	}
	if got == nil {
		t.Fatal("expected stream ad, got nil")
	}
	if got.PlaybackID != "play-123" {
		t.Errorf("PlaybackID = %q, want %q", got.PlaybackID, "play-123")
	}
	if len(got.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(got.Edges))
	}

	// Withdraw stream
	record.IsLive = false
	if err = cache.SetStreamAd(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetStreamAd (withdraw): %v", err)
	}
	got, err = cache.GetStreamAd(ctx, "cluster-b", "tenant1+stream1")
	if err != nil {
		t.Fatalf("GetStreamAd after withdraw: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after stream withdrawal")
	}
}

func TestStreamAd_WithdrawDeletesPlaybackIndex(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	record := &StreamAdRecord{
		InternalName: "tenant1+stream1",
		PlaybackID:   "play-123",
		IsLive:       true,
	}
	if err := cache.SetStreamAd(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetStreamAd: %v", err)
	}
	if err := cache.SetPlaybackIndex(ctx, "play-123", "tenant1+stream1"); err != nil {
		t.Fatalf("SetPlaybackIndex: %v", err)
	}

	record.IsLive = false
	if err := cache.SetStreamAd(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetStreamAd (withdraw): %v", err)
	}

	mapped, err := cache.GetPlaybackIndex(ctx, "play-123")
	if err != nil {
		t.Fatalf("GetPlaybackIndex: %v", err)
	}
	if mapped != "" {
		t.Fatalf("expected playback index to be removed, got %q", mapped)
	}
}

func TestPeerHeartbeat_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	record := &PeerHeartbeatRecord{
		ProtocolVersion:  1,
		StreamCount:      25,
		TotalBWAvailable: 10_000_000_000,
		EdgeCount:        5,
		UptimeSeconds:    3600,
		Capabilities:     []string{"stream_ad", "artifact_ad"},
	}

	if err := cache.SetPeerHeartbeat(ctx, "cluster-b", record); err != nil {
		t.Fatalf("SetPeerHeartbeat: %v", err)
	}

	got, err := cache.GetPeerHeartbeat(ctx, "cluster-b")
	if err != nil {
		t.Fatalf("GetPeerHeartbeat: %v", err)
	}
	if got == nil {
		t.Fatal("expected heartbeat, got nil")
	}
	if got.StreamCount != 25 {
		t.Errorf("StreamCount = %d, want 25", got.StreamCount)
	}
	if got.EdgeCount != 5 {
		t.Errorf("EdgeCount = %d, want 5", got.EdgeCount)
	}
	if got.ReceivedAt == 0 {
		t.Error("expected ReceivedAt to be set")
	}
}

func TestPeerHeartbeat_TTLExpiry(t *testing.T) {
	cache, mr := setupTestCache(t)
	ctx := context.Background()

	cache.SetPeerHeartbeat(ctx, "cluster-b", &PeerHeartbeatRecord{StreamCount: 1})
	mr.FastForward(peerHeartbeatTTL + time.Second)

	got, err := cache.GetPeerHeartbeat(ctx, "cluster-b")
	if err != nil {
		t.Fatalf("GetPeerHeartbeat after expiry: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil after TTL expiry")
	}
}

func TestPlaybackIndex_SetGet(t *testing.T) {
	cache, _ := setupTestCache(t)
	ctx := context.Background()

	if err := cache.SetPlaybackIndex(ctx, "play-abc", "tenant1+stream1"); err != nil {
		t.Fatalf("SetPlaybackIndex: %v", err)
	}

	got, err := cache.GetPlaybackIndex(ctx, "play-abc")
	if err != nil {
		t.Fatalf("GetPlaybackIndex: %v", err)
	}
	if got != "tenant1+stream1" {
		t.Errorf("got %q, want %q", got, "tenant1+stream1")
	}

	// Miss returns empty
	got, err = cache.GetPlaybackIndex(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetPlaybackIndex (miss): %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for miss, got %q", got)
	}
}
