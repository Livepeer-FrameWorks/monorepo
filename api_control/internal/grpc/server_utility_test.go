package grpc

import (
	"database/sql"
	"testing"
	"time"

	pb "frameworks/pkg/proto"
)

func TestValidateBehavior_AllValid(t *testing.T) {
	req := &pb.RegisterRequest{
		PhoneNumber: "",
		HumanCheck:  "human",
		Behavior: &pb.BehaviorData{
			FormShownAt: 1000,
			SubmittedAt: 10000,
			Mouse:       true,
			Typed:       true,
		},
	}
	if !validateBehavior(req) {
		t.Fatal("expected valid")
	}
}

func TestValidateBehavior_HoneypotFilled(t *testing.T) {
	req := &pb.RegisterRequest{
		PhoneNumber: "555-1234",
		HumanCheck:  "human",
		Behavior:    &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 10000, Mouse: true},
	}
	if validateBehavior(req) {
		t.Fatal("honeypot filled should be invalid")
	}
}

func TestValidateBehavior_WrongHumanCheck(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "bot",
		Behavior:   &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 10000, Mouse: true},
	}
	if validateBehavior(req) {
		t.Fatal("wrong human check should be invalid")
	}
}

func TestValidateBehavior_NilBehavior(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior:   nil,
	}
	if validateBehavior(req) {
		t.Fatal("nil behavior should be invalid")
	}
}

func TestValidateBehavior_TooFast(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior:   &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 2000, Mouse: true},
	}
	if validateBehavior(req) {
		t.Fatal("too fast (1s) should be invalid")
	}
}

func TestValidateBehavior_TooSlow(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior: &pb.BehaviorData{
			FormShownAt: 0,
			SubmittedAt: 31 * 60 * 1000, // 31 minutes
			Mouse:       true,
		},
	}
	if validateBehavior(req) {
		t.Fatal("too slow (31min) should be invalid")
	}
}

func TestValidateBehavior_NoInteraction(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior:   &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 10000, Mouse: false, Typed: false},
	}
	if validateBehavior(req) {
		t.Fatal("no mouse/typed interaction should be invalid")
	}
}

func TestValidateBehavior_MouseOnly(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior:   &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 10000, Mouse: true, Typed: false},
	}
	if !validateBehavior(req) {
		t.Fatal("mouse-only interaction should be valid")
	}
}

func TestValidateBehavior_TypedOnly(t *testing.T) {
	req := &pb.RegisterRequest{
		HumanCheck: "human",
		Behavior:   &pb.BehaviorData{FormShownAt: 1000, SubmittedAt: 10000, Mouse: false, Typed: true},
	}
	if !validateBehavior(req) {
		t.Fatal("typed-only interaction should be valid")
	}
}

func TestMaskTargetURI_RTMPWithKey(t *testing.T) {
	masked := maskTargetURI("rtmp://live.twitch.tv/app/live_abc123def456")
	if masked == "rtmp://live.twitch.tv/app/live_abc123def456" {
		t.Fatal("key should be masked")
	}
	// Should contain partial mask
	if masked != "rtmp://live.twitch.tv/app/livexxxx456" {
		t.Fatalf("unexpected mask: %q", masked)
	}
}

func TestMaskTargetURI_ShortLastSegment(t *testing.T) {
	masked := maskTargetURI("rtmp://host/app/key")
	if masked != "rtmp://host/app/xxxx" {
		t.Fatalf("short key should be fully masked, got %q", masked)
	}
}

func TestMaskTargetURI_SRTWithQuery(t *testing.T) {
	masked := maskTargetURI("srt://host:9710?streamid=test&passphrase=secret")
	if masked == "" {
		t.Fatal("should not be empty")
	}
	// Query params should be stripped
	if contains(masked, "passphrase") || contains(masked, "secret") {
		t.Fatalf("query params should be stripped, got %q", masked)
	}
}

func TestMaskTargetURI_WithCredentials(t *testing.T) {
	masked := maskTargetURI("rtmp://user:pass@host/app/key")
	if contains(masked, "user") || contains(masked, "pass") {
		t.Fatalf("credentials should be stripped, got %q", masked)
	}
}

func TestMaskTargetURI_InvalidURI(t *testing.T) {
	masked := maskTargetURI("://invalid")
	if masked != "****" {
		t.Fatalf("expected '****' for invalid URI, got %q", masked)
	}
}

func TestMaskTargetURI_NoPath(t *testing.T) {
	masked := maskTargetURI("rtmp://host")
	if masked == "" {
		t.Fatal("should not be empty")
	}
}

func TestValidatePushTargetURI_Valid(t *testing.T) {
	for _, uri := range []string{
		"rtmp://live.twitch.tv/app/key",
		"rtmps://secure.example.com/live/stream",
		"srt://srt.host:9710",
	} {
		if err := validatePushTargetURI(uri); err != nil {
			t.Errorf("%s: unexpected error: %v", uri, err)
		}
	}
}

