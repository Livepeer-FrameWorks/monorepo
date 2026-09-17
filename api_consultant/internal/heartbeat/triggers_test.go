package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"frameworks/api_consultant/internal/chat"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeLookoutConsumer struct {
	topic   string
	handler kafka.Handler
	started bool
}

func (c *fakeLookoutConsumer) AddHandler(topic string, handler kafka.Handler) {
	c.topic = topic
	c.handler = handler
}

func (c *fakeLookoutConsumer) Start(context.Context) error {
	c.started = true
	return nil
}

type attachCall struct {
	incidentID, tenantID, reportID string
}

type fakeIncidentAttacher struct {
	calls []attachCall
	errs  []error
}

func (f *fakeIncidentAttacher) AttachInvestigation(_ context.Context, incidentID, tenantID, reportID string) (*lookoutpb.IncidentMutationResponse, error) {
	f.calls = append(f.calls, attachCall{incidentID, tenantID, reportID})
	if i := len(f.calls) - 1; i < len(f.errs) {
		return nil, f.errs[i]
	}
	return &lookoutpb.IncidentMutationResponse{}, nil
}

type fakeHeartbeatDecklog struct{}

func (fakeHeartbeatDecklog) SendServiceEvent(*ipcpb.ServiceEvent) error { return nil }
func (fakeHeartbeatDecklog) Close() error                               { return nil }

type idReportStore struct {
	recordingStore
	id string
}

func (s *idReportStore) Save(ctx context.Context, record ReportRecord) (ReportRecord, error) {
	record.ID = s.id
	return s.recordingStore.Save(ctx, record)
}

type failingOrchestrator struct{ calls int }

func (o *failingOrchestrator) Run(context.Context, []llm.Message, chat.TokenStreamer) (chat.OrchestratorResult, error) {
	o.calls++
	return chat.OrchestratorResult{}, errors.New("llm down")
}

const lookoutTestTenant = "tenant-lookout"

func newLookoutTestAgent(orchestrator Orchestrator, store ReportStore) *Agent {
	return NewAgent(AgentConfig{
		Orchestrator: orchestrator,
		Periscope: &fakePeriscopeClient{
			healthResp: &periscopepb.GetStreamHealthSummaryResponse{Summary: &periscopepb.StreamHealthSummary{
				AvgBufferHealth: 1.0, AvgFps: 20, AvgBitrate: 400000, TotalIssueCount: 2,
			}},
			overviewResp: &periscopepb.GetPlatformOverviewResponse{ActiveStreams: 1},
		},
		Quartermaster: fakeQuartermasterMonitoringClient{rows: []*quartermasterpb.ActiveTenant{{TenantId: lookoutTestTenant, MonitoringEnabled: true}}},
		Commodore: &fakeCommodoreClient{resp: &commodorepb.ListStreamMonitoringResponse{Streams: []*commodorepb.StreamMonitoringRow{
			monRow("11111111-1111-1111-1111-111111111111", commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON),
		}}},
		Decklog:  fakeHeartbeatDecklog{},
		Reporter: &Reporter{Store: store},
		Logger:   testLogger(),
	})
}

func lookoutMessage(t *testing.T, payload map[string]string) kafka.Message {
	t.Helper()
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return kafka.Message{Topic: topology.TopicLookoutIncidents, Value: value}
}

func investigatedOrchestrator() *fakeOrchestrator {
	return &fakeOrchestrator{result: chat.OrchestratorResult{
		Content: `{"summary":"Investigated","metrics_reviewed":["avg_buffer"],"root_cause":"network","recommendations":[]}`,
	}}
}

func TestLookoutTriggerStartRegistersIncidentTopic(t *testing.T) {
	consumer := &fakeLookoutConsumer{}
	trigger := &LookoutTrigger{Consumer: consumer, Agent: NewAgent(AgentConfig{Logger: testLogger()}), Logger: testLogger()}
	if err := trigger.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if consumer.topic != topology.TopicLookoutIncidents || consumer.handler == nil || !consumer.started {
		t.Fatalf("consumer topic=%q handler=%v started=%v", consumer.topic, consumer.handler != nil, consumer.started)
	}
}

func TestLookoutTriggerAttachesPersistedReportToIncident(t *testing.T) {
	orchestrator := investigatedOrchestrator()
	attacher := &fakeIncidentAttacher{}
	trigger := &LookoutTrigger{
		Agent:   newLookoutTestAgent(orchestrator, &idReportStore{id: "report-1"}),
		Lookout: attacher,
		Logger:  testLogger(),
	}
	msg := lookoutMessage(t, map[string]string{
		"incident_id": "incident-1", "tenant_id": lookoutTestTenant, "cluster_id": "cluster-a",
		"severity": "critical", "summary": "edge down",
	})
	if err := trigger.handleIncident(context.Background(), msg); err != nil {
		t.Fatalf("handleIncident: %v", err)
	}
	if orchestrator.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1", orchestrator.calls)
	}
	want := attachCall{incidentID: "incident-1", tenantID: lookoutTestTenant, reportID: "report-1"}
	if len(attacher.calls) != 1 || attacher.calls[0] != want {
		t.Fatalf("attach calls = %#v, want %#v", attacher.calls, want)
	}
}

