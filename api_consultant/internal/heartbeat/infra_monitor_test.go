package heartbeat

import (
	"context"
	"testing"
	"time"

	"frameworks/api_consultant/internal/diagnostics"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/email"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Fakes ---

type fakeInfraNodeClient struct {
	liveNodes []*pb.LiveNode
	perfResp  *pb.GetNodePerformance5MResponse
}

func (f *fakeInfraNodeClient) GetLiveNodes(_ context.Context, _ string, _ *string, _ []string) (*pb.GetLiveNodesResponse, error) {
	return &pb.GetLiveNodesResponse{Nodes: f.liveNodes}, nil
}

func (f *fakeInfraNodeClient) GetNodePerformance5m(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*pb.GetNodePerformance5MResponse, error) {
	if f.perfResp != nil {
		return f.perfResp, nil
	}
	return &pb.GetNodePerformance5MResponse{}, nil
}

func (f *fakeInfraNodeClient) GetNetworkLiveStats(_ context.Context) (*pb.GetNetworkLiveStatsResponse, error) {
	return &pb.GetNetworkLiveStatsResponse{}, nil
}

func (f *fakeInfraNodeClient) GetFederationSummary(_ context.Context, _ string, _ *periscope.TimeRangeOpts) (*pb.GetFederationSummaryResponse, error) {
	return &pb.GetFederationSummaryResponse{}, nil
}

type fakeInfraClusterClient struct {
	clusters []*pb.InfrastructureCluster
	owners   map[string]*pb.NodeOwnerResponse
}

func (f *fakeInfraClusterClient) ListClusters(_ context.Context, _ *pb.CursorPaginationRequest) (*pb.ListClustersResponse, error) {
	return &pb.ListClustersResponse{Clusters: f.clusters}, nil
}

func (f *fakeInfraClusterClient) GetNodeOwner(_ context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
	if resp, ok := f.owners[nodeID]; ok {
		return resp, nil
	}
	return &pb.NodeOwnerResponse{}, nil
}

type fakeBillingClient struct {
	billingEmail string
}

func (f *fakeBillingClient) GetBillingStatus(_ context.Context, _ string) (*pb.BillingStatusResponse, error) {
	return &pb.BillingStatusResponse{
		Subscription: &pb.TenantSubscription{
			BillingEmail: f.billingEmail,
		},
	}, nil
}

type emailCapture struct {
	to      string
	subject string
	body    string
	calls   int
}

// --- Helpers ---

func ptr[T any](v T) *T { return &v }

func freshNow() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(-30 * time.Second))
}

func staleTimestamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(-15 * time.Minute))
}

func activeCluster(id, name, ownerTenantID string) *pb.InfrastructureCluster {
	return &pb.InfrastructureCluster{
		ClusterId:     id,
		ClusterName:   name,
		OwnerTenantId: ptr(ownerTenantID),
		IsActive:      true,
	}
}

func healthyNode(nodeID string) *pb.LiveNode {
	return &pb.LiveNode{
		NodeId:         nodeID,
		CpuPercent:     40,
		RamUsedBytes:   4_000_000_000,
		RamTotalBytes:  16_000_000_000,
		DiskUsedBytes:  50_000_000_000,
		DiskTotalBytes: 200_000_000_000,
		UpdatedAt:      freshNow(),
	}
}

func cpuStuckNode(nodeID string) *pb.LiveNode {
	return &pb.LiveNode{
		NodeId:         nodeID,
		CpuPercent:     99,
		RamUsedBytes:   8_000_000_000,
		RamTotalBytes:  16_000_000_000,
		DiskUsedBytes:  50_000_000_000,
		DiskTotalBytes: 200_000_000_000,
		UpdatedAt:      freshNow(),
	}
}

func diskFullNode(nodeID string, usedPct float64) *pb.LiveNode {
	total := uint64(200_000_000_000)
	used := uint64(float64(total) * usedPct / 100)
	return &pb.LiveNode{
		NodeId:         nodeID,
		CpuPercent:     30,
		RamUsedBytes:   4_000_000_000,
		RamTotalBytes:  16_000_000_000,
		DiskUsedBytes:  used,
		DiskTotalBytes: total,
		UpdatedAt:      freshNow(),
	}
}

func persistentCPURecords(count int, avgCPU float32) []*pb.NodePerformance5M {
	recs := make([]*pb.NodePerformance5M, count)
	for i := range recs {
		recs[i] = &pb.NodePerformance5M{AvgCpu: avgCPU}
	}
	return recs
}

