package heartbeat

import (
	"context"
	"errors"
	"io"
	"testing"

	"frameworks/api_consultant/internal/chat"
	"frameworks/api_consultant/internal/diagnostics"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"
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
		Health: &pb.StreamHealthSummary{
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

func TestProcessTenantHealthySkipsLLM(t *testing.T) {
	orchestrator := &fakeOrchestrator{
		result: chat.OrchestratorResult{Content: "should not be called"},
	}

	fp := &fakePeriscopeClient{
		healthResp: &pb.GetStreamHealthSummaryResponse{
			Summary: &pb.StreamHealthSummary{
				AvgBufferHealth: 3.0,
				AvgFps:          30,
				AvgBitrate:      5000000,
				TotalIssueCount: 0,
			},
		},
		qoeResp: &pb.GetClientQoeSummaryResponse{
			Summary: &pb.ClientQoeSummary{},
		},
		overviewResp: &pb.GetPlatformOverviewResponse{ActiveStreams: 5},
	}

	agent := NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Periscope:    fp,
		Logger:       testLogger(),
	})

	if err := agent.processTenant(context.Background(), "tenant-healthy"); err != nil {
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
		healthResp: &pb.GetStreamHealthSummaryResponse{
			Summary: &pb.StreamHealthSummary{
				AvgBufferHealth: 1.0,
				AvgFps:          20,
				AvgBitrate:      400000,
				TotalIssueCount: 2,
			},
		},
		overviewResp: &pb.GetPlatformOverviewResponse{ActiveStreams: 3},
	}

	agent := NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Periscope:    fp,
		Reporter:     reporter,
		Logger:       testLogger(),
	})

	if err := agent.processTenant(context.Background(), "tenant-degraded"); err != nil {
		t.Fatalf("processTenant: %v", err)
	}
	if orchestrator.calls != 1 {
		t.Fatalf("expected 1 LLM call for degraded tenant, got %d", orchestrator.calls)
	}
	if store.last.TenantID != "tenant-degraded" {
		t.Fatalf("expected report for tenant-degraded, got %s", store.last.TenantID)
	}
}

func TestIsSkipperEnabledAllowsHeartbeatOnBillingError(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Purser:            &fakeBillingClient{err: errors.New("billing unavailable")},
		Logger:            testLogger(),
		RequiredTierLevel: 3,
	})

	if !agent.isSkipperEnabled(context.Background(), "tenant-billing-error") {
		t.Fatal("expected billing lookup errors to fail open for heartbeat")
	}
}

func TestIsSkipperEnabledStillHonorsKnownLowTier(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Purser:            &fakeBillingClient{tierLevel: 1},
		Logger:            testLogger(),
		RequiredTierLevel: 3,
	})

	if agent.isSkipperEnabled(context.Background(), "tenant-low-tier") {
		t.Fatal("expected known low tier to remain ineligible")
	}
}

func TestIsSkipperEnabledAllowsSystemTenant(t *testing.T) {
	agent := NewAgent(AgentConfig{
		Purser:            &fakeBillingClient{tierLevel: 1},
		Logger:            testLogger(),
		RequiredTierLevel: 3,
	})

	if !agent.isSkipperEnabled(context.Background(), tenants.SystemTenantID.String()) {
		t.Fatal("expected system tenant to bypass commercial tier gate")
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
	healthResp    *pb.GetStreamHealthSummaryResponse
	qoeResp       *pb.GetClientQoeSummaryResponse
	overviewResp  *pb.GetPlatformOverviewResponse
	streamMetrics *pb.GetStreamHealthMetricsResponse
}

func (f *fakePeriscopeClient) GetStreamHealthSummary(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts) (*pb.GetStreamHealthSummaryResponse, error) {
	return f.healthResp, nil
}

func (f *fakePeriscopeClient) GetClientQoeSummary(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts) (*pb.GetClientQoeSummaryResponse, error) {
	return f.qoeResp, nil
}

func (f *fakePeriscopeClient) GetPlatformOverview(_ context.Context, _ string, _ *periscope.TimeRangeOpts) (*pb.GetPlatformOverviewResponse, error) {
	return f.overviewResp, nil
}

func (f *fakePeriscopeClient) GetStreamHealthMetrics(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*pb.GetStreamHealthMetricsResponse, error) {
	if f.streamMetrics != nil {
		return f.streamMetrics, nil
	}
	return &pb.GetStreamHealthMetricsResponse{}, nil
}
