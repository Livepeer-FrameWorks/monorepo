package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterQoETools registers QoE diagnostic MCP tools.
func RegisterQoETools(server *mcp.Server, serviceClients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// diagnose_rebuffering - Analyze rebuffer events for a stream
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "diagnose_rebuffering",
			Description: "Analyze rebuffering events for a stream. Returns rebuffer count, duration patterns, and recommendations.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DiagnoseRebufferingInput) (*mcp.CallToolResult, any, error) {
			return handleDiagnoseRebuffering(ctx, args, serviceClients, logger)
		},
	)

	// diagnose_buffer_health - Analyze buffer state transitions
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "diagnose_buffer_health",
			Description: "Analyze buffer health and state transitions. Identifies dry buffer events and quality fluctuations.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DiagnoseBufferHealthInput) (*mcp.CallToolResult, any, error) {
			return handleDiagnoseBufferHealth(ctx, args, serviceClients, logger)
		},
	)

	// diagnose_packet_loss - Analyze packet loss for a stream
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "diagnose_packet_loss",
			Description: "Analyze packet loss for a stream with protocol-aware guidance. Uses client QoE packet loss rollups and stream metrics when available.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DiagnosePacketLossInput) (*mcp.CallToolResult, any, error) {
			return handleDiagnosePacketLoss(ctx, args, serviceClients, logger)
		},
	)

	// diagnose_routing - Analyze CDN routing decisions
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "diagnose_routing",
			Description: "Analyze CDN routing decisions for a stream. Shows node selection patterns and geographic distribution.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args DiagnoseRoutingInput) (*mcp.CallToolResult, any, error) {
			return handleDiagnoseRouting(ctx, args, serviceClients, logger)
		},
	)

	// get_stream_health_summary - Get aggregated health metrics
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_stream_health_summary",
			Description: "Get aggregated health metrics for a stream over a time range. Includes bitrate, FPS, quality tier, and issue counts.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetStreamHealthInput) (*mcp.CallToolResult, any, error) {
			return handleGetStreamHealth(ctx, args, serviceClients, logger)
		},
	)

	// get_anomaly_report - Detect statistical anomalies
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "get_anomaly_report",
			Description: "Detect anomalies across stream metrics. Compares recent performance to baseline and flags deviations.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetAnomalyReportInput) (*mcp.CallToolResult, any, error) {
			return handleGetAnomalyReport(ctx, args, serviceClients, logger)
		},
	)
}

// Input types

type DiagnoseRebufferingInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	TimeRange string `json:"time_range,omitempty" jsonschema_description:"Time range: last_1h (default), last_6h, last_24h, last_7d"`
}

type DiagnoseBufferHealthInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	TimeRange string `json:"time_range,omitempty" jsonschema_description:"Time range: last_1h (default), last_6h, last_24h, last_7d"`
}

type DiagnosePacketLossInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	TimeRange string `json:"time_range,omitempty" jsonschema_description:"Time range: last_1h (default), last_6h, last_24h, last_7d"`
}

type DiagnoseRoutingInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	TimeRange string `json:"time_range,omitempty" jsonschema_description:"Time range: last_1h (default), last_6h, last_24h, last_7d"`
}

type GetStreamHealthInput struct {
	StreamID  string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	TimeRange string `json:"time_range,omitempty" jsonschema_description:"Time range: last_1h (default), last_6h, last_24h, last_7d"`
}

type GetAnomalyReportInput struct {
	StreamID    string `json:"stream_id" jsonschema:"required" jsonschema_description:"Relay ID or stream_id to analyze"`
	Sensitivity string `json:"sensitivity,omitempty" jsonschema_description:"Anomaly sensitivity: low, medium (default), high"`
}

// Output types

type DiagnosticResult struct {
	Status          string                 `json:"status"`
	Metrics         map[string]interface{} `json:"metrics"`
	Analysis        string                 `json:"analysis"`
	Recommendations []string               `json:"recommendations,omitempty"`
}

const (
	protocolTypeRealtime  = "realtime"
	protocolTypeStreaming = "streaming"
	protocolTypeUnknown   = "unknown"
)

const (
	packetLossRealtimeHealthy  = 0.005
	packetLossRealtimeWarning  = 0.01
	packetLossStreamingHealthy = 0.01
	packetLossStreamingWarning = 0.05
)