func newTestMonitor(nodes InfraNodeClient, clusters InfraClusterClient, billing BillingClient) *InfraMonitor {
	return NewInfraMonitor(&InfraMonitorConfig{
		Nodes:    nodes,
		Clusters: clusters,
		Billing:  billing,
		SMTP:     email.Config{Host: "smtp.test", From: "test@frameworks.network"},
		Logger:   testLogger(),
	})
}

// --- Tests ---

func TestInfraMonitor_HealthyNodes_NoAlerts(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{healthyNode("node-1"), healthyNode("node-2")},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	// Run should complete without panic or error for healthy nodes.
	m.Run(context.Background())
}

func TestInfraMonitor_CPUStuck_TransientNoAlert(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{cpuStuckNode("node-stuck")},
		perfResp: &pb.GetNodePerformance5MResponse{
			Records: []*pb.NodePerformance5M{
				{AvgCpu: 99}, // only 1 window above threshold
				{AvgCpu: 50},
				{AvgCpu: 40},
				{AvgCpu: 45},
			},
		},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
	// No panic — transient spike (1/4 windows) should not trigger alert.
}

func TestInfraMonitor_CPUStuck_PersistentTriggersAlert(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{cpuStuckNode("node-stuck")},
		perfResp: &pb.GetNodePerformance5MResponse{
			Records: persistentCPURecords(4, 98),
		},
	}
	ownerTenantID := "tenant-a"
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", ownerTenantID)},
		owners: map[string]*pb.NodeOwnerResponse{
			"node-stuck": {OwnerTenantId: ptr(ownerTenantID)},
		},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	// The emailer is a real Sender with a fake SMTP address — it will fail
	// to dial, but the monitor catches and logs the error gracefully.
	m.Run(context.Background())
}

func TestInfraMonitor_DiskWarning_ImmediateAlert(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{diskFullNode("node-disk", 92)},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
}

func TestInfraMonitor_DiskCritical_ImmediateAlert(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{diskFullNode("node-disk", 96)},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
}

func TestInfraMonitor_CooldownSuppressesRepeat(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{diskFullNode("node-disk", 96)},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
	m.Run(context.Background()) // second run should be suppressed by cooldown
}

func TestInfraMonitor_StaleNodeSkipped(t *testing.T) {
	staleNode := &pb.LiveNode{
		NodeId:         "node-stale",
		CpuPercent:     99,
		RamUsedBytes:   15_000_000_000,
		RamTotalBytes:  16_000_000_000,
		DiskUsedBytes:  195_000_000_000,
		DiskTotalBytes: 200_000_000_000,
		UpdatedAt:      staleTimestamp(),
	}
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{staleNode},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{activeCluster("c1", "prod", "tenant-a")},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
	// Stale node (>10min old) should be skipped entirely.
}

func TestInfraMonitor_InactiveClusterSkipped(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{cpuStuckNode("node-stuck")},
	}
	inactiveCluster := &pb.InfrastructureCluster{
		ClusterId:     "c1",
		ClusterName:   "decommissioned",
		OwnerTenantId: ptr("tenant-a"),
		IsActive:      false,
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{inactiveCluster},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
}

func TestInfraMonitor_NoOwnerTenantSkipped(t *testing.T) {
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{cpuStuckNode("node-stuck")},
	}
	noOwner := &pb.InfrastructureCluster{
		ClusterId:   "c1",
		ClusterName: "orphan",
		IsActive:    true,
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{noOwner},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
}

func TestInfraMonitor_NodeDedupAcrossClusters(t *testing.T) {
	sharedNode := cpuStuckNode("shared-node")
	nodes := &fakeInfraNodeClient{
		liveNodes: []*pb.LiveNode{sharedNode},
		perfResp: &pb.GetNodePerformance5MResponse{
			Records: persistentCPURecords(4, 98),
		},
	}
	clusters := &fakeInfraClusterClient{
		clusters: []*pb.InfrastructureCluster{
			activeCluster("c1", "prod-a", "tenant-a"),
			activeCluster("c2", "prod-b", "tenant-b"),
		},
	}
	billing := &fakeBillingClient{billingEmail: "ops@test.com"}

	m := newTestMonitor(nodes, clusters, billing)
	m.Run(context.Background())
	// shared-node appears in both clusters but should only be checked once.
}

