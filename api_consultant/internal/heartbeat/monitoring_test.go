package heartbeat

import (
	"context"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// TestMonitoredTruthTable exhaustively checks the layered decision across all
// combinations of tenant-wide switch, tier entitlement, and per-stream toggle.
func TestMonitoredTruthTable(t *testing.T) {
	toggles := []struct {
		name string
		tog  streamToggle
	}{
		{"inherit", toggleInherit},
		{"on", toggleOn},
		{"off", toggleOff},
	}
	for _, tw := range []bool{false, true} {
		for _, te := range []bool{false, true} {
			for _, tg := range toggles {
				got := monitored(tw, te, tg.tog)
				want := tw && tg.tog != toggleOff &&
					(tg.tog == toggleOn || (tg.tog == toggleInherit && te))
				if got != want {
					t.Errorf("monitored(tenantWide=%v, tierEntitled=%v, %s)=%v want %v", tw, te, tg.name, got, want)
				}
			}
		}
	}
}

func TestMonitoredSpecificCases(t *testing.T) {
	// OFF always wins, even tenant-wide on + tier-entitled.
	if monitored(true, true, toggleOff) {
		t.Error("OFF stream must never be monitored")
	}
	// ON overrides a non-entitled (free-tier) tenant, matching the demo-stream case.
	if !monitored(true, false, toggleOn) {
		t.Error("ON stream must be monitored regardless of tier")
	}
	// INHERIT on a non-entitled tenant is not monitored.
	if monitored(true, false, toggleInherit) {
		t.Error("INHERIT on unentitled tenant must not be monitored")
	}
	// Tenant-wide off suppresses even an explicit ON.
	if monitored(false, true, toggleOn) {
		t.Error("tenant-wide off must suppress everything")
	}
}

func TestTenantMonitoringEligibleAndCoversAll(t *testing.T) {
	cases := []struct {
		name      string
		tm        tenantMonitoring
		eligible  bool
		coversAll bool
	}{
		{"tenant-wide off", tenantMonitoring{TenantWideEnabled: false, Monitored: []string{"a"}, AllStreamCount: 1}, false, false},
		{"no monitored", tenantMonitoring{TenantWideEnabled: true, Monitored: nil, AllStreamCount: 3}, false, false},
		{"partial", tenantMonitoring{TenantWideEnabled: true, Monitored: []string{"a"}, AllStreamCount: 3}, true, false},
		{"covers all", tenantMonitoring{TenantWideEnabled: true, Monitored: []string{"a", "b"}, AllStreamCount: 2}, true, true},
	}
	for _, c := range cases {
		if got := c.tm.eligible(); got != c.eligible {
			t.Errorf("%s: eligible()=%v want %v", c.name, got, c.eligible)
		}
		if got := c.tm.coversAll(); got != c.coversAll {
			t.Errorf("%s: coversAll()=%v want %v", c.name, got, c.coversAll)
		}
	}
}

func TestAggregateHealthSummariesSampleWeighted(t *testing.T) {
	parts := []*periscopepb.StreamHealthSummary{
		{AvgBitrate: 1000, AvgBufferHealth: 2, AvgFps: 30, SampleCount: 1, TotalRebufferCount: 1, TotalIssueCount: 2, HasActiveIssues: true, CurrentQualityTier: "degraded"},
		{AvgBitrate: 2000, AvgBufferHealth: 4, AvgFps: 0, SampleCount: 3, TotalRebufferCount: 5, TotalIssueCount: 0, CurrentQualityTier: "good"},
		{AvgBitrate: 9999, SampleCount: 0, CurrentQualityTier: "excellent"}, // no samples: skipped from means + tier
	}
	out := aggregateHealthSummaries(parts)
	// Quality tier comes from the dominant (most-sampled) stream, ignoring the
	// zero-sample stream.
	if out.GetCurrentQualityTier() != "good" {
		t.Errorf("CurrentQualityTier=%q want \"good\" (dominant stream)", out.GetCurrentQualityTier())
	}
	// Sample-weighted bitrate: (1000*1 + 2000*3) / 4 = 1750.
	if out.GetAvgBitrate() != 1750 {
		t.Errorf("AvgBitrate=%v want 1750", out.GetAvgBitrate())
	}
	// Buffer: (2*1 + 4*3)/4 = 3.5.
	if out.GetAvgBufferHealth() != 3.5 {
		t.Errorf("AvgBufferHealth=%v want 3.5", out.GetAvgBufferHealth())
	}
	// FPS excludes the AvgFps<=0 stream: only the 30fps/1-sample stream counts.
	if out.GetAvgFps() != 30 {
		t.Errorf("AvgFps=%v want 30 (zero-fps stream excluded)", out.GetAvgFps())
	}
	if out.GetTotalRebufferCount() != 6 || out.GetTotalIssueCount() != 2 {
		t.Errorf("totals rebuffer=%d issue=%d want 6/2", out.GetTotalRebufferCount(), out.GetTotalIssueCount())
	}
	if out.GetSampleCount() != 4 {
		t.Errorf("SampleCount=%d want 4", out.GetSampleCount())
	}
	if !out.GetHasActiveIssues() {
		t.Error("HasActiveIssues should be OR of parts")
	}
}

func TestAggregateQoeSummariesSessionWeighted(t *testing.T) {
	pl1, pl2 := 0.1, 0.3
	peak1, peak2 := 0.5, 0.9
	parts := []*periscopepb.ClientQoeSummary{
		{AvgPacketLossRate: &pl1, PeakPacketLossRate: &peak1, TotalActiveSessions: 1},
		{AvgPacketLossRate: &pl2, PeakPacketLossRate: &peak2, TotalActiveSessions: 3},
	}
	out := aggregateQoeSummaries(parts)
	// Session-weighted: (0.1*1 + 0.3*3)/4 = 0.25.
	if got := out.GetAvgPacketLossRate(); got < 0.2499 || got > 0.2501 {
		t.Errorf("AvgPacketLossRate=%v want ~0.25", got)
	}
	if out.GetPeakPacketLossRate() != 0.9 {
		t.Errorf("PeakPacketLossRate=%v want 0.9 (max)", out.GetPeakPacketLossRate())
	}
	if out.GetTotalActiveSessions() != 4 {
		t.Errorf("TotalActiveSessions=%d want 4", out.GetTotalActiveSessions())
	}
}

// fakeCommodoreClient serves per-tenant stream monitoring rows.
type fakeCommodoreClient struct {
	resp *commodorepb.ListStreamMonitoringResponse
	err  error
}

func (f *fakeCommodoreClient) ListStreamMonitoring(_ context.Context, _ string) (*commodorepb.ListStreamMonitoringResponse, error) {
	return f.resp, f.err
}

// monRow sets a distinct stream_id (UUID) and internal_name so tests prove the
// resolver keys the monitored set on the public stream UUID, not internal_name.
func monRow(streamID string, tog commodorepb.MonitoringToggle) *commodorepb.StreamMonitoringRow {
	return &commodorepb.StreamMonitoringRow{StreamId: streamID, InternalName: streamID + "-internal", MonitoringToggle: tog}
}

func TestResolveTenantMonitoringScopedSet(t *testing.T) {
	// Free-tier tenant (unentitled): only the explicit ON stream is monitored.
	agent := NewAgent(AgentConfig{
		Logger:            testLogger(),
		Purser:            &fakeBillingClient{tierLevel: 1},
		RequiredTierLevel: 3,
		Commodore: &fakeCommodoreClient{resp: &commodorepb.ListStreamMonitoringResponse{
			Streams: []*commodorepb.StreamMonitoringRow{
				monRow("demo", commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON),
				monRow("other", commodorepb.MonitoringToggle_MONITORING_TOGGLE_INHERIT),
				monRow("muted", commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF),
			},
		}},
	})
	tm := agent.resolveTenantMonitoring(context.Background(), "tenant-free", true)
	if tm.TierEntitled {
		t.Fatal("free tier should not be entitled")
	}
	if len(tm.Monitored) != 1 || tm.Monitored[0] != "demo" {
		t.Fatalf("Monitored=%v want [demo]", tm.Monitored)
	}
	if tm.AllStreamCount != 3 {
		t.Fatalf("AllStreamCount=%d want 3", tm.AllStreamCount)
	}
	if tm.coversAll() {
		t.Fatal("should not cover all (only 1 of 3 monitored)")
	}
	if !tm.eligible() {
		t.Fatal("should be eligible (demo is ON)")
	}
}

func TestResolveTenantMonitoringSkipsOnCommodoreError(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Logger:    testLogger(),
		Commodore: &fakeCommodoreClient{err: context.DeadlineExceeded},
	})
	tm := agent.resolveTenantMonitoring(context.Background(), "tenant-a", true)
	if tm.eligible() || tm.coversAll() {
		t.Fatal("Commodore error should skip monitoring rather than guessing stream opt-out state")
	}
}

