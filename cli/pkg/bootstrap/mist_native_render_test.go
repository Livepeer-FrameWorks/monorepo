package bootstrap

import (
	"strings"
	"testing"
)

func TestMistNativeStreamToRendered_AcceptsExecForSystemTenant(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "control-central", Type: "central"},
	}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "frameworks-demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:ffmpeg -re -stream_loop -1 -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -",
		SourceKind:        "exec",
		AlwaysOn:          true,
		PlacementCount:    1,
		AllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered.SourceKind != "exec" || rendered.PlacementCount != 1 {
		t.Fatalf("unexpected rendered shape: %+v", rendered)
	}
}

func TestMistNativeStreamToRendered_RejectsExecForCustomerTenant(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.tenants.northwind"},
		Title:       "Demo",
		Source:      "ts-exec:ffmpeg -re -i clip.mp4 -c copy -f mpegts -",
		SourceKind:  "exec",
		AlwaysOn:    true,
	}, clusters)
	if err == nil || !strings.Contains(err.Error(), "owner_tenant=frameworks") {
		t.Fatalf("expected exec-tenant rejection, got: %v", err)
	}
}

func TestMistNativeStreamToRendered_RejectsKindSourceMismatch(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		Source:      "/var/lib/frameworks/demo/clip.mp4",
		SourceKind:  "exec",
	}, clusters)
	if err == nil || !strings.Contains(err.Error(), "ts-exec:") {
		t.Fatalf("expected source/kind mismatch error, got: %v", err)
	}
}

func TestMistNativeStreamToRendered_RejectsUnknownCluster(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:ffmpeg -re -i clip.mp4 -c copy -f mpegts -",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"ghost-cluster"},
	}, clusters)
	if err == nil || !strings.Contains(err.Error(), "ghost-cluster") {
		t.Fatalf("expected unknown-cluster error, got: %v", err)
	}
}

// TestMistNativeStreamToRendered_RejectsEmptyAllowedClusterIDs pins the
// non-empty invariant: mist_native placement requires an explicit source
// cluster, and the reconciler cannot operate on an empty set.
func TestMistNativeStreamToRendered_RejectsEmptyAllowedClusterIDs(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		Source:      "ts-exec:cat /dev/null",
		SourceKind:  "exec",
	}, clusters)
	if err == nil || !strings.Contains(err.Error(), "at least one media cluster") {
		t.Fatalf("expected at-least-one-cluster rejection, got: %v", err)
	}
}

// TestMistNativeStreamToRendered_RejectsMultipleSourceClusters pins the
// current contract: mist_native source election is cluster-local, so
// cross-cluster source failover is rejected instead of guessed.
func TestMistNativeStreamToRendered_RejectsMultipleSourceClusters(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "edge-us-1", Type: "edge"},
	}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"edge-eu-1", "edge-us-1"},
	}, clusters)
	if err == nil {
		t.Fatalf("multi-cluster source set must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one source cluster") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMistNativeStreamToRendered_PlacementCountDefaultsToOne(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered.PlacementCount != 1 {
		t.Fatalf("expected PlacementCount=1 default, got %d", rendered.PlacementCount)
	}
}

func TestMistNativeStreamToRendered_ValidatesMonitoring(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		Monitoring:        " ON ",
		AllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters)
	if err != nil {
		t.Fatalf("monitoring=ON should render: %v", err)
	}
	if rendered.Monitoring != "on" {
		t.Fatalf("Monitoring=%q want on", rendered.Monitoring)
	}

	_, err = mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		Monitoring:        "enabled",
		AllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters)
	if err == nil || !strings.Contains(err.Error(), "inherit/on/off") {
		t.Fatalf("expected invalid monitoring rejection, got %v", err)
	}
}

// TestMistNativeStreamToRendered_PlacementCountIsNodeCountNotClusterCount
// locks the contract that placement_count counts elected edge NODES, not
// clusters: a single allowed cluster legitimately supports placement_count
// > 1 because it may contain many edges. Runtime placement clamps to the
// eligible-node count.
func TestMistNativeStreamToRendered_PlacementCountIsNodeCountNotClusterCount(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
	}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		PlacementCount:    3,
		AllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters)
	if err != nil {
		t.Fatalf("placement_count=3 with one allowed cluster must render: %v", err)
	}
	if rendered.PlacementCount != 3 {
		t.Fatalf("PlacementCount not preserved through render: got %d", rendered.PlacementCount)
	}
}

// TestMistNativeStreamToRendered_RejectsMultiClusterMultiEdge keeps the
// stricter one-source-cluster contract pinned even when placement_count asks
// for multiple elected nodes.
func TestMistNativeStreamToRendered_RejectsMultiClusterMultiEdge(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "edge-us-1", Type: "edge"},
	}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		PlacementCount:    2,
		AllowedClusterIDs: []string{"edge-eu-1", "edge-us-1"},
	}, clusters)
	if err == nil {
		t.Fatalf("placement_count=2 across two clusters must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one source cluster") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMistNativeStreamToRendered_RejectsMultiClusterSingleEdge confirms that
// even placement_count=1 does not imply safe cross-cluster source election:
// each cluster-local Foghorn has only its local Redis node view today.
func TestMistNativeStreamToRendered_RejectsMultiClusterSingleEdge(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "edge-us-1", Type: "edge"},
	}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:        "demo",
		OwnerTenant:       TenantRef{Ref: "quartermaster.system_tenant"},
		Title:             "Demo",
		Source:            "ts-exec:cat /dev/null",
		SourceKind:        "exec",
		AllowedClusterIDs: []string{"edge-eu-1", "edge-us-1"},
	}, clusters)
	if err == nil {
		t.Fatalf("placement_count=1 across two clusters must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one source cluster") {
		t.Fatalf("unexpected error: %v", err)
	}
}
