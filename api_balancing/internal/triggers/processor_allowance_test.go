package triggers

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// ============================================================================
// Pure policy tests — applyLoadGate is the three-tier decision function.
// ============================================================================

func TestApplyLoadGatePaidAlwaysAdmits(t *testing.T) {
	th := loadThresholds{rejectOverAllowance: 0.5, rejectAnyFree: 0.95}
	for _, load := range []float64{0.0, 0.49, 0.5, 0.8, 0.95, 0.99, 1.0} {
		// isFreeTier=false → admit at every load level
		if got := applyLoadGate(load, false, true, th); got != admissionAdmit {
			t.Errorf("paid tenant load=%.2f: got %v, want admit", load, got)
		}
	}
}

func TestApplyLoadGateFreeUnderAllowanceAdmittedUntilRedline(t *testing.T) {
	th := loadThresholds{rejectOverAllowance: 0.5, rejectAnyFree: 0.95}
	cases := []struct {
		load float64
		want admissionDecision
	}{
		{0.0, admissionAdmit},
		{0.49, admissionAdmit},
		{0.50, admissionAdmit}, // under allowance: 50% gate does not apply
		{0.80, admissionAdmit},
		{0.94, admissionAdmit},
		{0.95, admissionRejectRedline}, // redline
		{0.99, admissionRejectRedline},
	}
	for _, tc := range cases {
		if got := applyLoadGate(tc.load, true, false, th); got != tc.want {
			t.Errorf("free under-allowance load=%.2f: got %v, want %v", tc.load, got, tc.want)
		}
	}
}

func TestApplyLoadGateFreeOverAllowanceRejectedAt50Percent(t *testing.T) {
	th := loadThresholds{rejectOverAllowance: 0.5, rejectAnyFree: 0.95}
	cases := []struct {
		load float64
		want admissionDecision
	}{
		{0.0, admissionAdmit},
		{0.49, admissionAdmit},
		{0.50, admissionRejectOverAllowance},
		{0.80, admissionRejectOverAllowance},
		{0.94, admissionRejectOverAllowance},
		{0.95, admissionRejectRedline}, // redline takes precedence
		{0.99, admissionRejectRedline},
	}
	for _, tc := range cases {
		if got := applyLoadGate(tc.load, true, true, th); got != tc.want {
			t.Errorf("free over-allowance load=%.2f: got %v, want %v", tc.load, got, tc.want)
		}
	}
}

func TestApplyLoadGateRedlinePrecedesOverAllowance(t *testing.T) {
	// At redline, both free over- and under-allowance get the redline reason.
	th := loadThresholds{rejectOverAllowance: 0.5, rejectAnyFree: 0.95}
	if got := applyLoadGate(0.96, true, true, th); got != admissionRejectRedline {
		t.Errorf("over-allowance at redline must use redline reason, got %v", got)
	}
	if got := applyLoadGate(0.96, true, false, th); got != admissionRejectRedline {
		t.Errorf("under-allowance at redline must use redline reason, got %v", got)
	}
}

// ============================================================================
// freeTierAllowanceState — derive admission flags from allowance list.
// ============================================================================

func TestFreeTierAllowanceState(t *testing.T) {
	cases := []struct {
		name        string
		allowances  []*purserpb.MeterAllowance
		wantFree    bool
		wantExhaust bool
	}{
		{"nil list", nil, false, false},
		{"empty list", []*purserpb.MeterAllowance{}, false, false},
		{
			"paid only",
			[]*purserpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: false, Exhausted: true}},
			false, false,
		},
		{
			"free under allowance",
			[]*purserpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: true, Exhausted: false}},
			true, false,
		},
		{
			"free exhausted",
			[]*purserpb.MeterAllowance{{Meter: "delivered_minutes", IsFreeTier: true, Exhausted: true}},
			true, true,
		},
		{
			"mixed: free meter exhausted plus paid meter not",
			[]*purserpb.MeterAllowance{
				{Meter: "delivered_minutes", IsFreeTier: true, Exhausted: true},
				{Meter: "storage", IsFreeTier: false, Exhausted: false},
			},
			true, true,
		},
		{
			"nil entry skipped",
			[]*purserpb.MeterAllowance{nil, {Meter: "delivered_minutes", IsFreeTier: true, Exhausted: false}},
			true, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isFree, exhausted := freeTierAllowanceState(tc.allowances)
			if isFree != tc.wantFree || exhausted != tc.wantExhaust {
				t.Errorf("got (free=%v exhausted=%v), want (free=%v exhausted=%v)",
					isFree, exhausted, tc.wantFree, tc.wantExhaust)
			}
		})
	}
}