func TestValidatePushTargetURI_InvalidScheme(t *testing.T) {
	for _, uri := range []string{
		"http://example.com/stream",
		"https://example.com/stream",
		"ftp://files.example.com",
	} {
		if err := validatePushTargetURI(uri); err == nil {
			t.Errorf("%s: expected error for invalid scheme", uri)
		}
	}
}

func TestValidatePushTargetURI_NoHost(t *testing.T) {
	if err := validatePushTargetURI("rtmp:///app/key"); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestSelectActiveIngestCluster_Fresh(t *testing.T) {
	now := time.Now()
	id, ok := selectActiveIngestCluster(
		sql.NullString{String: "cluster-1", Valid: true},
		sql.NullTime{Time: now.Add(-30 * time.Second), Valid: true},
		now,
	)
	if !ok || id != "cluster-1" {
		t.Fatalf("expected (cluster-1, true), got (%q, %v)", id, ok)
	}
}

func TestSelectActiveIngestCluster_Stale(t *testing.T) {
	now := time.Now()
	_, ok := selectActiveIngestCluster(
		sql.NullString{String: "cluster-1", Valid: true},
		sql.NullTime{Time: now.Add(-5 * time.Minute), Valid: true},
		now,
	)
	if ok {
		t.Fatal("stale cluster should return false")
	}
}

func TestSelectActiveIngestCluster_NullClusterID(t *testing.T) {
	_, ok := selectActiveIngestCluster(
		sql.NullString{Valid: false},
		sql.NullTime{Time: time.Now(), Valid: true},
		time.Now(),
	)
	if ok {
		t.Fatal("null cluster ID should return false")
	}
}

func TestSelectActiveIngestCluster_EmptyClusterID(t *testing.T) {
	_, ok := selectActiveIngestCluster(
		sql.NullString{String: "", Valid: true},
		sql.NullTime{Time: time.Now(), Valid: true},
		time.Now(),
	)
	if ok {
		t.Fatal("empty cluster ID should return false")
	}
}

func TestSelectActiveIngestCluster_NullUpdatedAt(t *testing.T) {
	_, ok := selectActiveIngestCluster(
		sql.NullString{String: "cluster-1", Valid: true},
		sql.NullTime{Valid: false},
		time.Now(),
	)
	if ok {
		t.Fatal("null updatedAt should return false")
	}
}

func TestClusterInPeers_Found(t *testing.T) {
	peers := []*pb.TenantClusterPeer{
		{ClusterId: "c1"},
		{ClusterId: "c2"},
		{ClusterId: "c3"},
	}
	if !clusterInPeers(peers, "c2") {
		t.Fatal("expected true for c2")
	}
}

func TestClusterInPeers_NotFound(t *testing.T) {
	peers := []*pb.TenantClusterPeer{
		{ClusterId: "c1"},
	}
	if clusterInPeers(peers, "c99") {
		t.Fatal("expected false")
	}
}

func TestClusterInPeers_Empty(t *testing.T) {
	if clusterInPeers(nil, "c1") {
		t.Fatal("expected false for nil peers")
	}
}

func TestBuildClusterFanoutTargets_NilRoute(t *testing.T) {
	targets := buildClusterFanoutTargets(nil)
	if targets != nil {
		t.Fatal("expected nil for nil route")
	}
}

func TestBuildClusterFanoutTargets_Dedup(t *testing.T) {
	route := &clusterRoute{
		clusterID:               "c1",
		foghornAddr:             "addr1",
		officialClusterID:       "c1",
		officialFoghornGrpcAddr: "addr1",
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "c1", FoghornGrpcAddr: "addr1"},
			{ClusterId: "c2", FoghornGrpcAddr: "addr2"},
		},
	}
	targets := buildClusterFanoutTargets(route)
	if len(targets) != 2 {
		t.Fatalf("expected 2 deduplicated targets, got %d", len(targets))
	}
}

func TestBuildClusterFanoutTargets_SkipsEmptyAddr(t *testing.T) {
	route := &clusterRoute{
		clusterID:   "c1",
		foghornAddr: "",
		clusterPeers: []*pb.TenantClusterPeer{
			{ClusterId: "c2", FoghornGrpcAddr: "addr2"},
		},
	}
	targets := buildClusterFanoutTargets(route)
	if len(targets) != 1 {
		t.Fatalf("expected 1 target (empty addr skipped), got %d", len(targets))
	}
	if targets[0].clusterID != "c2" {
		t.Fatalf("expected c2, got %s", targets[0].clusterID)
	}
}

func TestBuildClusterFanoutTargets_DistinctOfficial(t *testing.T) {
	route := &clusterRoute{
		clusterID:               "c1",
		foghornAddr:             "addr1",
		officialClusterID:       "c2",
		officialFoghornGrpcAddr: "addr2",
	}
	targets := buildClusterFanoutTargets(route)
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets (primary + official), got %d", len(targets))
	}
}

func TestFoghornPoolKey_WithClusterID(t *testing.T) {
	if foghornPoolKey("c1", "addr1") != "c1" {
		t.Fatal("should prefer clusterID")
	}
}

func TestFoghornPoolKey_FallbackToAddr(t *testing.T) {
	if foghornPoolKey("", "addr1") != "addr1" {
		t.Fatal("should fallback to addr")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
