package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_consultant/internal/chat"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultHeartbeatInterval = 30 * time.Minute
	defaultSummaryWindow     = 15 * time.Minute
)

const decisionSystemPrompt = `You are Skipper's heartbeat agent, monitoring live streams for early warning signs.
You will receive a snapshot of health and QoE metrics. Decide whether to investigate now.

Return ONLY valid JSON with:
{
  "action": "investigate" | "flag" | "skip",
  "reason": "short rationale",
  "metrics_reviewed": ["metric1", "metric2"]
}`

const investigationSystemPrompt = `You are Skipper's diagnostic agent.
Use available tools to investigate issues, then produce a report in JSON with:
{
  "summary": "short summary",
  "metrics_reviewed": ["metric1", "metric2"],
  "root_cause": "suspected root cause",
  "recommendations": [
    {"text": "recommendation", "confidence": "high|medium|low"}
  ]
}
Use tools when it helps confirm findings.`

type AgentConfig struct {
	Interval          time.Duration
	Orchestrator      Orchestrator
	Commodore         CommodoreClient
	Periscope         PeriscopeClient
	Purser            BillingClient
	Quartermaster     QuartermasterClient
	Decklog           DecklogClient
	Reporter          *Reporter
	Logger            logging.Logger
	RequiredTierLevel int
}

type Agent struct {
	interval          time.Duration
	orchestrator      Orchestrator
	commodore         CommodoreClient
	periscope         PeriscopeClient
	purser            BillingClient
	quartermaster     QuartermasterClient
	decklog           DecklogClient
	reporter          *Reporter
	logger            logging.Logger
	requiredTierLevel int
	thresholdTrigger  *ThresholdTrigger
}

type decisionPayload struct {
	Action          string   `json:"action"`
	Reason          string   `json:"reason"`
	MetricsReviewed []string `json:"metrics_reviewed"`
}

type healthSnapshot struct {
	TenantID      string
	ActiveStreams int
	Window        time.Duration
	Health        *pb.StreamHealthSummary
	ClientQoE     *pb.ClientQoeSummary
}

type Orchestrator interface {
	Run(ctx context.Context, messages []llm.Message, streamer chat.TokenStreamer) (chat.OrchestratorResult, error)
}

type CommodoreClient interface {
	ListStreams(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListStreamsResponse, error)
}

type PeriscopeClient interface {
	GetStreamHealthSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*pb.GetStreamHealthSummaryResponse, error)
	GetClientQoeSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*pb.GetClientQoeSummaryResponse, error)
}

type BillingClient interface {
	GetBillingStatus(ctx context.Context, tenantID string) (*pb.BillingStatusResponse, error)
}

type QuartermasterClient interface {
	ListActiveTenants(ctx context.Context) ([]string, error)
	BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error)
}

type DecklogClient interface {
	SendServiceEvent(event *pb.ServiceEvent) error
	Close() error
}

func NewAgent(cfg AgentConfig) *Agent {
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	agent := &Agent{
		interval:          interval,
		orchestrator:      cfg.Orchestrator,
		commodore:         cfg.Commodore,
		periscope:         cfg.Periscope,
		purser:            cfg.Purser,
		quartermaster:     cfg.Quartermaster,
		decklog:           cfg.Decklog,
		reporter:          cfg.Reporter,
		logger:            cfg.Logger,
		requiredTierLevel: cfg.RequiredTierLevel,
	}
	agent.thresholdTrigger = NewThresholdTrigger(agent)
	return agent
}

func (a *Agent) Start(ctx context.Context) {
	if a == nil {
		return
	}
	a.runCycle(ctx)
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runCycle(ctx)
		}
	}
}

func (a *Agent) runCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			if a.logger != nil {
				a.logger.WithField("panic", fmt.Sprint(r)).Error("Heartbeat cycle panic")
			}
		}
	}()
	if a.logger == nil {
		return
	}
	tenantIDs, err := a.fetchEligibleTenants(ctx)
	if err != nil {
		a.logger.WithError(err).Warn("Heartbeat tenant discovery failed")
		return
	}
	for _, tenantID := range tenantIDs {
		if tenantID == "" {
			continue
		}
		if err := a.processTenant(ctx, tenantID); err != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Heartbeat processing failed")
		}
	}
}