// ============================================================================
// Env-var thresholds — defaults, valid overrides, garbage falls back.
// ============================================================================

func TestIngestLoadThresholdsDefaults(t *testing.T) {
	t.Setenv("FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD", "")
	t.Setenv("FOGHORN_INGEST_REJECT_FREE_LOAD", "")
	got := ingestLoadThresholds()
	if got.rejectOverAllowance != 0.5 || got.rejectAnyFree != 0.95 {
		t.Errorf("defaults: got %+v, want over=0.5 redline=0.95", got)
	}
}

func TestIngestLoadThresholdsOverridesAndFallback(t *testing.T) {
	t.Setenv("FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD", "0.4")
	t.Setenv("FOGHORN_INGEST_REJECT_FREE_LOAD", "0.85")
	got := ingestLoadThresholds()
	if got.rejectOverAllowance != 0.4 || got.rejectAnyFree != 0.85 {
		t.Errorf("overrides: got %+v", got)
	}

	t.Setenv("FOGHORN_INGEST_REJECT_OVER_ALLOWANCE_LOAD", "garbage")
	t.Setenv("FOGHORN_INGEST_REJECT_FREE_LOAD", "1.5") // out of range
	got = ingestLoadThresholds()
	if got.rejectOverAllowance != 0.5 || got.rejectAnyFree != 0.95 {
		t.Errorf("garbage/out-of-range fall back to defaults: got %+v", got)
	}
}

func TestViewerLoadThresholdsDefaults(t *testing.T) {
	t.Setenv("FOGHORN_VIEWER_REJECT_OVER_ALLOWANCE_LOAD", "")
	t.Setenv("FOGHORN_VIEWER_REJECT_FREE_LOAD", "")
	got := viewerLoadThresholds()
	if got.rejectOverAllowance != 0.8 || got.rejectAnyFree != 0.95 {
		t.Errorf("viewer defaults: got %+v, want over=0.8 redline=0.95", got)
	}
}

// ============================================================================
// Integration: evaluateFreeTierAdmission and evaluateViewerAdmission with
// real state.DefaultManager seeded for a synthetic cluster.
// ============================================================================

func TestEvaluateFreeTierAdmissionPaidTenantAlwaysAdmitted(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 95)

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "paid-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			Meter: "delivered_minutes", IsFreeTier: false, Exhausted: false,
		}},
	}
	if reason, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a"); blocked {
		t.Fatalf("paid tenant must always admit, got reason=%q", reason)
	}
}

func TestEvaluateFreeTierAdmissionFreeUnderAllowanceAtMidLoadAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	// 70% load: above over-allowance gate (0.5) but below redline (0.95)
	seedClusterNode(sm, "n1", "media-cluster-a", 70)

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: false, Included: 10000, Used: 5000,
		}},
	}
	if _, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a"); blocked {
		t.Fatal("free under-allowance at 70% must admit (below 95% redline)")
	}
}

func TestEvaluateFreeTierAdmissionFreeOverAllowanceAtMidLoadRejects(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 70) // > 50% over-allowance gate

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: true, Included: 10000, Used: 15000,
		}},
	}
	reason, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a")
	if !blocked {
		t.Fatal("free over-allowance at 70% must reject")
	}
	if reason == "" {
		t.Error("expected non-empty rejection reason")
	}
}

func TestEvaluateFreeTierAdmissionFreeUnderAllowanceAtRedlineRejects(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 96) // > 95% redline

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: false,
		}},
	}
	if _, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a"); !blocked {
		t.Fatal("free under-allowance at redline (>95%) must reject")
	}
}

