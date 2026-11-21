package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

// A processing job's source media must NOT be placed on a node whose virtual cluster is NOT entitled to the
// job's tenant (Quartermaster cluster↔tenant entitlement); a platform-shared cluster serves any tenant.
func TestRouteProcessingJob_ExcludesCrossTenantDedicatedNode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	ctx := context.Background()

	// Injected entitlement (no live Quartermaster): cluster-b serves only tenant-b; cluster-shared serves any.
	origAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(_ context.Context, clusterID, tenantID string) bool {
		switch clusterID {
		case "cluster-shared":
			return true
		case "cluster-b":
			return tenantID == "tenant-b"
		default:
			return false
		}
	}
	t.Cleanup(func() { clusterAccessibleForTenant = origAccess })

	// Node in tenant-b's dedicated cluster, with the most spare capacity (0 in-flight).
	sm.SetNodeConnectionInfo(ctx, "byoc-b", "h", "tenant-b", "cluster-b", nil)
	sm.TouchNode("byoc-b", true)
	setNodeProcessing(sm, "byoc-b", true, 0, 0)
	// Node in a platform-shared cluster, busier.
	sm.SetNodeConnectionInfo(ctx, "shared", "h", "", "cluster-shared", nil)
	sm.TouchNode("shared", true)
	setNodeProcessing(sm, "shared", true, 0, 5)

	if id, _ := routeProcessingJob(&processingJob{TenantID: "tenant-a", OriginCluster: "cluster-shared"}); id != "shared" {
		t.Fatalf("tenant-a job originating on the platform-shared cluster must use the shared node; got %q", id)
	}
	if id, reason := routeProcessingJob(&processingJob{TenantID: "tenant-a", OriginCluster: "cluster-b"}); id != "" {
		t.Fatalf("tenant-a job must never land on tenant-b's non-entitled cluster (even at 0 load); got %q (%s)", id, reason)
	}
	if id, _ := routeProcessingJob(&processingJob{TenantID: "tenant-b", OriginCluster: "cluster-b"}); id != "byoc-b" {
		t.Fatalf("tenant-b job may use tenant-b's own entitled cluster; got %q", id)
	}
}

// A processing job runs only on nodes of its artifact's origin cluster, whose storage receives the output. An
// import accepted by the EU cluster must not be handed to an idle US edge, and a preferred node outside the
// origin is refused rather than used.
func TestRouteProcessingJob_OnlyOriginClusterNodes(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	origAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = origAccess })

	touchInCluster(sm, "us-edge", "platform-us")
	setNodeProcessing(sm, "us-edge", true, 8, 0) // idle
	touchInCluster(sm, "eu-edge", "platform-eu")
	setNodeProcessing(sm, "eu-edge", true, 8, 6) // busy, but in the origin cluster

	if id, reason := routeProcessingJob(&processingJob{TenantID: "tenant-a", OriginCluster: "platform-eu"}); id != "eu-edge" {
		t.Fatalf("EU import must run on the EU node; got %q (%s)", id, reason)
	}
	usPreferred := &processingJob{TenantID: "tenant-a", OriginCluster: "platform-eu", PreferredNode: sql.NullString{String: "us-edge", Valid: true}}
	if id, reason := routeProcessingJob(usPreferred); id != "" || reason != "preferred source node unavailable" {
		t.Fatalf("preferred node outside the origin cluster: got (%q, %q), want refusal", id, reason)
	}
	if id, reason := routeProcessingJob(&processingJob{TenantID: "tenant-a"}); id != "" || reason != "artifact origin cluster unknown" {
		t.Fatalf("unknown origin: got (%q, %q), want (\"\", artifact origin cluster unknown)", id, reason)
	}
	touchInCluster(sm, "eu-full", "platform-eu")
	if id, reason := routeProcessingJob(&processingJob{TenantID: "tenant-a", OriginCluster: "platform-apac"}); id != "" || reason != "no nodes in origin cluster platform-apac with capacity for class video_transcode" {
		t.Fatalf("origin without nodes: got (%q, %q)", id, reason)
	}
}

