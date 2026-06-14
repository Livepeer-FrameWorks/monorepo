package heartbeat

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/api_consultant/internal/chat"
	"frameworks/api_consultant/internal/diagnostics"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

type fakeOrchestrator struct {
	result chat.OrchestratorResult
	calls  int
}

func (o *fakeOrchestrator) Run(_ context.Context, _ []llm.Message, _ chat.TokenStreamer) (chat.OrchestratorResult, error) {
	o.calls++
	return o.result, nil
}

type recordingStore struct {
	last ReportRecord
}

func (s *recordingStore) Save(_ context.Context, record ReportRecord) (ReportRecord, error) {
	s.last = record
	return record, nil
}

func (s *recordingStore) ListByTenant(_ context.Context, _ string, _ int) ([]ReportRecord, error) {
	return nil, nil
}

func (s *recordingStore) ListByTenantPaginated(_ context.Context, _ string, _, _ int) ([]ReportRecord, int, error) {
	return nil, 0, nil
}

func (s *recordingStore) GetByID(_ context.Context, _, _ string) (ReportRecord, error) {
	return ReportRecord{}, nil
}

func (s *recordingStore) MarkRead(_ context.Context, _ string, _ []string) (int, error) {
	return 0, nil
}

func (s *recordingStore) UnreadCount(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func testLogger() logging.Logger {
	l := logging.NewLoggerWithService("test")
	l.SetOutput(io.Discard)
	return l
}

func TestThresholdCheckReturnViolations(t *testing.T) {
	agent := NewAgent(AgentConfig{Logger: testLogger()})
	trigger := NewThresholdTrigger(agent)

	snapshot := &healthSnapshot{
		TenantID:      "tenant-a",
		ActiveStreams: 1,
		Health: &periscopepb.StreamHealthSummary{
			AvgBufferHealth: 1.0,
			AvgFps:          20,
			AvgBitrate:      400000,
			TotalIssueCount: 2,
		},
	}

	violations := trigger.Check(snapshot)
	if len(violations) == 0 {
		t.Fatal("expected threshold violations for degraded metrics")
	}

	result := diagnostics.Triage(violations, nil, nil)
	if result.Action != diagnostics.TriageInvestigate {
		t.Fatalf("expected investigate, got %s", result.Action)
	}
}

func TestSnapshotToMetricMapSkipsUnknownFPS(t *testing.T) {
	metrics := snapshotToMetricMap(&healthSnapshot{
		Health: &periscopepb.StreamHealthSummary{
			AvgFps:          0,
			AvgBitrate:      5000000,
			AvgBufferHealth: 3,
		},
	})
	if _, ok := metrics["avg_fps"]; ok {
		t.Fatal("expected unknown FPS to be omitted")
	}

	metrics = snapshotToMetricMap(&healthSnapshot{
		Health: &periscopepb.StreamHealthSummary{AvgFps: 29.97},
	})
	if got := metrics["avg_fps"]; got != 29.97 {
		t.Fatalf("avg_fps = %v, want 29.97", got)
	}
}

func TestInvestigationPromptTreatsZeroFPSAsUnknown(t *testing.T) {
	prompt := buildInvestigationPrompt(&healthSnapshot{
		TenantID:      "tenant-a",
		ActiveStreams: 1,
		Window:        15,
		Health: &periscopepb.StreamHealthSummary{
			AvgFps:          0,
			AvgBitrate:      5000000,
			AvgBufferHealth: 1,
			TotalIssueCount: 1,
		},
	}, "threshold", "buffer health degraded", nil, nil)

	for _, want := range []string{
		"Mist reports FPS as 0 when frame rate is unknown or dynamic",
		"Local AV compatibility processing generates Opus",
		"Thumbnail processing can add JPEG preview/sprite tracks",
		"Avg FPS: unknown",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Avg FPS: 0.00") {
		t.Fatalf("prompt should not present unknown FPS as zero:\n%s", prompt)
	}
	if !strings.Contains(investigationSystemPrompt, "Treat avg_fps/fps <= 0 as unknown") {
		t.Fatal("system prompt is missing Mist FPS semantics")
	}
	if !strings.Contains(investigationSystemPrompt, "derived JPEG, thumbvtt, AAC, or Opus tracks") {
		t.Fatal("system prompt is missing processing/track semantics")
	}
}

func TestProcessTenantHealthySkipsLLM(t *testing.T) {
	orchestrator := &fakeOrchestrator{
		result: chat.OrchestratorResult{Content: "should not be called"},
	}

	fp := &fakePeriscopeClient{
		healthResp: &periscopepb.GetStreamHealthSummaryResponse{
			Summary: &periscopepb.StreamHealthSummary{
				AvgBufferHealth: 3.0,
				AvgFps:          30,
				AvgBitrate:      5000000,
				TotalIssueCount: 0,
			},
		},
		qoeResp: &periscopepb.GetClientQoeSummaryResponse{
			Summary: &periscopepb.ClientQoeSummary{},
		},
		overviewResp: &periscopepb.GetPlatformOverviewResponse{ActiveStreams: 5},
	}

	agent := NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Periscope:    fp,
		Logger:       testLogger(),
	})

	tm := tenantMonitoring{TenantID: "tenant-healthy", TenantWideEnabled: true, TierEntitled: true, Monitored: []string{"stream"}, AllStreamCount: 1}
	if err := agent.processTenant(context.Background(), tm); err != nil {
		t.Fatalf("processTenant: %v", err)
	}
	if orchestrator.calls != 0 {
		t.Fatalf("expected 0 LLM calls for healthy tenant, got %d", orchestrator.calls)
	}
}