func TestEvaluateFreeTierAdmissionFreeIdleAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 30) // < 50%

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: true, Included: 10000, Used: 15000,
		}},
	}
	if _, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a"); blocked {
		t.Fatal("free over-allowance at idle cluster (<50%) must admit")
	}
}

func TestEvaluateFreeTierAdmissionNoLoadSignalAdmits(t *testing.T) {
	state.ResetDefaultManagerForTests()
	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: true,
		}},
	}
	if _, blocked := p.evaluateFreeTierAdmission(resp, "media-cluster-a"); blocked {
		t.Fatal("missing load signal must admit (fail-open)")
	}
}

func TestEvaluateFreeTierAdmissionNoClusterContextAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 96)

	p := newTestProcessor(t)
	resp := &commodorepb.ValidateStreamKeyResponse{
		TenantId: "free-tenant",
		Allowances: []*purserpb.MeterAllowance{{
			IsFreeTier: true, Exhausted: true,
		}},
	}
	if _, blocked := p.evaluateFreeTierAdmission(resp, "   "); blocked {
		t.Fatal("empty cluster context must admit (cannot measure load)")
	}
}

// ============================================================================
// Viewer-side admission gate.
// ============================================================================

func TestEvaluateViewerAdmissionPaidStreamAlwaysAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 99)
	p := newTestProcessor(t)
	ctx := streamContext{TenantID: "paid", IsFreeTier: false}
	if _, blocked := p.evaluateViewerAdmission(ctx, "media-cluster-a"); blocked {
		t.Fatal("paid stream's viewer must always admit")
	}
}

func TestEvaluateViewerAdmissionFreeOverAllowanceAt85PctRejects(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 85) // > 80% over-allowance viewer gate

	p := newTestProcessor(t)
	ctx := streamContext{TenantID: "free-tenant", IsFreeTier: true, AllowanceExhausted: true}
	reason, blocked := p.evaluateViewerAdmission(ctx, "media-cluster-a")
	if !blocked {
		t.Fatal("viewer of free over-allowance stream at 85%% must reject")
	}
	if reason == "" {
		t.Error("expected non-empty rejection reason")
	}
}

func TestEvaluateViewerAdmissionFreeUnderAllowanceAt85PctAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 85)

	p := newTestProcessor(t)
	ctx := streamContext{TenantID: "free-tenant", IsFreeTier: true, AllowanceExhausted: false}
	if _, blocked := p.evaluateViewerAdmission(ctx, "media-cluster-a"); blocked {
		t.Fatal("viewer of free under-allowance stream at 85%% (<95%% redline) must admit")
	}
}

func TestEvaluateViewerAdmissionFreeUnderAllowanceAtRedlineRejects(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 96)

	p := newTestProcessor(t)
	ctx := streamContext{TenantID: "free-tenant", IsFreeTier: true, AllowanceExhausted: false}
	if _, blocked := p.evaluateViewerAdmission(ctx, "media-cluster-a"); !blocked {
		t.Fatal("viewer of free under-allowance stream at redline must reject")
	}
}

func TestEvaluateViewerAdmissionFreeIdleAdmits(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()
	seedClusterNode(sm, "n1", "media-cluster-a", 30)

	p := newTestProcessor(t)
	ctx := streamContext{TenantID: "free-tenant", IsFreeTier: true, AllowanceExhausted: true}
	if _, blocked := p.evaluateViewerAdmission(ctx, "media-cluster-a"); blocked {
		t.Fatal("viewer at idle cluster must admit even when broadcaster is over-allowance")
	}
}

// ============================================================================
// State seeding helper.
// ============================================================================

func seedClusterNode(sm *state.StreamStateManager, nodeID, clusterID string, cpuPercent float64) {
	sm.SetNodeInfo(nodeID, "", true, nil, nil, "", "", nil)
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
	}{CPU: cpuPercent})
	sm.SetNodeConnectionInfo(context.Background(), nodeID, "", "", clusterID, nil)
	// TouchNode refreshes LastHeartbeat and clears IsStale so the node passes
	// the healthy-and-fresh filter in ClusterLoad.
	sm.TouchNode(nodeID, true)
}