func (a *Agent) fetchEligibleTenants(ctx context.Context) ([]string, error) {
	if a.quartermaster == nil {
		return nil, errors.New("quartermaster client unavailable")
	}
	tenantIDs, err := a.quartermaster.ListActiveTenants(ctx)
	if err != nil {
		return nil, err
	}
	eligible := make([]string, 0, len(tenantIDs))
	for _, tenantID := range tenantIDs {
		if tenantID == "" {
			continue
		}
		if !a.isSkipperEnabled(ctx, tenantID) {
			continue
		}
		activeStreams, err := a.countActiveStreams(ctx, tenantID)
		if err != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Heartbeat stream scan failed")
			continue
		}
		if activeStreams == 0 {
			continue
		}
		eligible = append(eligible, tenantID)
	}
	return eligible, nil
}

func (a *Agent) isSkipperEnabled(ctx context.Context, tenantID string) bool {
	if a.purser == nil {
		a.logger.WithField("tenant_id", tenantID).Warn("Purser unavailable; skipping Skipper heartbeat")
		return false
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	status, err := a.purser.GetBillingStatus(ctx, tenantID)
	if err != nil {
		a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to fetch billing status")
		return false
	}
	tier := status.GetTier()
	if tier == nil {
		return false
	}
	return int(tier.TierLevel) >= a.requiredTierLevel
}

func (a *Agent) countActiveStreams(ctx context.Context, tenantID string) (int, error) {
	if a.commodore == nil {
		return 0, errors.New("commodore client unavailable")
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	var cursor *string
	count := 0
	for {
		resp, err := a.commodore.ListStreams(ctx, &pb.CursorPaginationRequest{
			First: 100,
			After: cursor,
		})
		if err != nil {
			return count, err
		}
		for _, stream := range resp.GetStreams() {
			if stream.GetIsLive() {
				count++
			}
		}
		pagination := resp.GetPagination()
		if pagination == nil || !pagination.GetHasNextPage() {
			break
		}
		endCursor := pagination.GetEndCursor()
		if endCursor == "" {
			break
		}
		cursor = &endCursor
	}
	return count, nil
}

func (a *Agent) processTenant(ctx context.Context, tenantID string) error {
	if a.orchestrator == nil || a.periscope == nil {
		return errors.New("heartbeat dependencies unavailable")
	}
	snapshot, err := a.loadSnapshot(ctx, tenantID)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.Health == nil {
		return errors.New("missing health snapshot")
	}

	if a.thresholdTrigger != nil {
		if triggered := a.thresholdTrigger.Evaluate(ctx, snapshot); triggered {
			return nil
		}
	}

	decision, result, err := a.evaluateDecision(ctx, snapshot)
	if err != nil {
		a.logUsage(ctx, tenantID, result.TokenCounts, true)
		return err
	}
	a.logUsage(ctx, tenantID, result.TokenCounts, false)

	action := strings.ToLower(strings.TrimSpace(decision.Action))
	switch action {
	case "investigate":
		report, tokens, err := a.Investigate(ctx, tenantID, "heartbeat", decision.Reason, snapshot)
		a.logUsage(ctx, tenantID, tokens, err != nil)
		if err != nil {
			return err
		}
		a.logger.WithField("tenant_id", tenantID).WithField("report", report.FormatMarkdown()).Info("HEARTBEAT_INVESTIGATION")
	case "flag":
		if a.reporter != nil {
			report := Report{
				Trigger:         "flag",
				Summary:         decision.Reason,
				MetricsReviewed: decision.MetricsReviewed,
				RootCause:       "pending review",
				Recommendations: []Recommendation{},
			}
			_ = a.reporter.Send(ctx, tenantID, report)
		}
		a.logger.WithField("tenant_id", tenantID).WithField("reason", decision.Reason).Info("HEARTBEAT_FLAG")
	default:
		a.logger.WithField("tenant_id", tenantID).Info("HEARTBEAT_OK")
	}
	return nil
}

func (a *Agent) loadSnapshot(ctx context.Context, tenantID string) (*healthSnapshot, error) {
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	now := time.Now()
	window := defaultSummaryWindow
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-window),
		EndTime:   now,
	}
	healthResp, err := a.periscope.GetStreamHealthSummary(ctx, tenantID, nil, timeRange)
	if err != nil {
		return nil, err
	}
	qoeResp, err := a.periscope.GetClientQoeSummary(ctx, tenantID, nil, timeRange)
	if err != nil {
		if a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Heartbeat client QoE lookup failed")
		}
	}
	var qoeSummary *pb.ClientQoeSummary
	if qoeResp != nil {
		qoeSummary = qoeResp.GetSummary()
	}
	activeStreams, err := a.countActiveStreams(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return &healthSnapshot{
		TenantID:      tenantID,
		ActiveStreams: activeStreams,
		Window:        window,
		Health:        healthResp.GetSummary(),
		ClientQoE:     qoeSummary,
	}, nil
}

