package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_consultant/internal/diagnostics"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ThresholdTrigger struct {
	agent              *Agent
	logger             logging.Logger
	warningBuffer      float64
	warningFPS         float64
	warningBitrate     float64
	warningIssueCount  int64
	warningPacketLoss  float64
	considerActiveOnly bool
}

// LookoutConsumer is the part of kafka.Consumer the Lookout trigger drives.
type LookoutConsumer interface {
	AddHandler(topic string, handler kafka.Handler)
	Start(ctx context.Context) error
}

// IncidentAttacher links a persisted investigation report to its Lookout
// incident. pkg/clients/lookout.GRPCClient satisfies it.
type IncidentAttacher interface {
	AttachInvestigation(ctx context.Context, incidentID, tenantID, reportID string) (*lookoutpb.IncidentMutationResponse, error)
}

type LookoutTrigger struct {
	Consumer LookoutConsumer
	Agent    *Agent
	Lookout  IncidentAttacher
	Logger   logging.Logger
	// Topic defaults to topology.TopicLookoutIncidents.
	Topic string
	// AttachBackoff is the wait before each AttachInvestigation retry; nil uses
	// defaultAttachBackoff.
	AttachBackoff []time.Duration
}

var defaultAttachBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

type lookoutIncident struct {
	IncidentID string `json:"incident_id"`
	TenantID   string `json:"tenant_id"`
	ClusterID  string `json:"cluster_id"`
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
}

func NewThresholdTrigger(agent *Agent) *ThresholdTrigger {
	return &ThresholdTrigger{
		agent:              agent,
		logger:             agent.logger,
		warningBuffer:      1.5,
		warningFPS:         24.0,
		warningBitrate:     800_000,
		warningIssueCount:  1,
		warningPacketLoss:  0.02,
		considerActiveOnly: true,
	}
}

// Check returns threshold violations without side effects.
// The caller (processTenant) decides what action to take.
func (t *ThresholdTrigger) Check(snapshot *healthSnapshot) []diagnostics.ThresholdViolation {
	if t == nil || snapshot == nil || snapshot.Health == nil {
		return nil
	}
	if t.considerActiveOnly && snapshot.ActiveStreams == 0 {
		return nil
	}
	health := snapshot.Health
	qoe := snapshot.ClientQoE
	var violations []diagnostics.ThresholdViolation
	if health.GetAvgBufferHealth() > 0 && health.GetAvgBufferHealth() < t.warningBuffer {
		violations = append(violations, diagnostics.ThresholdViolation{
			Metric:  "avg_buffer_health",
			Value:   health.GetAvgBufferHealth(),
			Limit:   t.warningBuffer,
			Message: fmt.Sprintf("buffer health %.2f < %.2f", health.GetAvgBufferHealth(), t.warningBuffer),
		})
	}
	if health.GetAvgFps() > 0 && health.GetAvgFps() < t.warningFPS {
		violations = append(violations, diagnostics.ThresholdViolation{
			Metric:  "avg_fps",
			Value:   health.GetAvgFps(),
			Limit:   t.warningFPS,
			Message: fmt.Sprintf("avg FPS %.2f < %.2f", health.GetAvgFps(), t.warningFPS),
		})
	}
	if health.GetAvgBitrate() > 0 && health.GetAvgBitrate() < t.warningBitrate {
		violations = append(violations, diagnostics.ThresholdViolation{
			Metric:  "avg_bitrate",
			Value:   health.GetAvgBitrate(),
			Limit:   t.warningBitrate,
			Message: fmt.Sprintf("avg bitrate %.2f < %.2f", health.GetAvgBitrate(), t.warningBitrate),
		})
	}
	if health.GetTotalIssueCount() >= t.warningIssueCount {
		violations = append(violations, diagnostics.ThresholdViolation{
			Metric:  "total_issue_count",
			Value:   float64(health.GetTotalIssueCount()),
			Limit:   float64(t.warningIssueCount),
			Message: fmt.Sprintf("issue count %d >= %d", health.GetTotalIssueCount(), t.warningIssueCount),
		})
	}
	if qoe != nil && qoe.GetAvgPacketLossRate() >= t.warningPacketLoss {
		violations = append(violations, diagnostics.ThresholdViolation{
			Metric:  "avg_packet_loss",
			Value:   qoe.GetAvgPacketLossRate(),
			Limit:   t.warningPacketLoss,
			Message: fmt.Sprintf("packet loss %.4f >= %.4f", qoe.GetAvgPacketLossRate(), t.warningPacketLoss),
		})
	}
	return violations
}