func TestResolveTenantMonitoringNilCommodoreSkips(t *testing.T) {
	agent := NewAgent(AgentConfig{Logger: testLogger()})
	tm := agent.resolveTenantMonitoring(context.Background(), "tenant-a", true)
	if tm.eligible() {
		t.Fatal("nil Commodore should skip monitoring")
	}
}

func TestResolveTenantMonitoringTenantWideOff(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Logger:    testLogger(),
		Commodore: &fakeCommodoreClient{resp: &commodorepb.ListStreamMonitoringResponse{}},
	})
	tm := agent.resolveTenantMonitoring(context.Background(), "tenant-a", false)
	if tm.eligible() {
		t.Fatal("tenant-wide off must not be eligible")
	}
}

func TestLoadSnapshotFastPathSingleCall(t *testing.T) {
	fp := &fakePeriscopeClient{
		healthResp:   &periscopepb.GetStreamHealthSummaryResponse{Summary: &periscopepb.StreamHealthSummary{AvgBitrate: 5000}},
		qoeResp:      &periscopepb.GetClientQoeSummaryResponse{Summary: &periscopepb.ClientQoeSummary{}},
		overviewResp: &periscopepb.GetPlatformOverviewResponse{ActiveStreams: 4},
	}
	agent := NewAgent(AgentConfig{Logger: testLogger(), Periscope: fp})
	tm := tenantMonitoring{TenantID: "t", TenantWideEnabled: true, Monitored: []string{"a"}, AllStreamCount: 1}
	snap, err := agent.loadSnapshot(context.Background(), tm)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if len(fp.summaryStreamIDArgs) != 1 || fp.summaryStreamIDArgs[0] != "" {
		t.Fatalf("fast path should issue exactly one nil-streamID summary call, got %v", fp.summaryStreamIDArgs)
	}
	if snap.ActiveStreams != 4 {
		t.Fatalf("ActiveStreams=%d want 4 (overview count)", snap.ActiveStreams)
	}
}

