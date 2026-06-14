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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/llm"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	defaultHeartbeatInterval = 30 * time.Minute
	defaultSummaryWindow     = 15 * time.Minute
)

const mistMetricSemantics = `Mist metric semantics:
- Mist reports FPS as 0 when frame rate is unknown or dynamic. Treat avg_fps/fps <= 0 as unknown, not as a zero-frame stream, encoder stall, or health fault.
- Only cite FPS as a root-cause signal when it is positive and explicitly below baseline or threshold.`

const mistProcessingSemantics = `FrameWorks processing and track semantics:
- Livepeer handles video ABR when a gateway is available; if no gateway is available, video ABR is stripped and audio/thumbnail processes may still run.
- Local AV compatibility processing generates Opus when incoming audio is not Opus and AAC when incoming audio is not AAC. Treat those audio tracks/processes as expected compatibility work, not unexpected transcoding.
- Thumbnail processing can add JPEG preview/sprite tracks and thumbvtt metadata. MistProcThumbs runs for live+, dvr+, and processing+ sources when thumbnail processing is enabled; vod+ boot is for .dtsh generation, not thumbnail creation.
- Track inventory is diagnostic context. Do not blame derived JPEG, thumbvtt, AAC, or Opus tracks unless timing, buffer, packet loss, or processing error evidence also points there.`

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
Use tools when it helps confirm findings.

` + mistMetricSemantics + `