func TestProcessTenantDegradedInvestigates(t *testing.T) {
	orchestrator := &fakeOrchestrator{
		result: chat.OrchestratorResult{
			Content: `{"summary":"Investigated","metrics_reviewed":["avg_buffer"],"root_cause":"network","recommendations":[{"text":"reduce bitrate","confidence":"high"}]}`,
		},
	}
	store := &recordingStore{}
	reporter := &Reporter{Store: store}

	fp := &fakePeriscopeClient{
		healthResp: &periscopepb.GetStreamHealthSummaryResponse{
			Summary: &periscopepb.StreamHealthSummary{
				AvgBufferHealth: 1.0,
				AvgFps:          20,
				AvgBitrate:      400000,
				TotalIssueCount: 2,
			},
		},
		overviewResp: &periscopepb.GetPlatformOverviewResponse{ActiveStreams: 3},
	}

	agent := NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Periscope:    fp,
		Reporter:     reporter,
		Logger:       testLogger(),
	})

	tm := tenantMonitoring{TenantID: "tenant-degraded", TenantWideEnabled: true, TierEntitled: true, Monitored: []string{"stream"}, AllStreamCount: 1}
	if err := agent.processTenant(context.Background(), tm); err != nil {
		t.Fatalf("processTenant: %v", err)
	}
	if orchestrator.calls != 1 {
		t.Fatalf("expected 1 LLM call for degraded tenant, got %d", orchestrator.calls)
	}
	if store.last.TenantID != "tenant-degraded" {
		t.Fatalf("expected report for tenant-degraded, got %s", store.last.TenantID)
	}
}

func TestTierEntitledFailsOpenOnBillingError(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Purser:            &fakeBillingClient{err: errors.New("billing unavailable")},
		Logger:            testLogger(),
		RequiredTierLevel: 3,
	})

	if !agent.tierEntitled(context.Background(), "tenant-billing-error") {
		t.Fatal("expected billing lookup errors to keep tenant entitled")
	}
}

func TestTierEntitledHonorsKnownLowTier(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Purser:            &fakeBillingClient{tierLevel: 1},
		Logger:            testLogger(),
		RequiredTierLevel: 3,
	})

	if agent.tierEntitled(context.Background(), "tenant-low-tier") {
		t.Fatal("expected known low tier to be unentitled")
	}
}

func TestReporterBuildNotificationUsesPrimaryUserWhenBillingEmailEmpty(t *testing.T) {
	reporter := &Reporter{
		Billing:  &fakeBillingClient{},
		Contacts: &fakeTenantContactClient{email: "owner-account@test.com", name: "Owner Account"},
	}
	notification := reporter.buildNotification(context.Background(), ReportRecord{TenantID: "tenant-a"}, Report{
		Summary: "test",
	})
	if notification.RecipientEmail != "owner-account@test.com" {
		t.Fatalf("RecipientEmail = %q, want primary user email", notification.RecipientEmail)
	}
	if notification.TenantName != "Owner Account" {
		t.Fatalf("TenantName = %q, want primary user name", notification.TenantName)
	}
}

func TestReporterBuildNotificationUsesDefaultRecipientLast(t *testing.T) {
	reporter := &Reporter{
		Billing:          &fakeBillingClient{},
		Contacts:         &fakeTenantContactClient{},
		DefaultRecipient: "ops@test.com",
	}
	notification := reporter.buildNotification(context.Background(), ReportRecord{TenantID: "tenant-a"}, Report{
		Summary: "test",
	})
	if notification.RecipientEmail != "ops@test.com" {
		t.Fatalf("RecipientEmail = %q, want default recipient", notification.RecipientEmail)
	}
}

// --- Test helpers ---

type fakePeriscopeClient struct {
	healthResp    *periscopepb.GetStreamHealthSummaryResponse
	qoeResp       *periscopepb.GetClientQoeSummaryResponse
	overviewResp  *periscopepb.GetPlatformOverviewResponse
	streamMetrics *periscopepb.GetStreamHealthMetricsResponse
	// Per-stream summaries keyed by stream_id (public UUID), for scoped-path tests.
	healthByStream map[string]*periscopepb.StreamHealthSummary
	qoeByStream    map[string]*periscopepb.ClientQoeSummary
	// summaryStreamIDArgs records the streamID arg of each
	// GetStreamHealthSummary call ("" for nil) so tests can assert the
	// fast-path (one nil call) vs scoped-path (one call per stream).
	summaryStreamIDArgs []string
}

func (f *fakePeriscopeClient) GetStreamHealthSummary(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
	arg := ""
	if streamID != nil {
		arg = *streamID
	}
	f.summaryStreamIDArgs = append(f.summaryStreamIDArgs, arg)
	if streamID != nil && f.healthByStream != nil {
		return &periscopepb.GetStreamHealthSummaryResponse{Summary: f.healthByStream[*streamID]}, nil
	}
	return f.healthResp, nil
}

func (f *fakePeriscopeClient) GetClientQoeSummary(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
	if streamID != nil && f.qoeByStream != nil {
		return &periscopepb.GetClientQoeSummaryResponse{Summary: f.qoeByStream[*streamID]}, nil
	}
	return f.qoeResp, nil
}

func (f *fakePeriscopeClient) GetPlatformOverview(_ context.Context, _ string, _ *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error) {
	return f.overviewResp, nil
}

func (f *fakePeriscopeClient) GetStreamHealthMetrics(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
	if f.streamMetrics != nil {
		return f.streamMetrics, nil
	}
	return &periscopepb.GetStreamHealthMetricsResponse{}, nil
}
