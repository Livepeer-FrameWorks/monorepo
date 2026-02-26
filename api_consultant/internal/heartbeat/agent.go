package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_consultant/internal/chat"
	"frameworks/api_consultant/internal/diagnostics"
	"frameworks/api_consultant/internal/skipper"
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
	Periscope         PeriscopeClient
	Purser            BillingClient
	Quartermaster     QuartermasterClient
	Decklog           DecklogClient
	Reporter          *Reporter
	Diagnostics       *diagnostics.BaselineEvaluator
	Logger            logging.Logger
	RequiredTierLevel int
	InfraMonitor      *InfraMonitorConfig

	// Callbacks for external consumers (e.g. social posting agent).
	OnPlatformOverview  func(tenantID string, overview *pb.GetPlatformOverviewResponse)
	OnFederationSummary func(tenantID string, summary *pb.GetFederationSummaryResponse)
}

type Agent struct {
	interval          time.Duration
	orchestrator      Orchestrator
	periscope         PeriscopeClient
	purser            BillingClient
	quartermaster     QuartermasterClient
	decklog           DecklogClient
	reporter          *Reporter
	diagnostics       *diagnostics.BaselineEvaluator
	cooldown          *diagnostics.TriageCooldown
	perStreamAnalyzer *diagnostics.PerStreamAnalyzer
	logger            logging.Logger
	requiredTierLevel int
	thresholdTrigger  *ThresholdTrigger
	infraMonitor      *InfraMonitor

	onPlatformOverview  func(string, *pb.GetPlatformOverviewResponse)
	onFederationSummary func(string, *pb.GetFederationSummaryResponse)
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

type PeriscopeClient interface {
	GetStreamHealthSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*pb.GetStreamHealthSummaryResponse, error)
	GetClientQoeSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*pb.GetClientQoeSummaryResponse, error)
	GetPlatformOverview(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*pb.GetPlatformOverviewResponse, error)
	GetStreamHealthMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, pagination *periscope.CursorPaginationOpts) (*pb.GetStreamHealthMetricsResponse, error)
	GetFederationSummary(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*pb.GetFederationSummaryResponse, error)
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
	var perStream *diagnostics.PerStreamAnalyzer
	if cfg.Diagnostics != nil {
		perStream = diagnostics.NewPerStreamAnalyzer(cfg.Diagnostics)
	}
	agent := &Agent{
		interval:            interval,
		orchestrator:        cfg.Orchestrator,
		periscope:           cfg.Periscope,
		purser:              cfg.Purser,
		quartermaster:       cfg.Quartermaster,
		decklog:             cfg.Decklog,
		reporter:            cfg.Reporter,
		diagnostics:         cfg.Diagnostics,
		cooldown:            diagnostics.NewTriageCooldown(diagnostics.DefaultFlagCooldown),
		perStreamAnalyzer:   perStream,
		logger:              cfg.Logger,
		requiredTierLevel:   cfg.RequiredTierLevel,
		infraMonitor:        NewInfraMonitor(cfg.InfraMonitor),
		onPlatformOverview:  cfg.OnPlatformOverview,
		onFederationSummary: cfg.OnFederationSummary,
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

	// Collect federation metrics across all active tenants for external
	// consumers (social agent). Runs separately from per-tenant diagnostics
	// because federation data is relevant even for tenants without streams.
	a.collectFederation(ctx)

	// Infrastructure health check — runs independently of per-tenant stream
	// health. Iterates all clusters and checks node-level metrics (CPU,
	// memory, disk) against hard thresholds + baseline deviations.
	a.infraMonitor.Run(ctx)
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
	if a.periscope == nil {
		return 0, errors.New("periscope client unavailable")
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	resp, err := a.periscope.GetPlatformOverview(ctx, tenantID, nil)
	if err != nil {
		return 0, err
	}
	if a.onPlatformOverview != nil {
		a.onPlatformOverview(tenantID, resp)
	}
	return int(resp.GetActiveStreams()), nil
}

func (a *Agent) collectFederation(ctx context.Context) {
	if a.onFederationSummary == nil || a.periscope == nil || a.quartermaster == nil {
		return
	}
	tenants, err := a.quartermaster.ListActiveTenants(ctx)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-24 * time.Hour),
		EndTime:   now,
	}
	for _, tenantID := range tenants {
		summary, err := a.periscope.GetFederationSummary(ctx, tenantID, timeRange)
		if err != nil {
			continue
		}
		if s := summary.GetSummary(); s != nil && s.GetTotalEvents() > 0 {
			a.onFederationSummary(tenantID, summary)
		}
	}
}