func (a *Agent) evaluateDecision(ctx context.Context, snapshot *healthSnapshot) (decisionPayload, chat.OrchestratorResult, error) {
	prompt := buildDecisionPrompt(snapshot)
	messages := []llm.Message{
		{Role: "system", Content: decisionSystemPrompt},
		{Role: "user", Content: prompt},
	}
	result, err := a.orchestrator.Run(ctx, messages, nil)
	if err != nil {
		return decisionPayload{}, result, err
	}
	payload, err := parseDecisionPayload(result.Content)
	if err != nil {
		return decisionPayload{}, result, err
	}
	return payload, result, nil
}

func (a *Agent) Investigate(ctx context.Context, tenantID, trigger, reason string, snapshot *healthSnapshot) (Report, chat.TokenCounts, error) {
	if a.orchestrator == nil {
		return Report{}, chat.TokenCounts{}, errors.New("orchestrator unavailable")
	}
	prompt := buildInvestigationPrompt(snapshot, trigger, reason)
	messages := []llm.Message{
		{Role: "system", Content: investigationSystemPrompt},
		{Role: "user", Content: prompt},
	}
	result, err := a.orchestrator.Run(ctx, messages, nil)
	if err != nil {
		return Report{}, result.TokenCounts, err
	}
	report, err := parseReport(result.Content)
	if err != nil {
		report = Report{
			Trigger:         trigger,
			Summary:         result.Content,
			MetricsReviewed: []string{},
			RootCause:       "unknown",
			Recommendations: []Recommendation{},
		}
	}
	report.Trigger = trigger
	if a.reporter != nil {
		_ = a.reporter.Send(ctx, tenantID, report)
	}
	return report, result.TokenCounts, nil
}

func (a *Agent) logUsage(ctx context.Context, tenantID string, tokens chat.TokenCounts, hadError bool) {
	if a.decklog == nil || tenantID == "" {
		return
	}
	duration := uint64(time.Second.Milliseconds())
	totalTokens := uint32(tokens.Input + tokens.Output)
	agg := &pb.APIRequestAggregate{
		TenantId:        tenantID,
		AuthType:        "service",
		OperationType:   "skipper_heartbeat",
		OperationName:   "skipper_heartbeat",
		RequestCount:    1,
		ErrorCount:      boolToCount(hadError),
		TotalDurationMs: duration,
		TotalComplexity: totalTokens,
		Timestamp:       time.Now().Unix(),
	}
	batch := &pb.APIRequestBatch{
		Timestamp:  time.Now().Unix(),
		SourceNode: "skipper",
		Aggregates: []*pb.APIRequestAggregate{agg},
	}
	event := &pb.ServiceEvent{
		EventType: "api_request_batch",
		Timestamp: timestamppb.Now(),
		Source:    "skipper",
		TenantId:  tenantID,
		Payload:   &pb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
	}
	if err := a.decklog.SendServiceEvent(event); err != nil && a.logger != nil {
		a.logger.WithError(err).Warn("Failed to emit heartbeat usage event")
	}
}

