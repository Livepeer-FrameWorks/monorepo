package heartbeat

import (
	"context"
	"testing"

	"frameworks/api_skipper/internal/chat"
	"frameworks/pkg/llm"
	pb "frameworks/pkg/proto"
)

type fakeOrchestrator struct {
	result chat.OrchestratorResult
	calls  int
}

func (o *fakeOrchestrator) Run(ctx context.Context, messages []llm.Message, streamer chat.TokenStreamer) (chat.OrchestratorResult, error) {
	_ = ctx
	_ = messages
	_ = streamer
	o.calls++
	return o.result, nil
}

type recordingStore struct {
	last ReportRecord
}

func (s *recordingStore) Save(ctx context.Context, record ReportRecord) (ReportRecord, error) {
	_ = ctx
	s.last = record
	return record, nil
}

func (s *recordingStore) ListByTenant(ctx context.Context, tenantID string, limit int) ([]ReportRecord, error) {
	_ = ctx
	_ = tenantID
	_ = limit
	return nil, nil
}

func TestThresholdTriggerInvestigatesDegradedMetrics(t *testing.T) {
	orchestrator := &fakeOrchestrator{
		result: chat.OrchestratorResult{
			Content: `{"summary":"Investigated","metrics_reviewed":["avg_buffer"],"root_cause":"network","recommendations":[{"text":"reduce bitrate","confidence":"high"}]}`,
		},
	}
	store := &recordingStore{}
	reporter := &Reporter{Store: store}

	agent := NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Reporter:     reporter,
	})

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

	trigger := NewThresholdTrigger(agent)
	triggered := trigger.Evaluate(context.Background(), snapshot)
	if !triggered {
		t.Fatalf("expected threshold trigger to investigate")
	}
	if store.last.TenantID != "tenant-a" {
		t.Fatalf("expected report stored for tenant-a, got %s", store.last.TenantID)
	}
}