// Staging dispatched a URL import to a node fenced in maintenance. Maintenance
// takes a node out of all work; draining only keeps a preferred source node.
func TestRouteProcessingJob_HonorsOperationalMode(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	t.Cleanup(sm.Shutdown)
	origAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = origAccess })
	ctx := context.Background()

	touchInCluster(sm, "fenced", routeTestCluster)
	setNodeProcessing(sm, "fenced", true, 8, 0) // idle, would win on load
	if err := sm.SetNodeOperationalMode(ctx, "fenced", state.NodeModeMaintenance, "update-orchestrator"); err != nil {
		t.Fatal(err)
	}
	if id, reason := routeProcessingJob(originJob()); id != "" {
		t.Fatalf("maintenance node received new work: (%q, %q)", id, reason)
	}
	if id, _ := routeProcessingJob(preferred("fenced")); id != "" {
		t.Fatalf("maintenance node accepted preferred-source work: %q", id)
	}

	touchInCluster(sm, "open", routeTestCluster)
	setNodeProcessing(sm, "open", true, 8, 5)
	if id, _ := routeProcessingJob(originJob()); id != "open" {
		t.Fatalf("got %q, want the only node in normal mode", id)
	}

	if err := sm.SetNodeOperationalMode(ctx, "fenced", state.NodeModeDraining, "operator"); err != nil {
		t.Fatal(err)
	}
	if id, _ := routeProcessingJob(originJob()); id != "open" {
		t.Fatalf("draining node received unpinned work: %q", id)
	}
	if id, reason := routeProcessingJob(preferred("fenced")); id != "fenced" || reason != "preferred_source_node" {
		t.Fatalf("draining source node refused its own source: (%q, %q)", id, reason)
	}
}

const routeTestCluster = "cluster-eu"

func touchInCluster(sm *state.StreamStateManager, nodeID, clusterID string) {
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "h", "", clusterID, nil)
	sm.TouchNode(nodeID, true)
}

func originJob() *processingJob {
	return &processingJob{OriginCluster: routeTestCluster}
}

func preferred(nodeID string) *processingJob {
	return &processingJob{OriginCluster: routeTestCluster, PreferredNode: sql.NullString{String: nodeID, Valid: nodeID != ""}}
}

// routeProcessingJob picks the edge node for a transcode. The invariants:
//   - a viable preferred (source) node wins outright, honoring locality;
//   - a named-but-unviable preferred node fails closed to
//     "preferred source node unavailable" rather than silently rebalancing
//     elsewhere (the caller wants the source node or nothing);
//   - otherwise it spreads load by picking the fewest in-flight transcodes,
//     treating MaxTranscodes==0 as unbounded capacity.
func TestRouteProcessingJob(t *testing.T) {
	// Leave a fresh manager behind so a leftover alive node can't leak into
	// other tests in this package (the dispatcher routes to alive nodes).
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })

	// These subtests exercise capacity/class routing, not the tenant boundary — make every cluster entitled
	// so eligibility isolates to capacity. The cross-tenant boundary has its own test.
	origAccess := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = origAccess })

	t.Run("no alive nodes", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		if id, reason := routeProcessingJob(originJob()); id != "" || reason != "no alive nodes" {
			t.Errorf("got (%q,%q), want (\"\",\"no alive nodes\")", id, reason)
		}
	})

	t.Run("viable preferred node wins", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "source", routeTestCluster)
		setNodeProcessing(sm, "source", true, 4, 1)
		touchInCluster(sm, "other", routeTestCluster)
		setNodeProcessing(sm, "other", true, 4, 0) // emptier, but not preferred
		if id, reason := routeProcessingJob(preferred("source")); id != "source" || reason != "preferred_source_node" {
			t.Errorf("got (%q,%q), want (\"source\",\"preferred_source_node\")", id, reason)
		}
	})

	t.Run("preferred missing fails closed", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "other", routeTestCluster) // keeps aliveIDs non-empty
		setNodeProcessing(sm, "other", true, 4, 0)
		if id, reason := routeProcessingJob(preferred("ghost")); id != "" || reason != "preferred source node unavailable" {
			t.Errorf("got (%q,%q), want (\"\",\"preferred source node unavailable\")", id, reason)
		}
	})

	t.Run("preferred present but not processing-capable fails closed", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "source", routeTestCluster)
		setNodeProcessing(sm, "source", false, 4, 0) // alive but cannot process
		if id, reason := routeProcessingJob(preferred("source")); id != "" || reason != "preferred source node unavailable" {
			t.Errorf("got (%q,%q), want (\"\",\"preferred source node unavailable\")", id, reason)
		}
	})

	t.Run("no preference picks lowest transcode load", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "busy", routeTestCluster)
		setNodeProcessing(sm, "busy", true, 8, 5)
		touchInCluster(sm, "idle", routeTestCluster)
		setNodeProcessing(sm, "idle", true, 8, 1) // fewest in-flight
		touchInCluster(sm, "mid", routeTestCluster)
		setNodeProcessing(sm, "mid", true, 8, 3)
		if id, reason := routeProcessingJob(originJob()); id != "idle" || reason != "lowest_load:video_transcode" {
			t.Errorf("got (%q,%q), want (\"idle\",\"lowest_load:video_transcode\")", id, reason)
		}
	})

	t.Run("slots_total 0 is unbounded and eligible", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "unbounded", routeTestCluster)
		setNodeProcessing(sm, "unbounded", true, 0, 99) // 99 in-flight but no cap
		if id, reason := routeProcessingJob(originJob()); id != "unbounded" || reason != "lowest_load:video_transcode" {
			t.Errorf("got (%q,%q), want (\"unbounded\",\"lowest_load:video_transcode\")", id, reason)
		}
	})

	t.Run("all capable nodes full yields none available", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		touchInCluster(sm, "full", routeTestCluster)
		setNodeProcessing(sm, "full", true, 2, 2) // at capacity
		if id, reason := routeProcessingJob(originJob()); id != "" || reason != "no nodes in origin cluster cluster-eu with capacity for class video_transcode" {
			t.Errorf("got (%q,%q), want (\"\",\"no nodes in origin cluster cluster-eu with capacity for class video_transcode\")", id, reason)
		}
	})

	t.Run("node advertising a different class is not eligible", func(t *testing.T) {
		sm := state.ResetDefaultManagerForTests()
		t.Cleanup(sm.Shutdown)
		// Processing-capable, but only advertises ai_inference capacity — a
		// video_transcode job must not land here.
		touchInCluster(sm, "infer", routeTestCluster)
		setNodeClassCapacity(sm, "infer", map[string]state.ClassCapacity{
			mist.ProcessingClassAIInference: {Total: 4, Used: 0},
		})
		if id, reason := routeProcessingJob(originJob()); id != "" || reason != "no nodes in origin cluster cluster-eu with capacity for class video_transcode" {
			t.Errorf("got (%q,%q), want (\"\",\"no nodes in origin cluster cluster-eu with capacity for class video_transcode\")", id, reason)
		}
	})
}

