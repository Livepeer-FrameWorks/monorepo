package bootstrap

import (
	"strings"
	"testing"
)

func clusterLocation(ids ...string) *SourceLocation {
	return &SourceLocation{Clusters: ids}
}

func TestMistNativeStreamToRendered_AcceptsExecForSystemTenant(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "control-central", Type: "central"},
	}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "frameworks-demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:ffmpeg -re -stream_loop -1 -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -",
		SourceKind:     "exec",
		AlwaysOn:       true,
		PlacementCount: 1,
		SourceLocation: clusterLocation("edge-eu-1"),
	}, clusters, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if rendered.SourceKind != "exec" || rendered.PlacementCount != 1 {
		t.Fatalf("unexpected rendered shape: %+v", rendered)
	}
	if rendered.SourceLocation == nil || len(rendered.SourceLocation.Clusters) != 1 || rendered.SourceLocation.Clusters[0].ClusterID != "edge-eu-1" {
		t.Fatalf("source location = %+v, want edge-eu-1", rendered.SourceLocation)
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
	}, clusters, nil)
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
	}, clusters, nil)
	if err == nil || !strings.Contains(err.Error(), "ts-exec:") {
		t.Fatalf("expected source/kind mismatch error, got: %v", err)
	}
}

func TestMistNativeStreamToRendered_RejectsUnknownCluster(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:ffmpeg -re -i clip.mp4 -c copy -f mpegts -",
		SourceKind:     "exec",
		SourceLocation: clusterLocation("ghost-cluster"),
	}, clusters, nil)
	if err == nil || !strings.Contains(err.Error(), "ghost-cluster") {
		t.Fatalf("expected unknown-cluster error, got: %v", err)
	}
}

// Mist-native placement requires an explicit source cluster; the reconciler
// cannot operate on an unrestricted location.
func TestMistNativeStreamToRendered_RejectsMissingSourceLocation(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:  "demo",
		OwnerTenant: TenantRef{Ref: "quartermaster.system_tenant"},
		Title:       "Demo",
		Source:      "ts-exec:cat /dev/null",
		SourceKind:  "exec",
	}, clusters, nil)
	if err == nil || !strings.Contains(err.Error(), "source_location.clusters must contain at least one media cluster") {
		t.Fatalf("expected at-least-one-cluster rejection, got: %v", err)
	}
}

// Mist-native source election is cluster-local, so cross-cluster source
// failover is rejected instead of guessed.
func TestMistNativeStreamToRendered_RejectsMultipleSourceClusters(t *testing.T) {
	clusters := []Cluster{
		{ID: "edge-eu-1", Type: "edge"},
		{ID: "edge-us-1", Type: "edge"},
	}
	for _, count := range []int{0, 2} {
		_, err := mistNativeStreamToRendered(MistNativeStream{
			PlaybackID:     "demo",
			OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
			Title:          "Demo",
			Source:         "ts-exec:cat /dev/null",
			SourceKind:     "exec",
			PlacementCount: count,
			SourceLocation: clusterLocation("edge-eu-1", "edge-us-1"),
		}, clusters, nil)
		if err == nil || !strings.Contains(err.Error(), "exactly one source cluster") {
			t.Fatalf("placement_count=%d across two clusters: unexpected error %v", count, err)
		}
	}
}

func TestMistNativeStreamToRendered_AcceptsNodesInItsCluster(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	nodes := []Node{{ID: "eu-edge-a", ClusterID: "edge-eu-1"}, {ID: "eu-edge-b", ClusterID: "edge-eu-1"}}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:cat /dev/null",
		SourceKind:     "exec",
		SourceLocation: &SourceLocation{Clusters: []string{"edge-eu-1"}, Nodes: []string{"eu-edge-a"}, AvoidNodes: []string{"eu-edge-b"}},
	}, clusters, nodes)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	got := rendered.SourceLocation
	if got == nil || len(got.Clusters) != 1 || strings.Join(got.Clusters[0].NodeIDs, ",") != "eu-edge-a" || strings.Join(got.AvoidNodeIDs, ",") != "eu-edge-b" {
		t.Fatalf("source location = %+v", got)
	}
}

func TestMistNativeStreamToRendered_PlacementCountDefaultsToOne(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:cat /dev/null",
		SourceKind:     "exec",
		SourceLocation: clusterLocation("edge-eu-1"),
	}, clusters, nil)
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
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:cat /dev/null",
		SourceKind:     "exec",
		Monitoring:     " ON ",
		SourceLocation: clusterLocation("edge-eu-1"),
	}, clusters, nil)
	if err != nil {
		t.Fatalf("monitoring=ON should render: %v", err)
	}
	if rendered.Monitoring != "on" {
		t.Fatalf("Monitoring=%q want on", rendered.Monitoring)
	}

	_, err = mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:cat /dev/null",
		SourceKind:     "exec",
		Monitoring:     "enabled",
		SourceLocation: clusterLocation("edge-eu-1"),
	}, clusters, nil)
	if err == nil || !strings.Contains(err.Error(), "inherit/on/off") {
		t.Fatalf("expected invalid monitoring rejection, got %v", err)
	}
}

// placement_count counts elected edge nodes, not clusters: a single source
// cluster legitimately supports placement_count > 1.
func TestMistNativeStreamToRendered_PlacementCountIsNodeCountNotClusterCount(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	rendered, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:     "demo",
		OwnerTenant:    TenantRef{Ref: "quartermaster.system_tenant"},
		Title:          "Demo",
		Source:         "ts-exec:cat /dev/null",
		SourceKind:     "exec",
		PlacementCount: 3,
		SourceLocation: clusterLocation("edge-eu-1"),
	}, clusters, nil)
	if err != nil {
		t.Fatalf("placement_count=3 with one source cluster must render: %v", err)
	}
	if rendered.PlacementCount != 3 {
		t.Fatalf("PlacementCount not preserved through render: got %d", rendered.PlacementCount)
	}
}

func TestMistNativeStreamToRendered_RejectsLegacyAllowedClusterIDs(t *testing.T) {
	clusters := []Cluster{{ID: "edge-eu-1", Type: "edge"}}
	_, err := mistNativeStreamToRendered(MistNativeStream{
		PlaybackID:              "demo",
		OwnerTenant:             TenantRef{Ref: "quartermaster.system_tenant"},
		Title:                   "Demo",
		Source:                  "ts-exec:cat /dev/null",
		SourceKind:              "exec",
		LegacyAllowedClusterIDs: []string{"edge-eu-1"},
	}, clusters, nil)
	if err == nil || !strings.Contains(err.Error(), "source_location") || !strings.Contains(err.Error(), "allowed_cluster_ids") {
		t.Fatalf("expected legacy-key rejection naming source_location, got %v", err)
	}
}