func TestLookoutTriggerDropsTenantlessAndIncidentlessMessages(t *testing.T) {
	for name, payload := range map[string]map[string]string{
		"tenantless":   {"incident_id": "incident-1", "summary": "platform incident"},
		"incidentless": {"tenant_id": lookoutTestTenant, "summary": "no incident"},
	} {
		t.Run(name, func(t *testing.T) {
			orchestrator := investigatedOrchestrator()
			attacher := &fakeIncidentAttacher{}
			trigger := &LookoutTrigger{
				Agent:   newLookoutTestAgent(orchestrator, &idReportStore{id: "report-1"}),
				Lookout: attacher,
				Logger:  testLogger(),
			}
			if err := trigger.handleIncident(context.Background(), lookoutMessage(t, payload)); err != nil {
				t.Fatalf("handleIncident: %v", err)
			}
			if orchestrator.calls != 0 || len(attacher.calls) != 0 {
				t.Fatalf("LLM calls = %d, attach calls = %d, want none", orchestrator.calls, len(attacher.calls))
			}
		})
	}
}

func TestLookoutTriggerRetriesAttachThenGivesUpWithoutRedelivery(t *testing.T) {
	orchestrator := investigatedOrchestrator()
	unavailable := status.Error(codes.Unavailable, "lookout down")
	attacher := &fakeIncidentAttacher{errs: []error{unavailable, unavailable, unavailable}}
	trigger := &LookoutTrigger{
		Agent:         newLookoutTestAgent(orchestrator, &idReportStore{id: "report-1"}),
		Lookout:       attacher,
		Logger:        testLogger(),
		AttachBackoff: []time.Duration{time.Millisecond, time.Millisecond},
	}
	msg := lookoutMessage(t, map[string]string{"incident_id": "incident-1", "tenant_id": lookoutTestTenant, "summary": "edge down"})
	if err := trigger.handleIncident(context.Background(), msg); err != nil {
		t.Fatalf("handleIncident returned %v; a failed attach must not redeliver the investigation", err)
	}
	if len(attacher.calls) != 3 {
		t.Fatalf("attach attempts = %d, want 3 (initial + 2 retries)", len(attacher.calls))
	}
	if orchestrator.calls != 1 {
		t.Fatalf("LLM calls = %d, want 1", orchestrator.calls)
	}
}

func TestLookoutTriggerDoesNotRetryPermanentAttachErrors(t *testing.T) {
	attacher := &fakeIncidentAttacher{errs: []error{status.Error(codes.NotFound, "incident missing")}}
	trigger := &LookoutTrigger{
		Agent:         newLookoutTestAgent(investigatedOrchestrator(), &idReportStore{id: "report-1"}),
		Lookout:       attacher,
		Logger:        testLogger(),
		AttachBackoff: []time.Duration{time.Millisecond, time.Millisecond},
	}
	msg := lookoutMessage(t, map[string]string{"incident_id": "incident-1", "tenant_id": lookoutTestTenant})
	if err := trigger.handleIncident(context.Background(), msg); err != nil {
		t.Fatalf("handleIncident: %v", err)
	}
	if len(attacher.calls) != 1 {
		t.Fatalf("attach attempts = %d, want 1", len(attacher.calls))
	}
}

func TestLookoutTriggerDoesNotAttachWhenInvestigationFails(t *testing.T) {
	orchestrator := &failingOrchestrator{}
	attacher := &fakeIncidentAttacher{}
	trigger := &LookoutTrigger{
		Agent:   newLookoutTestAgent(orchestrator, &idReportStore{id: "report-1"}),
		Lookout: attacher,
		Logger:  testLogger(),
	}
	msg := lookoutMessage(t, map[string]string{"incident_id": "incident-1", "tenant_id": lookoutTestTenant, "summary": "edge down"})
	if err := trigger.handleIncident(context.Background(), msg); err != nil {
		t.Fatalf("handleIncident: %v", err)
	}
	if orchestrator.calls != 1 || len(attacher.calls) != 0 {
		t.Fatalf("LLM calls = %d, attach calls = %d, want 1 and 0", orchestrator.calls, len(attacher.calls))
	}
}

func TestLookoutTriggerSkipsAttachWithoutPersistedReport(t *testing.T) {
	attacher := &fakeIncidentAttacher{}
	trigger := &LookoutTrigger{
		Agent:   newLookoutTestAgent(investigatedOrchestrator(), &recordingStore{}),
		Lookout: attacher,
		Logger:  testLogger(),
	}
	msg := lookoutMessage(t, map[string]string{"incident_id": "incident-1", "tenant_id": lookoutTestTenant})
	if err := trigger.handleIncident(context.Background(), msg); err != nil {
		t.Fatalf("handleIncident: %v", err)
	}
	if len(attacher.calls) != 0 {
		t.Fatalf("attach calls = %d, want 0 without a report ID", len(attacher.calls))
	}
}
