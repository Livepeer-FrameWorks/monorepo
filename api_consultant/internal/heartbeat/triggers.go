package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_consultant/internal/diagnostics"

	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
)

const defaultLookoutTopic = "lookout.incidents"

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

type LookoutTrigger struct {
	Consumer *kafka.Consumer
	Agent    *Agent
	Logger   logging.Logger
	Topic    string
}

type lookoutIncident struct {
	TenantID string `json:"tenant_id"`
	Summary  string `json:"summary"`
	Severity string `json:"severity"`
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
		topic = defaultLookoutTopic
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
	if incident.TenantID == "" {
		return nil
	}
	if !t.Agent.isSkipperEnabled(ctx, incident.TenantID) {
		return nil
	}
	snapshot, err := t.Agent.loadSnapshot(ctx, incident.TenantID)
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
	report, tokens, err := t.Agent.Investigate(ctx, incident.TenantID, "lookout", reason, snapshot, nil, nil)
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
		t.Logger.WithField("tenant_id", incident.TenantID).WithField("report", report.FormatMarkdown()).Info("LOOKOUT_INVESTIGATION")
	}
	time.Sleep(10 * time.Millisecond)
	return nil
}