// normalizeTimeRange converts a time range string to start/end times and returns the normalized label.
func normalizeTimeRange(tr string) (string, *periscope.TimeRangeOpts) {
	if tr == "" {
		tr = "last_1h"
	}
	now := time.Now()
	var start time.Time

	switch tr {
	case "last_6h":
		start = now.Add(-6 * time.Hour)
	case "last_24h":
		start = now.Add(-24 * time.Hour)
	case "last_7d":
		start = now.Add(-7 * 24 * time.Hour)
	default: // last_1h
		start = now.Add(-1 * time.Hour)
	}

	return tr, &periscope.TimeRangeOpts{
		StartTime: start,
		EndTime:   now,
	}
}

func classifyProtocol(protocol string) string {
	p := strings.ToLower(protocol)
	switch {
	case strings.Contains(p, "webrtc"),
		strings.Contains(p, "srt"),
		strings.Contains(p, "rtp"),
		strings.Contains(p, "udp"):
		return protocolTypeRealtime
	case strings.Contains(p, "hls"),
		strings.Contains(p, "dash"),
		strings.Contains(p, "rtmp"),
		strings.Contains(p, "http"):
		return protocolTypeStreaming
	default:
		return protocolTypeUnknown
	}
}

func packetLossStatus(protocolType string, avgLoss float64) string {
	switch protocolType {
	case protocolTypeRealtime:
		if avgLoss <= packetLossRealtimeHealthy {
			return "healthy"
		}
		if avgLoss <= packetLossRealtimeWarning {
			return "warning"
		}
		return "critical"
	case protocolTypeStreaming:
		if avgLoss <= packetLossStreamingHealthy {
			return "healthy"
		}
		if avgLoss <= packetLossStreamingWarning {
			return "warning"
		}
		return "critical"
	default:
		if avgLoss <= packetLossStreamingHealthy {
			return "healthy"
		}
		if avgLoss <= packetLossStreamingWarning {
			return "warning"
		}
		return "critical"
	}
}