func (t *LookoutTrigger) Start(ctx context.Context) error {
	if t == nil || t.Consumer == nil {
		return fmt.Errorf("lookout consumer unavailable")
	}
	topic := t.Topic
	if topic == "" {
		topic = topology.TopicLookoutIncidents
	}
	t.Consumer.AddHandler(topic, t.handleIncident)
	return t.Consumer.Start(ctx)
}

func (t *LookoutTrigger) handleIncident(ctx context.Context, msg kafka.Message) error {
	defer func() {
		if r := recover(); r != nil {
			if t != nil && t.Logger != nil {
				t.Logger.WithField("panic", fmt.Sprint(r)).Error("Lookout incident handler panic")
			}
		}
	}()
	if t == nil || t.Agent == nil {
		return nil
	}
	var incident lookoutIncident
	if err := json.Unmarshal(msg.Value, &incident); err != nil {
		if t.Logger != nil {
			t.Logger.WithError(err).WithField("topic", msg.Topic).Warn("Failed to parse Lookout incident")
		}
		return nil
	}
	// Investigations are tenant-scoped: every tool call needs the tenant, and
	// the report is attached back to a specific incident.
	if incident.TenantID == "" || incident.IncidentID == "" {
		return nil
	}
	tm := t.Agent.resolveTenant(ctx, incident.TenantID)
	if !tm.eligible() {
		return nil
	}
	snapshot, err := t.Agent.loadSnapshot(ctx, tm)
	if err != nil {
		if t.Logger != nil {
			t.Logger.WithError(err).WithField("tenant_id", incident.TenantID).Warn("Lookout snapshot load failed")
		}
		// Do not block the consumer partition on transient upstream failures.
		return nil
	}
	reason := strings.TrimSpace(incident.Summary)
	if reason == "" {
		reason = fmt.Sprintf("Lookout incident severity=%s", incident.Severity)
	}
	report, reportID, tokens, err := t.Agent.Investigate(ctx, incident.TenantID, "lookout", reason, snapshot, nil, nil)
	if logErr := t.Agent.logUsage(ctx, incident.TenantID, tokens, err != nil); logErr != nil {
		if t.Logger != nil {
			t.Logger.WithError(logErr).WithField("tenant_id", incident.TenantID).Warn("Lookout usage logging failed")
		}
		return logErr
	}
	if err != nil {
		if t.Logger != nil {
			t.Logger.WithError(err).WithField("tenant_id", incident.TenantID).Warn("Lookout investigation failed")
		}
		return nil
	}
	if t.Logger != nil {
		t.Logger.WithField("tenant_id", incident.TenantID).WithField("incident_id", incident.IncidentID).WithField("report", report.FormatMarkdown()).Info("LOOKOUT_INVESTIGATION")
	}
	t.attachInvestigation(ctx, incident, reportID)
	return nil
}

// attachInvestigation links the report to the incident. Failures are retried
// inline and then dropped rather than returned: returning an error would
// redeliver the message and rerun the paid LLM investigation.
func (t *LookoutTrigger) attachInvestigation(ctx context.Context, incident lookoutIncident, reportID string) {
	log := t.Logger
	fields := logging.Fields{"tenant_id": incident.TenantID, "incident_id": incident.IncidentID}
	if reportID == "" {
		if log != nil {
			log.WithFields(fields).Warn("Lookout investigation produced no persisted report; skipping incident attach")
		}
		return
	}
	if t.Lookout == nil {
		if log != nil {
			log.WithFields(fields).Warn("Lookout client unavailable; skipping incident attach")
		}
		return
	}
	backoff := t.AttachBackoff
	if backoff == nil {
		backoff = defaultAttachBackoff
	}
	var err error
	for attempt := 0; ; attempt++ {
		_, err = t.Lookout.AttachInvestigation(ctx, incident.IncidentID, incident.TenantID, reportID)
		if err == nil {
			return
		}
		if !retryableAttachError(err) || attempt >= len(backoff) {
			break
		}
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-time.After(backoff[attempt]):
			continue
		}
		break
	}
	if log != nil {
		log.WithError(err).WithFields(fields).WithField("report_id", reportID).Warn("Failed to attach investigation to Lookout incident")
	}
}

func retryableAttachError(err error) bool {
	switch status.Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition:
		return false
	default:
		return true
	}
}
