package heartbeat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

func (t *ThresholdTrigger) Evaluate(ctx context.Context, snapshot *healthSnapshot) bool {
	if t == nil || snapshot == nil || snapshot.Health == nil || t.agent == nil {
		return false
	}
	if t.considerActiveOnly && snapshot.ActiveStreams == 0 {
		return false
	}
	health := snapshot.Health
	qoe := snapshot.ClientQoE
	yellowReasons := make([]string, 0)
	if health.GetAvgBufferHealth() > 0 && health.GetAvgBufferHealth() < t.warningBuffer {
		yellowReasons = append(yellowReasons, fmt.Sprintf("buffer health %.2f", health.GetAvgBufferHealth()))
	}
	if health.GetAvgFps() > 0 && health.GetAvgFps() < t.warningFPS {
		yellowReasons = append(yellowReasons, fmt.Sprintf("avg FPS %.2f", health.GetAvgFps()))
	}
	if health.GetAvgBitrate() > 0 && health.GetAvgBitrate() < t.warningBitrate {
		yellowReasons = append(yellowReasons, fmt.Sprintf("avg bitrate %.2f", health.GetAvgBitrate()))
	}
	if health.GetTotalIssueCount() >= t.warningIssueCount {
		yellowReasons = append(yellowReasons, fmt.Sprintf("issue count %d", health.GetTotalIssueCount()))
	}
	if qoe != nil && qoe.GetAvgPacketLossRate() >= t.warningPacketLoss {
		yellowReasons = append(yellowReasons, fmt.Sprintf("packet loss %.2f", qoe.GetAvgPacketLossRate()))
	}
	if len(yellowReasons) == 0 {
		return false
	}
	reason := fmt.Sprintf("threshold warning: %s", strings.Join(yellowReasons, ", "))
	report, tokens, err := t.agent.Investigate(ctx, snapshot.TenantID, "threshold", reason, snapshot)
	t.agent.logUsage(ctx, snapshot.TenantID, tokens, err != nil)
	if err != nil {
		if t.logger != nil {
			t.logger.WithError(err).WithField("tenant_id", snapshot.TenantID).Warn("Threshold investigation failed")
		}
		return false
	}
	if t.logger != nil {
		t.logger.WithField("tenant_id", snapshot.TenantID).WithField("report", report.FormatMarkdown()).Info("HEARTBEAT_THRESHOLD")
	}
	return true
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
	report, tokens, err := t.Agent.Investigate(ctx, incident.TenantID, "lookout", reason, snapshot)
	t.Agent.logUsage(ctx, incident.TenantID, tokens, err != nil)
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