func TestLoadSnapshotScopedPathAggregatesMonitored(t *testing.T) {
	// Monitored holds public stream UUIDs; the scoped path keys Periscope reads
	// on those (stream_health_5m.stream_id), and aggregates only live streams.
	fp := &fakePeriscopeClient{
		healthByStream: map[string]*periscopepb.StreamHealthSummary{
			"uuid-demo":  {AvgBitrate: 1000, SampleCount: 2},
			"uuid-other": {AvgBitrate: 3000, SampleCount: 2},
			"uuid-dead":  {SampleCount: 0}, // not live: skipped
		},
		qoeByStream: map[string]*periscopepb.ClientQoeSummary{},
	}
	agent := NewAgent(AgentConfig{Logger: testLogger(), Periscope: fp})
	tm := tenantMonitoring{
		TenantID:          "t",
		TenantWideEnabled: true,
		Monitored:         []string{"uuid-demo", "uuid-other", "uuid-dead"},
		AllStreamCount:    5, // not coversAll -> scoped path
	}
	snap, err := agent.loadSnapshot(context.Background(), tm)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	// One summary call per monitored stream (no nil-streamID call).
	for _, arg := range fp.summaryStreamIDArgs {
		if arg == "" {
			t.Fatalf("scoped path must not issue a nil-streamID call: %v", fp.summaryStreamIDArgs)
		}
	}
	if len(fp.summaryStreamIDArgs) != 3 {
		t.Fatalf("expected 3 per-stream summary calls, got %v", fp.summaryStreamIDArgs)
	}
	// Only the 2 live streams contribute: (1000*2 + 3000*2)/4 = 2000.
	if snap.Health.GetAvgBitrate() != 2000 {
		t.Fatalf("AvgBitrate=%v want 2000", snap.Health.GetAvgBitrate())
	}
	if snap.ActiveStreams != 2 {
		t.Fatalf("ActiveStreams=%d want 2 (live monitored)", snap.ActiveStreams)
	}
}