func (a *Agent) processTenant(ctx context.Context, tenantID string) error {
	if a.orchestrator == nil || a.periscope == nil {
		return errors.New("heartbeat dependencies unavailable")
	}
	ctx = skipper.WithTenantID(ctx, tenantID)
	ctx = skipper.WithMode(ctx, "heartbeat")
	snapshot, err := a.loadSnapshot(ctx, tenantID)
	if err != nil {
		return err
	}
	if snapshot == nil || snapshot.Health == nil {
		return errors.New("missing health snapshot")
	}

	metrics := snapshotToMetricMap(snapshot)

	// Compare against previous baseline before updating with current sample,
	// so the current observation doesn't dilute the anomaly signal.
	var deviations []diagnostics.Deviation
	if a.diagnostics != nil {
		deviations, _ = a.diagnostics.Deviations(ctx, tenantID, "", metrics)
	}

	// Feed baselines (heartbeat is the sole writer).
	if a.diagnostics != nil {
		if err := a.diagnostics.Update(ctx, tenantID, "", metrics); err != nil && a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Baseline update failed")
		}
	}
	violations := a.thresholdTrigger.Check(snapshot)
	correlations := diagnostics.Correlate(deviations)
	result := diagnostics.Triage(violations, deviations, correlations)

	// Stale baseline cleanup (best-effort).
	if a.diagnostics != nil {
		_ = a.diagnostics.Cleanup(ctx, tenantID, 7*24*time.Hour)
	}

	// Per-stream analysis when triage signals trouble.
	var streamAnomalies []diagnostics.StreamAnomaly
	if result.Action != diagnostics.TriageOK && a.perStreamAnalyzer != nil && a.periscope != nil {
		streamAnomalies = a.collectPerStreamAnomalies(ctx, tenantID, snapshot)
	}

	switch result.Action {
	case diagnostics.TriageInvestigate:
		report, tokens, err := a.Investigate(ctx, tenantID, result.Trigger, result.Reason, snapshot, &result, streamAnomalies)
		if logErr := a.logUsage(ctx, tenantID, tokens, err != nil); logErr != nil {
			a.logger.WithError(logErr).WithField("tenant_id", tenantID).Warn("Heartbeat usage logging failed")
		}
		if err != nil {
			return err
		}
		a.logger.WithField("tenant_id", tenantID).WithField("report", report.FormatMarkdown()).Info("HEARTBEAT_INVESTIGATION")
	case diagnostics.TriageFlag:
		if a.cooldown.ShouldFlag(tenantID) {
			if a.reporter != nil {
				flagReport := Report{
					Trigger:         result.Trigger,
					Summary:         result.Reason,
					MetricsReviewed: metricsFromDeviations(result.Deviations),
					RootCause:       "pending review",
					Recommendations: []Recommendation{},
				}
				_ = a.reporter.Send(ctx, tenantID, flagReport)
			}
			a.logger.WithField("tenant_id", tenantID).WithField("reason", result.Reason).Info("HEARTBEAT_FLAG")
		}
	default:
		a.logger.WithField("tenant_id", tenantID).Info("HEARTBEAT_OK")
	}
	return nil
}

func snapshotToMetricMap(snapshot *healthSnapshot) map[string]float64 {
	m := make(map[string]float64)
	if snapshot == nil {
		return m
	}
	if h := snapshot.Health; h != nil {
		m["avg_bitrate"] = h.GetAvgBitrate()
		m["avg_fps"] = h.GetAvgFps()
		m["avg_buffer_health"] = h.GetAvgBufferHealth()
		m["total_rebuffer_count"] = float64(h.GetTotalRebufferCount())
		m["total_issue_count"] = float64(h.GetTotalIssueCount())
	}
	if q := snapshot.ClientQoE; q != nil {
		m["avg_packet_loss"] = q.GetAvgPacketLossRate()
		m["avg_bandwidth_in"] = q.GetAvgBandwidthIn()
		m["avg_bandwidth_out"] = q.GetAvgBandwidthOut()
		m["total_active_sessions"] = float64(q.GetTotalActiveSessions())
	}
	return m
}

func metricsFromDeviations(devs []diagnostics.Deviation) []string {
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Metric
	}
	return names
}

func (a *Agent) collectPerStreamAnomalies(ctx context.Context, tenantID string, snapshot *healthSnapshot) []diagnostics.StreamAnomaly {
	now := time.Now()
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-defaultSummaryWindow),
		EndTime:   now,
	}
	pagination := &periscope.CursorPaginationOpts{First: 100}
	var allMetrics []*pb.StreamHealthMetric
	for {
		resp, err := a.periscope.GetStreamHealthMetrics(ctx, tenantID, nil, timeRange, pagination)
		if err != nil {
			if a.logger != nil {
				a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Per-stream metrics fetch failed")
			}
			break
		}
		allMetrics = append(allMetrics, resp.GetMetrics()...)
		pg := resp.GetPagination()
		if pg == nil || !pg.GetHasNextPage() {
			break
		}
		cursor := pg.GetEndCursor()
		if cursor == "" {
			break
		}
		pagination = &periscope.CursorPaginationOpts{First: 100, After: &cursor}
	}
	if len(allMetrics) == 0 {
		return nil
	}
	anomalies, err := a.perStreamAnalyzer.Analyze(ctx, tenantID, allMetrics)
	if err != nil {
		if a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Per-stream analysis failed")
		}
		return nil
	}
	return anomalies
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