func TestRouteProcessingJobSkipsInsufficientRAM(t *testing.T) {
	useRuntimeProcessingInventory(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	old := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = old })
	for _, id := range []string{"small", "large"} {
		touchInCluster(sm, id, routeTestCluster)
		setNodeProcessing(sm, id, true, 8, 0)
	}
	// Node heartbeats already report RAMMax; preserve the other advertised metrics.
	setNodeMemory(sm, "small", 1536*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	setNodeMemory(sm, "large", 8*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	job := originJob()
	job.RequiredMemoryBytes = 2 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "large" {
		t.Fatalf("memory-aware route=%s (%s), want large", id, reason)
	}
	job.RequiredMemoryBytes = 16 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason != "no node in this cluster has enough memory for this ladder" {
		t.Fatalf("impossible job not refused clearly: %s %s", id, reason)
	}
}

func setNodeMemory(sm *state.StreamStateManager, nodeID string, ram float64, classes map[string]state.ClassCapacity) {
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
		ProcessingClasses    map[string]state.ClassCapacity
	}{RAMMax: ram,
		CapProcessing:     true,
		ProcessingClasses: classes,
	})
	sm.SetNodeProcessingMemoryBudget(nodeID, mist.ProcessingMemoryBudget(uint64(ram)))
}

func TestRegisteredLargeNodeMayRecoverBeforePlacement(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	old := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = old })
	for _, id := range []string{"small", "large"} {
		touchInCluster(sm, id, routeTestCluster)
	}
	setNodeMemory(sm, "small", 4*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	setNodeMemory(sm, "large", 32*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	sm.MarkNodeDisconnected("large")
	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason == mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("temporary large-node absence made job permanently impossible: %s %s", id, reason)
	}
	sm.TouchNode("large", true)
	if id, reason := routeProcessingJob(job); id != "large" {
		t.Fatalf("recovered large node not selected: %s %s", id, reason)
	}
}