` + mistProcessingSemantics

type AgentConfig struct {
	Interval          time.Duration
	Orchestrator      Orchestrator
	Periscope         PeriscopeClient
	Purser            BillingClient
	Quartermaster     QuartermasterClient
	Commodore         CommodoreClient
	Decklog           DecklogClient
	Reporter          *Reporter
	Diagnostics       *diagnostics.BaselineEvaluator
	Logger            logging.Logger
	RequiredTierLevel int
	InfraMonitor      *InfraMonitorConfig
}

type Agent struct {
	interval          time.Duration
	orchestrator      Orchestrator
	periscope         PeriscopeClient
	purser            BillingClient
	quartermaster     QuartermasterClient
	commodore         CommodoreClient
	decklog           DecklogClient
	reporter          *Reporter
	diagnostics       *diagnostics.BaselineEvaluator
	cooldown          *diagnostics.TriageCooldown
	perStreamAnalyzer *diagnostics.PerStreamAnalyzer
	logger            logging.Logger
	requiredTierLevel int
	thresholdTrigger  *ThresholdTrigger
	infraMonitor      *InfraMonitor
}

type healthSnapshot struct {
	TenantID      string
	ActiveStreams int
	Window        time.Duration
	Health        *periscopepb.StreamHealthSummary
	ClientQoE     *periscopepb.ClientQoeSummary
}

type Orchestrator interface {
	Run(ctx context.Context, messages []llm.Message, streamer chat.TokenStreamer) (chat.OrchestratorResult, error)
}

type PeriscopeClient interface {
	GetStreamHealthSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error)
	GetClientQoeSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error)
	GetPlatformOverview(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error)
	GetStreamHealthMetrics(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, pagination *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error)
}

type BillingClient interface {
	GetBillingStatus(ctx context.Context, tenantID string) (*purserpb.BillingStatusResponse, error)
}

type QuartermasterClient interface {
	ListActiveTenantsWithMonitoring(ctx context.Context) ([]*quartermasterpb.ActiveTenant, error)
	BootstrapService(ctx context.Context, req *quartermasterpb.BootstrapServiceRequest) (*quartermasterpb.BootstrapServiceResponse, error)
}

// CommodoreClient is the narrow read Skipper needs: per-tenant stream
// monitoring toggles, each carrying the public stream UUID (the key Skipper
// uses for Periscope reads) plus internal_name for logging.
type CommodoreClient interface {
	ListStreamMonitoring(ctx context.Context, tenantID string) (*commodorepb.ListStreamMonitoringResponse, error)
}

type DecklogClient interface {
	SendServiceEvent(event *ipcpb.ServiceEvent) error
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
		interval:          interval,
		orchestrator:      cfg.Orchestrator,
		periscope:         cfg.Periscope,
		purser:            cfg.Purser,
		quartermaster:     cfg.Quartermaster,
		commodore:         cfg.Commodore,
		decklog:           cfg.Decklog,
		reporter:          cfg.Reporter,
		diagnostics:       cfg.Diagnostics,
		cooldown:          diagnostics.NewTriageCooldown(diagnostics.DefaultFlagCooldown),
		perStreamAnalyzer: perStream,
		logger:            cfg.Logger,
		requiredTierLevel: cfg.RequiredTierLevel,
		infraMonitor:      NewInfraMonitor(cfg.InfraMonitor),
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
	eligible, err := a.fetchEligibleTenants(ctx)
	if err != nil {
		a.logger.WithError(err).Warn("Heartbeat tenant discovery failed")
		return
	}
	for _, tm := range eligible {
		if tm.TenantID == "" {
			continue
		}
		if err := a.processTenant(ctx, tm); err != nil {
			a.logger.WithError(err).WithField("tenant_id", tm.TenantID).Warn("Heartbeat processing failed")
		}
	}

	// Infrastructure health check — runs independently of per-tenant stream
	// health. Iterates all clusters and checks node-level metrics (CPU,
	// memory, disk) against hard thresholds + baseline deviations.
	a.infraMonitor.Run(ctx)
}

// fetchEligibleTenants lists active tenants with their tenant-wide monitoring
// switch, then resolves each to a monitoring decision. A tenant is eligible
// when its master switch is on, it has at least one candidate monitored stream
// and it currently has live streams.
func (a *Agent) fetchEligibleTenants(ctx context.Context) ([]tenantMonitoring, error) {
	if a.quartermaster == nil {
		return nil, errors.New("quartermaster client unavailable")
	}
	tenants, err := a.activeTenantsWithMonitoring(ctx)
	if err != nil {
		return nil, err
	}
	eligible := make([]tenantMonitoring, 0, len(tenants))
	for _, row := range tenants {
		tenantID := row.GetTenantId()
		if tenantID == "" {
			continue
		}
		tenantWide := row.GetMonitoringEnabled()
		// Master switch off: skip without touching Periscope/Commodore.
		if !tenantWide {
			continue
		}
		// Cheap pre-filter: idle tenants never reach the Commodore read.
		activeStreams, err := a.countActiveStreams(ctx, tenantID)
		if err != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Heartbeat stream scan failed")
			continue
		}
		if activeStreams == 0 {
			continue
		}
		tm := a.resolveTenantMonitoring(ctx, tenantID, tenantWide)
		if !tm.eligible() {
			continue
		}
		eligible = append(eligible, tm)
	}
	return eligible, nil
}

// activeTenantsWithMonitoring returns active tenants with their tenant-wide
// monitoring switches, preserving Quartermaster's response order.
func (a *Agent) activeTenantsWithMonitoring(ctx context.Context) ([]*quartermasterpb.ActiveTenant, error) {
	rows, err := a.quartermaster.ListActiveTenantsWithMonitoring(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
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
	return int(resp.GetActiveStreams()), nil
}

func (a *Agent) processTenant(ctx context.Context, tm tenantMonitoring) error {
	if a.orchestrator == nil || a.periscope == nil {
		return errors.New("heartbeat dependencies unavailable")
	}
	tenantID := tm.TenantID
	ctx = skipper.WithTenantID(ctx, tenantID)
	ctx = skipper.WithMode(ctx, "heartbeat")
	snapshot, err := a.loadSnapshot(ctx, tm)
	if err != nil {
		return err
	}
	// No live monitored streams this cycle (e.g. scoped tenant whose monitored
	// streams aren't currently live): nothing to evaluate.
	if snapshot == nil || snapshot.Health == nil {
		a.logger.WithField("tenant_id", tenantID).Info("HEARTBEAT_OK")
		return nil
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
		streamAnomalies = a.collectPerStreamAnomalies(ctx, tm)
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
		if h.GetAvgFps() > 0 {
			m["avg_fps"] = h.GetAvgFps()
		}
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

func (a *Agent) collectPerStreamAnomalies(ctx context.Context, tm tenantMonitoring) []diagnostics.StreamAnomaly {
	tenantID := tm.TenantID
	now := time.Now()
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-defaultSummaryWindow),
		EndTime:   now,
	}
	pagination := &periscope.CursorPaginationOpts{First: 100}
	var allMetrics []*periscopepb.StreamHealthMetric
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
	// Constrain anomalies to the monitored streams so muted/OFF streams never
	// surface. Skipped when the monitored set covers everything, where
	// filtering is a no-op. Both sides key on the public stream UUID
	// (tm.Monitored holds commodore.streams.id; StreamHealthMetric.stream_id
	// is the same UUID column).
	if !tm.coversAll() {
		monitoredSet := make(map[string]struct{}, len(tm.Monitored))
		for _, n := range tm.Monitored {
			monitoredSet[n] = struct{}{}
		}
		filtered := make([]*periscopepb.StreamHealthMetric, 0, len(allMetrics))
		for _, m := range allMetrics {
			if _, ok := monitoredSet[m.GetStreamId()]; ok {
				filtered = append(filtered, m)
			}
		}
		allMetrics = filtered
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

func (a *Agent) loadSnapshot(ctx context.Context, tm tenantMonitoring) (*healthSnapshot, error) {
	tenantID := tm.TenantID
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	now := time.Now()
	window := defaultSummaryWindow
	timeRange := &periscope.TimeRangeOpts{
		StartTime: now.Add(-window),
		EndTime:   now,
	}

	// Fast path: the monitored set covers every stream (for example, an
	// entitled tenant with no per-stream overrides), so the cheap tenant-wide
	// aggregate is exactly the monitored aggregate.
	if tm.coversAll() {
		healthResp, err := a.periscope.GetStreamHealthSummary(ctx, tenantID, nil, timeRange)
		if err != nil {
			return nil, err
		}
		qoeResp, err := a.periscope.GetClientQoeSummary(ctx, tenantID, nil, timeRange)
		if err != nil && a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Heartbeat client QoE lookup failed")
		}
		var qoeSummary *periscopepb.ClientQoeSummary
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

	// Scoped path: aggregate over only the monitored streams. tm.Monitored
	// holds public stream UUIDs (commodore.streams.id) — the same identifier
	// Periscope stores as stream_health_5m.stream_id and filters on. A
	// monitored stream with no samples in the window is simply not live right
	// now and is skipped; liveness comes from SampleCount.
	healthParts := make([]*periscopepb.StreamHealthSummary, 0, len(tm.Monitored))
	qoeParts := make([]*periscopepb.ClientQoeSummary, 0, len(tm.Monitored))
	liveCount := 0
	lookupErrs := 0
	for _, streamID := range tm.Monitored {
		healthResp, err := a.periscope.GetStreamHealthSummary(ctx, tenantID, &streamID, timeRange)
		if err != nil {
			lookupErrs++
			if a.logger != nil {
				a.logger.WithError(err).WithField("tenant_id", tenantID).WithField("stream_id", streamID).Warn("Scoped stream health lookup failed")
			}
			continue
		}
		summary := healthResp.GetSummary()
		if summary == nil || summary.GetSampleCount() <= 0 {
			continue
		}
		liveCount++
		healthParts = append(healthParts, summary)

		qoeResp, err := a.periscope.GetClientQoeSummary(ctx, tenantID, &streamID, timeRange)
		if err != nil {
			if a.logger != nil {
				a.logger.WithError(err).WithField("tenant_id", tenantID).WithField("stream_id", streamID).Warn("Scoped client QoE lookup failed")
			}
		} else if qoeResp != nil && qoeResp.GetSummary() != nil {
			qoeParts = append(qoeParts, qoeResp.GetSummary())
		}
	}
	if liveCount == 0 {
		// Distinguish "no monitored stream is live" (clean no-op) from "every
		// lookup failed" (telemetry outage) — the latter must not be reported
		// as healthy.
		if lookupErrs > 0 {
			return nil, fmt.Errorf("scoped stream health lookups failed for all %d monitored streams", lookupErrs)
		}
		return nil, nil
	}
	return &healthSnapshot{
		TenantID:      tenantID,
		ActiveStreams: liveCount,
		Window:        window,
		Health:        aggregateHealthSummaries(healthParts),
		ClientQoE:     aggregateQoeSummaries(qoeParts),
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
	agg := &ipcpb.APIRequestAggregate{
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
	batch := &ipcpb.APIRequestBatch{
		Timestamp:  time.Now().Unix(),
		SourceNode: "skipper",
		Aggregates: []*ipcpb.APIRequestAggregate{agg},
	}
	event := &ipcpb.ServiceEvent{
		EventType: "api_request_batch",
		Timestamp: timestamppb.Now(),
		Source:    "skipper",
		TenantId:  tenantID,
		Payload:   &ipcpb.ServiceEvent_ApiRequestBatch{ApiRequestBatch: batch},
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
	fmt.Fprintf(&b, "\n%s\n\n%s\n", mistMetricSemantics, mistProcessingSemantics)

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
	if health.GetAvgFps() > 0 {
		fmt.Fprintf(&b, "- Avg FPS: %.2f\n", health.GetAvgFps())
	} else {
		b.WriteString("- Avg FPS: unknown\n")
	}
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