func (a *Agent) Investigate(ctx context.Context, tenantID, trigger, reason string, snapshot *healthSnapshot, triage *diagnostics.TriageResult, streamAnomalies []diagnostics.StreamAnomaly) (Report, chat.TokenCounts, error) {
	if a.orchestrator == nil {
		return Report{}, chat.TokenCounts{}, errors.New("orchestrator unavailable")
	}
	prompt := buildInvestigationPrompt(snapshot, trigger, reason, triage, streamAnomalies)
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

func (a *Agent) logUsage(ctx context.Context, tenantID string, tokens chat.TokenCounts, hadError bool) error {
	if tenantID == "" {
		return errors.New("missing tenant id for usage logging")
	}
	if a.decklog == nil {
		return errors.New("decklog client unavailable")
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
	if err := a.decklog.SendServiceEvent(event); err != nil {
		if a.logger != nil {
			a.logger.WithError(err).Warn("Failed to emit heartbeat usage event")
		}
		return err
	}
	return nil
}

func buildInvestigationPrompt(snapshot *healthSnapshot, trigger, reason string, triage *diagnostics.TriageResult, streamAnomalies []diagnostics.StreamAnomaly) string {
	if snapshot == nil {
		return fmt.Sprintf("Trigger: %s\nReason: %s\nNo metrics available.", trigger, reason)
	}
	health := snapshot.Health
	qoe := snapshot.ClientQoE
	var b strings.Builder
	fmt.Fprintf(&b, "Tenant: %s\nTrigger: %s\nReason: %s\nWindow: last %s\n", snapshot.TenantID, trigger, reason, snapshot.Window)

	if triage != nil && len(triage.Deviations) > 0 {
		b.WriteString("\nBaseline Deviations:\n")
		for _, d := range triage.Deviations {
			fmt.Fprintf(&b, "- %s\n", d.String())
		}
	}
	if triage != nil && len(triage.Correlations) > 0 {
		b.WriteString("\nCorrelations:\n")
		for _, c := range triage.Correlations {
			fmt.Fprintf(&b, "- %s (confidence %.2f)\n", c.Hypothesis, c.Confidence)
		}
	}

	if len(streamAnomalies) > 0 {
		b.WriteString("\nPer-Stream Anomalies:\n")
		for _, sa := range streamAnomalies {
			fmt.Fprintf(&b, "- stream %s (max σ=%.1f):\n", sa.StreamID, sa.MaxSigma)
			for _, d := range sa.Deviations {
				fmt.Fprintf(&b, "    %s\n", d.String())
			}
			for _, c := range sa.Correlations {
				fmt.Fprintf(&b, "    correlation: %s (confidence %.2f)\n", c.Hypothesis, c.Confidence)
			}
		}
	}

	b.WriteString("\nRaw Metrics:\n")
	fmt.Fprintf(&b, "- Active streams: %d\n", snapshot.ActiveStreams)
	fmt.Fprintf(&b, "- Avg bitrate: %.2f\n", health.GetAvgBitrate())
	fmt.Fprintf(&b, "- Avg FPS: %.2f\n", health.GetAvgFps())
	fmt.Fprintf(&b, "- Avg buffer health: %.2f\n", health.GetAvgBufferHealth())
	fmt.Fprintf(&b, "- Total rebuffer count: %d\n", health.GetTotalRebufferCount())
	fmt.Fprintf(&b, "- Total issue count: %d\n", health.GetTotalIssueCount())
	fmt.Fprintf(&b, "- Has active issues: %t\n", health.GetHasActiveIssues())
	fmt.Fprintf(&b, "- Current quality tier: %s\n", health.GetCurrentQualityTier())
	if qoe != nil {
		fmt.Fprintf(&b, "- Avg packet loss: %.4f\n", qoe.GetAvgPacketLossRate())
		fmt.Fprintf(&b, "- Peak packet loss: %.4f\n", qoe.GetPeakPacketLossRate())
		fmt.Fprintf(&b, "- Avg bandwidth in: %.2f\n", qoe.GetAvgBandwidthIn())
		fmt.Fprintf(&b, "- Avg bandwidth out: %.2f\n", qoe.GetAvgBandwidthOut())
		fmt.Fprintf(&b, "- Total active sessions: %d\n", qoe.GetTotalActiveSessions())
	}
	b.WriteString("\nUse tools if needed to diagnose.")
	return b.String()
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