func TestLoadSnapshotScopedPathNoLiveReturnsNil(t *testing.T) {
	fp := &fakePeriscopeClient{
		healthByStream: map[string]*periscopepb.StreamHealthSummary{
			"a": {SampleCount: 0},
		},
	}
	agent := NewAgent(AgentConfig{Logger: testLogger(), Periscope: fp})
	tm := tenantMonitoring{TenantID: "t", TenantWideEnabled: true, Monitored: []string{"a"}, AllStreamCount: 2}
	snap, err := agent.loadSnapshot(context.Background(), tm)
	if err != nil {
		t.Fatalf("loadSnapshot: %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot when no monitored stream is live, got %+v", snap)
	}
}

type fakeQuartermasterMonitoringClient struct {
	rows []*quartermasterpb.ActiveTenant
	err  error
}

func (f fakeQuartermasterMonitoringClient) ListActiveTenants(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	ids := make([]string, 0, len(f.rows))
	for _, r := range f.rows {
		ids = append(ids, r.GetTenantId())
	}
	return ids, nil
}

func (f fakeQuartermasterMonitoringClient) ListActiveTenantsWithMonitoring(context.Context) ([]*quartermasterpb.ActiveTenant, error) {
	return f.rows, f.err
}

func (f fakeQuartermasterMonitoringClient) BootstrapService(context.Context, *quartermasterpb.BootstrapServiceRequest) (*quartermasterpb.BootstrapServiceResponse, error) {
	return &quartermasterpb.BootstrapServiceResponse{}, nil
}

func TestResolveTenantAbsentFromActiveListNotEligible(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Logger:        testLogger(),
		Quartermaster: fakeQuartermasterMonitoringClient{rows: []*quartermasterpb.ActiveTenant{{TenantId: "other", MonitoringEnabled: true}}},
		Commodore: &fakeCommodoreClient{resp: &commodorepb.ListStreamMonitoringResponse{
			Streams: []*commodorepb.StreamMonitoringRow{monRow("demo", commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON)},
		}},
	})
	tm := agent.resolveTenant(context.Background(), "tenant-missing")
	if tm.TenantWideEnabled || tm.eligible() {
		t.Fatalf("missing active tenant must not be eligible: %+v", tm)
	}
}

func TestResolveTenantQuartermasterErrorNotEligible(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Logger:        testLogger(),
		Quartermaster: fakeQuartermasterMonitoringClient{err: context.DeadlineExceeded},
		Commodore: &fakeCommodoreClient{resp: &commodorepb.ListStreamMonitoringResponse{
			Streams: []*commodorepb.StreamMonitoringRow{monRow("demo", commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON)},
		}},
	})
	tm := agent.resolveTenant(context.Background(), "tenant-a")
	if tm.TenantWideEnabled || tm.eligible() {
		t.Fatalf("Quartermaster lookup error must not guess tenant-wide policy: %+v", tm)
	}
}