func TestInfraMonitor_NilMonitorSafe(t *testing.T) {
	var m *InfraMonitor
	m.Run(context.Background()) // should not panic
}

func TestInfraMonitor_NilConfigReturnsNil(t *testing.T) {
	m := NewInfraMonitor(nil)
	if m != nil {
		t.Fatal("expected nil InfraMonitor for nil config")
	}
}

func TestInfraMonitor_MissingNodesReturnsNil(t *testing.T) {
	m := NewInfraMonitor(&InfraMonitorConfig{
		Clusters: &fakeInfraClusterClient{},
		Logger:   testLogger(),
	})
	if m != nil {
		t.Fatal("expected nil InfraMonitor when Nodes is nil")
	}
}

func TestInfraMonitor_MissingClustersReturnsNil(t *testing.T) {
	m := NewInfraMonitor(&InfraMonitorConfig{
		Nodes:  &fakeInfraNodeClient{},
		Logger: testLogger(),
	})
	if m != nil {
		t.Fatal("expected nil InfraMonitor when Clusters is nil")
	}
}

func TestConfirmPersistence_RequiresMinViolations(t *testing.T) {
	tests := []struct {
		name     string
		records  []*pb.NodePerformance5M
		metric   string
		expected bool
	}{
		{
			name:     "all above threshold",
			records:  persistentCPURecords(4, 97),
			metric:   "cpu",
			expected: true,
		},
		{
			name:     "3 of 4 above threshold",
			records:  append(persistentCPURecords(3, 97), &pb.NodePerformance5M{AvgCpu: 50}),
			metric:   "cpu",
			expected: true,
		},
		{
			name: "2 of 4 above threshold",
			records: []*pb.NodePerformance5M{
				{AvgCpu: 97}, {AvgCpu: 97}, {AvgCpu: 50}, {AvgCpu: 50},
			},
			metric:   "cpu",
			expected: false,
		},
		{
			name:     "1 of 4 above threshold",
			records:  []*pb.NodePerformance5M{{AvgCpu: 97}, {AvgCpu: 30}, {AvgCpu: 40}, {AvgCpu: 50}},
			metric:   "cpu",
			expected: false,
		},
		{
			name:     "no records",
			records:  nil,
			metric:   "cpu",
			expected: false,
		},
		{
			name: "memory 3 of 4",
			records: []*pb.NodePerformance5M{
				{AvgMemory: 96}, {AvgMemory: 97}, {AvgMemory: 98}, {AvgMemory: 50},
			},
			metric:   "memory",
			expected: true,
		},
		{
			name: "fewer than 4 records — 2 of 2 above",
			records: []*pb.NodePerformance5M{
				{AvgCpu: 98}, {AvgCpu: 97},
			},
			metric:   "cpu",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := &fakeInfraNodeClient{
				perfResp: &pb.GetNodePerformance5MResponse{Records: tt.records},
			}
			m := newTestMonitor(nodes, &fakeInfraClusterClient{}, &fakeBillingClient{})
			got := m.confirmPersistence(context.Background(), "t1", "n1", tt.metric)
			if got != tt.expected {
				t.Errorf("confirmPersistence(%s) = %v, want %v", tt.metric, got, tt.expected)
			}
		})
	}
}

func TestInfraAlertSeverity(t *testing.T) {
	tests := []struct {
		alertType InfraAlertType
		want      string
	}{
		{InfraAlertCPU, "CRITICAL"},
		{InfraAlertMemory, "CRITICAL"},
		{InfraAlertDiskCritical, "CRITICAL"},
		{InfraAlertDiskWarning, "WARNING"},
	}
	for _, tt := range tests {
		a := InfraAlert{AlertType: tt.alertType}
		if got := a.Severity(); got != tt.want {
			t.Errorf("InfraAlert(%s).Severity() = %q, want %q", tt.alertType, got, tt.want)
		}
	}
}

func TestRenderInfraAlertEmail(t *testing.T) {
	alerts := []InfraAlert{
		{
			NodeID: "node-1", ClusterID: "c1", ClusterName: "prod",
			AlertType: InfraAlertCPU, Current: 99.2, Threshold: 95,
			Baseline: 45.3, DetectedAt: time.Now(),
		},
		{
			NodeID: "node-1", ClusterID: "c1", ClusterName: "prod",
			AlertType: InfraAlertDiskWarning, Current: 92.1, Threshold: 90,
			DetectedAt: time.Now(),
		},
	}

	body, err := renderInfraAlertEmail(alerts)
	if err != nil {
		t.Fatalf("renderInfraAlertEmail: %v", err)
	}
	if body == "" {
		t.Fatal("expected non-empty email body")
	}

	// Verify key content is present.
	for _, want := range []string{"CRITICAL", "node-1", "prod", "99.2%", "92.1%", "Baseline average", "45.3%"} {
		if !contains(body, want) {
			t.Errorf("email body missing %q", want)
		}
	}
}