func TestMistMemoryLifecyclePlacesUsingEffectiveBudget(t *testing.T) {
	useRuntimeProcessingInventory(t)
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	old := clusterAccessibleForTenant
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	t.Cleanup(func() { clusterAccessibleForTenant = old })
	report := func(id string, hostGiB uint64, budget uint64) {
		t.Helper()
		update := &ipcpb.NodeLifecycleUpdate{NodeId: id, IsHealthy: true, BaseUrl: "http://" + id + ".invalid", Capabilities: &ipcpb.NodeCapabilities{Processing: true}, Limits: &ipcpb.NodeLimits{ProcessingClasses: []*ipcpb.ProcessingClassCapacity{{Class: mist.ProcessingClassVideoTranscode, SlotsTotal: 8}}}, ProcessingMemoryBudgetBytes: &budget}
		mist.ApplyMemoryMetrics(update, map[string]any{"mem_total": float64(hostGiB * 1024 * 1024), "mem_used": float64(1024 * 1024)})
		data, err := proto.Marshal(update)
		if err != nil {
			t.Fatal(err)
		}
		received := &ipcpb.NodeLifecycleUpdate{}
		if err = proto.Unmarshal(data, received); err != nil {
			t.Fatal(err)
		}
		touchInCluster(sm, id, routeTestCluster)
		if err = sm.ApplyNodeLifecycle(context.Background(), received); err != nil {
			t.Fatal(err)
		}
	}
	report("container", 16, mist.ProcessingMemoryBudget(3*1024*1024*1024))
	node := sm.GetNodeState("container")
	if node.RAMMax != 16*1024*1024*1024 || node.RAMCurrent != 1024*1024*1024 {
		t.Fatalf("Mist-shaped metrics lost byte units: %+v", node)
	}
	job := originJob()
	job.RequiredMemoryBytes = 2 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "container" {
		t.Fatalf("16 GiB host cannot fit 2 GiB job: %s %s", id, reason)
	}
	job.RequiredMemoryBytes = 4 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason != mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("host RAM overrode 3 GiB container: %s %s", id, reason)
	}
	report("larger", 16, mist.ProcessingMemoryBudget(8*1024*1024*1024))
	if id, reason := routeProcessingJob(job); id != "larger" {
		t.Fatalf("larger effective budget not selected: %s %s", id, reason)
	}
	job.MemoryRejectedNodes = []string{"larger"}
	report("larger", 16, mist.ProcessingMemoryBudget(3*1024*1024*1024))
	if id, reason := routeProcessingJob(job); id != "" || reason != mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("undersized refused node was reused: %s %s", id, reason)
	}
	report("larger", 16, mist.ProcessingMemoryBudget(8*1024*1024*1024))
	if id, reason := routeProcessingJob(job); id != "larger" {
		t.Fatalf("budget increase did not restore refused node: %s %s", id, reason)
	}
}

func TestRegisteredNodeMissingFromRuntimeIsNotImpossible(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	oldAccess, oldInventory := clusterAccessibleForTenant, registeredProcessingNodes
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	registeredProcessingNodes = func(context.Context, string) ([]string, error) { return []string{"small", "evicted-large"}, nil }
	t.Cleanup(func() { clusterAccessibleForTenant, registeredProcessingNodes = oldAccess, oldInventory })
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 4*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason == mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("evicted registered node turned unknown capacity into permanent failure: %s %s", id, reason)
	}
}

func useRuntimeProcessingInventory(t *testing.T) {
	t.Helper()
	previous := registeredProcessingNodes
	registeredProcessingNodes = func(_ context.Context, clusterID string) ([]string, error) {
		var ids []string
		for _, n := range state.DefaultManager().GetAllNodesSnapshot().Nodes {
			if n.ClusterID == clusterID {
				ids = append(ids, n.NodeID)
			}
		}
		return ids, nil
	}
	t.Cleanup(func() { registeredProcessingNodes = previous })
}

func TestInventoryOutageCannotProvePermanentCapacityFailure(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	t.Cleanup(func() { state.ResetDefaultManagerForTests() })
	oldAccess, oldInventory := clusterAccessibleForTenant, registeredProcessingNodes
	clusterAccessibleForTenant = func(context.Context, string, string) bool { return true }
	registeredProcessingNodes = func(context.Context, string) ([]string, error) {
		return nil, fmt.Errorf("temporary Quartermaster outage")
	}
	t.Cleanup(func() { clusterAccessibleForTenant, registeredProcessingNodes = oldAccess, oldInventory })
	touchInCluster(sm, "small", routeTestCluster)
	setNodeMemory(sm, "small", 4*1024*1024*1024, map[string]state.ClassCapacity{mist.ProcessingClassVideoTranscode: {Total: 8}})
	job := originJob()
	job.RequiredMemoryBytes = 6 * 1024 * 1024 * 1024
	if id, reason := routeProcessingJob(job); id != "" || reason == mist.ProcessingMemoryNoNodeReason {
		t.Fatalf("inventory outage failed upload permanently: %s %s", id, reason)
	}
}

// Unit routing checks use a warm inventory; dispatcher tests cover restart grace.
func routeProcessingJob(job *processingJob) (string, string) {
	node, reason, _ := routeProcessingJobWithInventory(context.Background(), job, newProcessingTickInventory(true))
	return node, reason
}