func buildDecisionPrompt(snapshot *healthSnapshot) string {
	if snapshot == nil {
		return "No snapshot available."
	}
	health := snapshot.Health
	qoe := snapshot.ClientQoE
	metrics := []string{
		fmt.Sprintf("Active streams: %d", snapshot.ActiveStreams),
		fmt.Sprintf("Avg bitrate: %.2f", health.GetAvgBitrate()),
		fmt.Sprintf("Avg FPS: %.2f", health.GetAvgFps()),
		fmt.Sprintf("Avg buffer health: %.2f", health.GetAvgBufferHealth()),
		fmt.Sprintf("Total rebuffer count: %d", health.GetTotalRebufferCount()),
		fmt.Sprintf("Total issue count: %d", health.GetTotalIssueCount()),
		fmt.Sprintf("Has active issues: %t", health.GetHasActiveIssues()),
		fmt.Sprintf("Current quality tier: %s", health.GetCurrentQualityTier()),
	}
	if qoe != nil {
		metrics = append(metrics,
			fmt.Sprintf("Avg packet loss: %.2f", qoe.GetAvgPacketLossRate()),
			fmt.Sprintf("Peak packet loss: %.2f", qoe.GetPeakPacketLossRate()),
			fmt.Sprintf("Avg bandwidth in: %.2f", qoe.GetAvgBandwidthIn()),
			fmt.Sprintf("Avg bandwidth out: %.2f", qoe.GetAvgBandwidthOut()),
			fmt.Sprintf("Total active sessions: %d", qoe.GetTotalActiveSessions()),
		)
	}
	return fmt.Sprintf("Tenant: %s\nWindow: last %s\nMetrics:\n- %s\nDecide whether to investigate.",
		snapshot.TenantID,
		snapshot.Window.String(),
		strings.Join(metrics, "\n- "),
	)
}

func buildInvestigationPrompt(snapshot *healthSnapshot, trigger, reason string) string {
	if snapshot == nil {
		return fmt.Sprintf("Trigger: %s\nReason: %s\nNo metrics available.", trigger, reason)
	}
	health := snapshot.Health
	qoe := snapshot.ClientQoE
	metrics := []string{
		fmt.Sprintf("Active streams: %d", snapshot.ActiveStreams),
		fmt.Sprintf("Avg bitrate: %.2f", health.GetAvgBitrate()),
		fmt.Sprintf("Avg FPS: %.2f", health.GetAvgFps()),
		fmt.Sprintf("Avg buffer health: %.2f", health.GetAvgBufferHealth()),
		fmt.Sprintf("Total rebuffer count: %d", health.GetTotalRebufferCount()),
		fmt.Sprintf("Total issue count: %d", health.GetTotalIssueCount()),
		fmt.Sprintf("Has active issues: %t", health.GetHasActiveIssues()),
		fmt.Sprintf("Current quality tier: %s", health.GetCurrentQualityTier()),
	}
	if qoe != nil {
		metrics = append(metrics,
			fmt.Sprintf("Avg packet loss: %.2f", qoe.GetAvgPacketLossRate()),
			fmt.Sprintf("Peak packet loss: %.2f", qoe.GetPeakPacketLossRate()),
			fmt.Sprintf("Avg bandwidth in: %.2f", qoe.GetAvgBandwidthIn()),
			fmt.Sprintf("Avg bandwidth out: %.2f", qoe.GetAvgBandwidthOut()),
			fmt.Sprintf("Total active sessions: %d", qoe.GetTotalActiveSessions()),
		)
	}
	return fmt.Sprintf("Tenant: %s\nTrigger: %s\nReason: %s\nWindow: last %s\nMetrics:\n- %s\nUse tools if needed to diagnose.",
		snapshot.TenantID,
		trigger,
		reason,
		snapshot.Window.String(),
		strings.Join(metrics, "\n- "),
	)
}

func parseDecisionPayload(content string) (decisionPayload, error) {
	var payload decisionPayload
	if content == "" {
		return payload, errors.New("empty decision response")
	}
	raw := extractJSON(content)
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return payload, err
	}
	if payload.Action == "" {
		return payload, errors.New("decision missing action")
	}
	return payload, nil
}

func parseReport(content string) (Report, error) {
	var report Report
	if content == "" {
		return report, errors.New("empty report")
	}
	raw := extractJSON(content)
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		return report, err
	}
	return report, nil
}

func extractJSON(content string) string {
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start >= 0 && end > start {
		return content[start : end+1]
	}
	return content
}

func boolToCount(value bool) uint32 {
	if value {
		return 1
	}
	return 0
}