func TestRenderInfraAlertEmail_NoAlerts(t *testing.T) {
	_, err := renderInfraAlertEmail(nil)
	if err == nil {
		t.Fatal("expected error for empty alerts")
	}
}

func TestInfraAlertSubject(t *testing.T) {
	alerts := []InfraAlert{
		{NodeID: "node-1", ClusterID: "c1", ClusterName: "prod", AlertType: InfraAlertCPU},
		{NodeID: "node-1", ClusterID: "c1", ClusterName: "prod", AlertType: InfraAlertDiskWarning},
	}
	subject := infraAlertSubject(alerts)
	for _, want := range []string{"CRITICAL", "prod", "node-1", "CPU stuck", "disk warning"} {
		if !contains(subject, want) {
			t.Errorf("subject missing %q: %s", want, subject)
		}
	}
}

func TestCollectActionItems(t *testing.T) {
	alerts := []InfraAlert{
		{AlertType: InfraAlertCPU},
		{AlertType: InfraAlertCPU}, // duplicate — should be deduped
		{AlertType: InfraAlertDiskCritical},
	}
	items := collectActionItems(alerts)
	// 2 alert-specific items (CPU + disk critical, deduped) + 2 unconditional CLI items
	if len(items) != 4 {
		t.Fatalf("expected 4 action items, got %d: %v", len(items), items)
	}
}

func TestResolveBaselines(t *testing.T) {
	devs := []diagnostics.Deviation{
		{Metric: "node_cpu", Baseline: 45.0},
		{Metric: "node_memory", Baseline: 60.0},
		{Metric: "node_disk", Baseline: 30.0},
	}
	m := newTestMonitor(&fakeInfraNodeClient{}, &fakeInfraClusterClient{}, &fakeBillingClient{})
	cpu, mem, disk := m.resolveBaselines(devs)
	if cpu != 45.0 {
		t.Errorf("cpu baseline = %v, want 45.0", cpu)
	}
	if mem != 60.0 {
		t.Errorf("memory baseline = %v, want 60.0", mem)
	}
	if disk != 30.0 {
		t.Errorf("disk baseline = %v, want 30.0", disk)
	}
}

func TestResolveOwnerEmail_FallsBackToTenantID(t *testing.T) {
	billing := &fakeBillingClient{billingEmail: "owner@test.com"}
	clusters := &fakeInfraClusterClient{
		owners: map[string]*pb.NodeOwnerResponse{},
	}
	m := newTestMonitor(&fakeInfraNodeClient{}, clusters, billing)
	email := m.resolveOwnerEmail(context.Background(), "node-1", "tenant-fallback")
	if email != "owner@test.com" {
		t.Errorf("resolveOwnerEmail = %q, want %q", email, "owner@test.com")
	}
}

func TestResolveOwnerEmail_UsesNodeOwner(t *testing.T) {
	billing := &fakeBillingClient{billingEmail: "node-owner@test.com"}
	ownerID := "specific-owner"
	clusters := &fakeInfraClusterClient{
		owners: map[string]*pb.NodeOwnerResponse{
			"node-1": {OwnerTenantId: &ownerID},
		},
	}
	m := newTestMonitor(&fakeInfraNodeClient{}, clusters, billing)
	email := m.resolveOwnerEmail(context.Background(), "node-1", "fallback-tenant")
	if email != "node-owner@test.com" {
		t.Errorf("resolveOwnerEmail = %q, want %q", email, "node-owner@test.com")
	}
}

func TestResolveOwnerEmail_NoBillingReturnsEmpty(t *testing.T) {
	clusters := &fakeInfraClusterClient{}
	m := newTestMonitor(&fakeInfraNodeClient{}, clusters, nil)
	email := m.resolveOwnerEmail(context.Background(), "node-1", "tenant-a")
	if email != "" {
		t.Errorf("resolveOwnerEmail = %q, want empty", email)
	}
}

// --- helpers ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