func handleDiagnoseRebuffering(ctx context.Context, args DiagnoseRebufferingInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	timeRangeLabel, timeRange := normalizeTimeRange(args.TimeRange)
	resp, err := serviceClients.Periscope.GetRebufferingEvents(ctx, tenantID, &streamID, nil, timeRange, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get rebuffering events")
		return toolError(fmt.Sprintf("Failed to fetch rebuffering data: %v", err))
	}

	rebufferCount := len(resp.Events)
	var totalDurationMs int64
	for _, evt := range resp.Events {
		if evt.RebufferStart != nil && evt.RebufferEnd != nil {
			duration := evt.RebufferEnd.AsTime().Sub(evt.RebufferStart.AsTime())
			totalDurationMs += duration.Milliseconds()
		}
	}

	avgDurationMs := int64(0)
	if rebufferCount > 0 {
		avgDurationMs = totalDurationMs / int64(rebufferCount)
	}

	status := "healthy"
	analysis := "No rebuffering detected in the time range."
	var recommendations []string

	if rebufferCount > 0 {
		if rebufferCount > 20 || avgDurationMs > 3000 {
			status = "critical"
			analysis = fmt.Sprintf("Critical rebuffering detected. %d events with avg duration of %dms indicates serious quality issues.", rebufferCount, avgDurationMs)
			recommendations = []string{
				"Check encoder output bitrate - may need to reduce by 20-30%",
				"Verify stable upload connection - high latency or packet loss likely",
				"Consider shorter keyframe intervals (2 seconds recommended)",
				"Check for network congestion or bandwidth limitations",
			}
		} else if rebufferCount > 5 || avgDurationMs > 1500 {
			status = "warning"
			analysis = fmt.Sprintf("Elevated rebuffering. %d events with avg duration of %dms.", rebufferCount, avgDurationMs)
			recommendations = []string{
				"Monitor encoder bitrate stability",
				"Consider enabling adaptive bitrate if not already active",
				"Check viewer connection quality metrics",
			}
		} else {
			status = "healthy"
			analysis = fmt.Sprintf("Minor rebuffering: %d events, avg %dms duration. Within normal range.", rebufferCount, avgDurationMs)
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"rebuffer_count":           rebufferCount,
			"avg_rebuffer_duration_ms": avgDurationMs,
			"total_rebuffer_time_ms":   totalDurationMs,
			"time_range":               timeRangeLabel,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

func handleDiagnoseBufferHealth(ctx context.Context, args DiagnoseBufferHealthInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	timeRangeLabel, timeRange := normalizeTimeRange(args.TimeRange)
	rollupResp, err := serviceClients.Periscope.GetStreamHealth5m(ctx, tenantID, streamID, timeRange, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get stream health metrics")
		return toolError(fmt.Sprintf("Failed to fetch buffer health data: %v", err))
	}

	metricsResp, err := serviceClients.Periscope.GetStreamHealthMetrics(ctx, &streamID, timeRange, &periscope.CursorPaginationOpts{First: 200})
	if err != nil {
		logger.WithError(err).Warn("Failed to get stream health samples")
	}

	rebufferResp, err := serviceClients.Periscope.GetRebufferingEvents(ctx, tenantID, &streamID, nil, timeRange, &periscope.CursorPaginationOpts{First: 200})
	if err != nil {
		logger.WithError(err).Warn("Failed to get rebuffering events")
	}

	if len(rollupResp.Records) == 0 && (metricsResp == nil || len(metricsResp.Metrics) == 0) && (rebufferResp == nil || len(rebufferResp.Events) == 0) {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]interface{}{
				"time_range": timeRangeLabel,
			},
			Analysis: "No buffer health data available for this stream in the specified time range. The stream may be offline or not yet ingesting.",
		}
		return toolSuccessJSON(result)
	}

	var totalBufferHealth float32
	var totalDryCount int32
	var totalIssueCount int32
	var minBufferHealth float32 = 1.0
	var sampleCount int32

	for _, record := range rollupResp.Records {
		totalBufferHealth += record.AvgBufferHealth
		totalDryCount += record.BufferDryCount
		totalIssueCount += record.IssueCount
		if record.AvgBufferHealth < minBufferHealth {
			minBufferHealth = record.AvgBufferHealth
		}
		sampleCount++
	}

	avgBufferHealth := float32(0)
	if sampleCount > 0 {
		avgBufferHealth = totalBufferHealth / float32(sampleCount)
	}

	var metricSamples int
	var minSampleBufferHealth float32 = 1.0
	var maxSampleBufferHealth float32
	var drySampleCount int
	if metricsResp != nil {
		for _, metric := range metricsResp.Metrics {
			if metric.BufferHealth > 0 {
				if metric.BufferHealth < minSampleBufferHealth {
					minSampleBufferHealth = metric.BufferHealth
				}
				if metric.BufferHealth > maxSampleBufferHealth {
					maxSampleBufferHealth = metric.BufferHealth
				}
				metricSamples++
			}
			if strings.ToUpper(metric.BufferState) == "DRY" {
				drySampleCount++
			}
		}
	}

	rebufferCount := 0
	var totalRebufferDurationMs int64
	if rebufferResp != nil {
		for _, evt := range rebufferResp.Events {
			if evt.RebufferStart != nil && evt.RebufferEnd != nil {
				duration := evt.RebufferEnd.AsTime().Sub(evt.RebufferStart.AsTime())
				totalRebufferDurationMs += duration.Milliseconds()
				rebufferCount++
			}
		}
	}

	status := "healthy"
	analysis := fmt.Sprintf("Buffer health is stable. Average: %.1f%%, minimum: %.1f%%.", avgBufferHealth*100, minBufferHealth*100)
	var recommendations []string

	if minBufferHealth < 0.3 || totalDryCount > 10 || drySampleCount > 10 {
		status = "critical"
		analysis = fmt.Sprintf("Critical buffer issues. Min health: %.1f%%, %d buffer dry events. Viewers are experiencing interruptions.", minBufferHealth*100, totalDryCount)
		recommendations = []string{
			"Reduce encoder bitrate immediately",
			"Check for upstream network issues",
			"Verify CDN node capacity",
		}
	} else if avgBufferHealth < 0.6 || totalDryCount > 3 || drySampleCount > 3 {
		status = "warning"
		analysis = fmt.Sprintf("Buffer health degraded. Avg: %.1f%%, %d dry events.", avgBufferHealth*100, totalDryCount)
		recommendations = []string{
			"Consider reducing bitrate by 10-20%",
			"Monitor viewer quality feedback",
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"avg_buffer_health":          avgBufferHealth,
			"min_buffer_health":          minBufferHealth,
			"buffer_dry_count":           totalDryCount,
			"issue_count":                totalIssueCount,
			"rollup_sample_count":        sampleCount,
			"metric_sample_count":        metricSamples,
			"min_sample_buffer_health":   minSampleBufferHealth,
			"max_sample_buffer_health":   maxSampleBufferHealth,
			"dry_sample_count":           drySampleCount,
			"rebuffer_event_count":       rebufferCount,
			"rebuffer_total_duration_ms": totalRebufferDurationMs,
			"time_range":                 timeRangeLabel,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

func handleDiagnosePacketLoss(ctx context.Context, args DiagnosePacketLossInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	timeRangeLabel, timeRange := normalizeTimeRange(args.TimeRange)

	clientResp, err := serviceClients.Periscope.GetClientMetrics5m(ctx, tenantID, &streamID, nil, timeRange, &periscope.CursorPaginationOpts{First: 200})
	if err != nil {
		logger.WithError(err).Warn("Failed to get client QoE metrics")
		return toolError(fmt.Sprintf("Failed to fetch client QoE metrics: %v", err))
	}

	var lossSum float64
	var maxLoss float64
	lossSamples := 0
	var avgBandwidthOut float64
	for _, record := range clientResp.Records {
		avgBandwidthOut += record.AvgBandwidthOut
		if record.PacketLossRate != nil {
			v := float64(*record.PacketLossRate)
			lossSum += v
			lossSamples++
			if v > maxLoss {
				maxLoss = v
			}
		}
	}
	if len(clientResp.Records) > 0 {
		avgBandwidthOut = avgBandwidthOut / float64(len(clientResp.Records))
	}

	if lossSamples == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]interface{}{
				"time_range": timeRangeLabel,
			},
			Analysis: "No packet loss samples available for this stream in the specified time range.",
		}
		return toolSuccessJSON(result)
	}

	avgLoss := lossSum / float64(lossSamples)

	protocol := ""
	protocolType := protocolTypeUnknown
	eventResp, err := serviceClients.Periscope.GetStreamEvents(ctx, streamID, timeRange, &periscope.CursorPaginationOpts{First: 20})
	if err == nil {
		for _, evt := range eventResp.Events {
			if evt.Protocol != nil && *evt.Protocol != "" {
				protocol = *evt.Protocol
				protocolType = classifyProtocol(protocol)
				break
			}
		}
	} else {
		logger.WithError(err).Warn("Failed to get stream events for protocol detection")
	}

	status := packetLossStatus(protocolType, avgLoss)
	analysis := fmt.Sprintf("Average packet loss: %.2f%% (max %.2f%%) across %d samples.", avgLoss*100, maxLoss*100, lossSamples)
	recommendations := []string{
		"Check network path for congestion or unstable uplink",
		"Verify encoder bitrate is within available bandwidth",
	}

	switch protocolType {
	case protocolTypeRealtime:
		analysis = fmt.Sprintf("Real-time protocol detected (%s). Average loss: %.2f%%, max %.2f%%.", protocol, avgLoss*100, maxLoss*100)
		recommendations = append(recommendations,
			"Consider SRT with ARQ/FEC on lossy networks",
			"Prefer wired networks over Wi-Fi for ingestion",
		)
	case protocolTypeStreaming:
		analysis = fmt.Sprintf("Buffered protocol detected (%s). Transport loss is masked by TCP but can reduce throughput. Avg loss: %.2f%%.", protocol, avgLoss*100)
		recommendations = append(recommendations,
			"Correlate with rebuffering and buffer health metrics",
			"Reduce bitrate or increase segment duration to stabilize throughput",
		)
	default:
		analysis = fmt.Sprintf("Protocol unknown. Average packet loss: %.2f%%, max %.2f%%.", avgLoss*100, maxLoss*100)
		recommendations = append(recommendations,
			"Identify streaming protocol to interpret loss impact more accurately",
		)
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"avg_packet_loss_rate":  avgLoss,
			"max_packet_loss_rate":  maxLoss,
			"loss_sample_count":     lossSamples,
			"avg_bandwidth_out_bps": avgBandwidthOut,
			"protocol":              protocol,
			"protocol_type":         protocolType,
			"time_range":            timeRangeLabel,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

func handleDiagnoseRouting(ctx context.Context, args DiagnoseRoutingInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	timeRangeLabel, timeRange := normalizeTimeRange(args.TimeRange)
	resp, err := serviceClients.Periscope.GetRoutingEvents(ctx, &streamID, timeRange, nil, []string{tenantID}, nil, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get routing events")
		return toolError(fmt.Sprintf("Failed to fetch routing data: %v", err))
	}

	if len(resp.Events) == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]interface{}{
				"time_range": timeRangeLabel,
			},
			Analysis: "No routing events found. The stream may not have had any viewers in this time range.",
		}
		return toolSuccessJSON(result)
	}

	nodeUsage := make(map[string]int)
	countryDistribution := make(map[string]int)
	var totalDistance float64
	distanceCount := 0
	failedRoutings := 0

	for _, evt := range resp.Events {
		if evt.SelectedNode != "" {
			nodeUsage[evt.SelectedNode]++
		}
		if evt.ClientCountry != nil && *evt.ClientCountry != "" {
			countryDistribution[*evt.ClientCountry]++
		}
		if evt.RoutingDistance != nil {
			totalDistance += *evt.RoutingDistance
			distanceCount++
		}
		if evt.Status == "failed" || evt.Status == "error" {
			failedRoutings++
		}
	}

	avgDistance := float64(0)
	if distanceCount > 0 {
		avgDistance = totalDistance / float64(distanceCount)
	}

	status := "healthy"
	analysis := fmt.Sprintf("Routing is functioning normally. %d routing events across %d nodes, serving %d countries.", len(resp.Events), len(nodeUsage), len(countryDistribution))
	var recommendations []string

	failureRate := float64(failedRoutings) / float64(len(resp.Events))
	if failureRate > 0.1 {
		status = "critical"
		analysis = fmt.Sprintf("High routing failure rate: %.1f%%. %d failed out of %d events.", failureRate*100, failedRoutings, len(resp.Events))
		recommendations = []string{
			"Check edge node availability",
			"Verify stream health on origin",
			"Review CDN configuration",
		}
	} else if avgDistance > 5000 {
		status = "warning"
		analysis = fmt.Sprintf("High average routing distance: %.0f km. Consider adding edge nodes closer to viewers.", avgDistance)
		recommendations = []string{
			"Deploy edge nodes in viewer-heavy regions",
			"Review geographic distribution of audience",
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"total_routing_events":    len(resp.Events),
			"unique_nodes":            len(nodeUsage),
			"unique_countries":        len(countryDistribution),
			"avg_routing_distance_km": avgDistance,
			"failed_routings":         failedRoutings,
			"time_range":              timeRangeLabel,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

func handleGetStreamHealth(ctx context.Context, args GetStreamHealthInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	timeRangeLabel, timeRange := normalizeTimeRange(args.TimeRange)
	resp, err := serviceClients.Periscope.GetStreamHealth5m(ctx, tenantID, streamID, timeRange, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get stream health")
		return toolError(fmt.Sprintf("Failed to fetch stream health: %v", err))
	}

	if len(resp.Records) == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]interface{}{
				"time_range": timeRangeLabel,
			},
			Analysis: "No health data available for this stream.",
		}
		return toolSuccessJSON(result)
	}

	var totalBitrate int64
	var totalFPS float32
	var totalIssues int32
	qualityTiers := make(map[string]int)

	for _, record := range resp.Records {
		totalBitrate += int64(record.AvgBitrate)
		totalFPS += record.AvgFps
		totalIssues += record.IssueCount
		if record.QualityTier != "" {
			qualityTiers[record.QualityTier]++
		}
	}

	avgBitrate := totalBitrate / int64(len(resp.Records))
	avgFPS := totalFPS / float32(len(resp.Records))

	status := "healthy"
	analysis := fmt.Sprintf("Stream health is good. Avg bitrate: %d kbps, avg FPS: %.1f.", avgBitrate/1000, avgFPS)
	var recommendations []string

	if totalIssues > int32(len(resp.Records)) {
		status = "warning"
		analysis = fmt.Sprintf("Stream has %d issues over the time range. Avg bitrate: %d kbps, avg FPS: %.1f.", totalIssues, avgBitrate/1000, avgFPS)
		recommendations = []string{
			"Review sample issues in stream health dashboard",
			"Check encoder stability",
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"avg_bitrate_kbps": avgBitrate / 1000,
			"avg_fps":          avgFPS,
			"total_issues":     totalIssues,
			"quality_tiers":    qualityTiers,
			"sample_count":     len(resp.Records),
			"time_range":       timeRangeLabel,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

func handleGetAnomalyReport(ctx context.Context, args GetAnomalyReportInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return toolError("Authentication required")
	}

	if args.StreamID == "" {
		return toolError("stream_id is required")
	}
	streamID, err := decodeStreamID(args.StreamID)
	if err != nil {
		return toolError(err.Error())
	}

	// Get recent data (last hour) and baseline data (last 24h)
	_, recentRange := normalizeTimeRange("last_1h")
	_, baselineRange := normalizeTimeRange("last_24h")

	recentHealth, err := serviceClients.Periscope.GetStreamHealth5m(ctx, tenantID, streamID, recentRange, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get recent health")
		return toolError(fmt.Sprintf("Failed to fetch recent health: %v", err))
	}

	baselineHealth, err := serviceClients.Periscope.GetStreamHealth5m(ctx, tenantID, streamID, baselineRange, nil)
	if err != nil {
		logger.WithError(err).Warn("Failed to get baseline health")
		return toolError(fmt.Sprintf("Failed to fetch baseline health: %v", err))
	}

	if len(recentHealth.Records) == 0 || len(baselineHealth.Records) == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]interface{}{
				"recent_samples":   len(recentHealth.Records),
				"baseline_samples": len(baselineHealth.Records),
			},
			Analysis: "Insufficient data to detect anomalies. Need more stream history.",
		}
		return toolSuccessJSON(result)
	}

	// Calculate baseline metrics
	var baselineBitrate int64
	var baselineIssues int32
	for _, r := range baselineHealth.Records {
		baselineBitrate += int64(r.AvgBitrate)
		baselineIssues += r.IssueCount
	}
	baselineBitrate /= int64(len(baselineHealth.Records))
	baselineIssueRate := float64(baselineIssues) / float64(len(baselineHealth.Records))

	// Calculate recent metrics
	var recentBitrate int64
	var recentIssues int32
	for _, r := range recentHealth.Records {
		recentBitrate += int64(r.AvgBitrate)
		recentIssues += r.IssueCount
	}
	recentBitrate /= int64(len(recentHealth.Records))
	recentIssueRate := float64(recentIssues) / float64(len(recentHealth.Records))

	// Detect anomalies based on sensitivity
	if args.Sensitivity == "" {
		args.Sensitivity = "medium"
	}
	threshold := 0.25 // medium sensitivity
	switch args.Sensitivity {
	case "low":
		threshold = 0.5
	case "high":
		threshold = 0.1
	}

	var anomalies []string
	bitrateChange := float64(recentBitrate-baselineBitrate) / float64(baselineBitrate)
	if bitrateChange < -threshold {
		anomalies = append(anomalies, fmt.Sprintf("Bitrate dropped %.0f%% from baseline", -bitrateChange*100))
	}
	if bitrateChange > threshold*2 {
		anomalies = append(anomalies, fmt.Sprintf("Bitrate spiked %.0f%% above baseline", bitrateChange*100))
	}

	issueChange := recentIssueRate - baselineIssueRate
	if issueChange > threshold && baselineIssueRate > 0 {
		anomalies = append(anomalies, fmt.Sprintf("Issue rate increased by %.1fx", recentIssueRate/baselineIssueRate))
	}

	status := "healthy"
	analysis := "No anomalies detected. Stream performance is within normal range."
	var recommendations []string

	if len(anomalies) > 0 {
		if len(anomalies) >= 2 {
			status = "critical"
		} else {
			status = "warning"
		}
		analysis = fmt.Sprintf("Detected %d anomalies: %v", len(anomalies), anomalies)
		recommendations = []string{
			"Check encoder settings for recent changes",
			"Review network conditions",
			"Compare with stream events for correlation",
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]interface{}{
			"baseline_bitrate_kbps":  baselineBitrate / 1000,
			"recent_bitrate_kbps":    recentBitrate / 1000,
			"bitrate_change_percent": bitrateChange * 100,
			"baseline_issue_rate":    baselineIssueRate,
			"recent_issue_rate":      recentIssueRate,
			"anomalies_detected":     len(anomalies),
			"sensitivity":            args.Sensitivity,
		},
		Analysis:        analysis,
		Recommendations: recommendations,
	}

	return toolSuccessJSON(result)
}

// toolSuccessJSON returns a success result with JSON-formatted content
func toolSuccessJSON(result interface{}) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("Failed to format result: %v", err))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, result, nil
}
