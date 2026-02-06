package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"frameworks/api_skipper/internal/knowledge"
	"frameworks/api_skipper/internal/metering"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/globalid"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/tenants"
)

const defaultMaxToolRounds = 5

type OrchestratorConfig struct {
	LLMProvider llm.Provider
	Logger      logging.Logger
	SearchWeb   *SearchWebTool
	Knowledge   *knowledge.Store
	Embedder    *knowledge.Embedder
	Periscope   *periscope.GRPCClient
	MaxRounds   int
}

type Orchestrator struct {
	llmProvider llm.Provider
	logger      logging.Logger
	searchWeb   *SearchWebTool
	knowledge   *knowledge.Store
	embedder    *knowledge.Embedder
	periscope   *periscope.GRPCClient
	tools       []llm.Tool
	maxRounds   int
}

type ToolDetail struct {
	Title   string `json:"title"`
	Payload any    `json:"payload"`
}

type ToolCallRecord struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Error     string          `json:"error,omitempty"`
}

type OrchestratorResult struct {
	Content     string
	Confidence  Confidence
	Sources     []Source
	ToolCalls   []ToolCallRecord
	Details     []ToolDetail
	TokenCounts TokenCounts
}

type TokenStreamer interface {
	SendToken(token string) error
}

type ToolOutcome struct {
	Content string
	Sources []Source
	Detail  ToolDetail
}

func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	tools := make([]llm.Tool, 0, len(ToolDefinitions))
	for _, tool := range ToolDefinitions {
		tools = append(tools, llm.Tool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}
	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = defaultMaxToolRounds
	}
	return &Orchestrator{
		llmProvider: cfg.LLMProvider,
		logger:      cfg.Logger,
		searchWeb:   cfg.SearchWeb,
		knowledge:   cfg.Knowledge,
		embedder:    cfg.Embedder,
		periscope:   cfg.Periscope,
		tools:       tools,
		maxRounds:   maxRounds,
	}
}

func (o *Orchestrator) Run(ctx context.Context, messages []llm.Message, streamer TokenStreamer) (OrchestratorResult, error) {
	if o == nil || o.llmProvider == nil {
		return OrchestratorResult{}, errors.New("llm provider is required")
	}

	var response strings.Builder
	var sources []Source
	var toolCalls []ToolCallRecord
	var details []ToolDetail
	inputTokens := 0
	filter := newConfidenceStreamFilter(streamer)

	for round := 0; round < o.maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return OrchestratorResult{}, err
		}

		inputTokens += countTokensInMessages(messages)
		stream, err := o.llmProvider.Complete(ctx, messages, o.tools)
		if err != nil {
			return OrchestratorResult{}, err
		}

		var pendingToolCalls []llm.ToolCall
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				_ = stream.Close()
				return OrchestratorResult{}, err
			}
			if chunk.Content != "" {
				response.WriteString(chunk.Content)
				if filterErr := filter.Write(chunk.Content); filterErr != nil {
					_ = stream.Close()
					return OrchestratorResult{}, filterErr
				}
			}
			if len(chunk.ToolCalls) > 0 {
				pendingToolCalls = mergeToolCalls(pendingToolCalls, chunk.ToolCalls)
			}
		}
		_ = stream.Close()
		if err := filter.Flush(); err != nil {
			return OrchestratorResult{}, err
		}

		if len(pendingToolCalls) == 0 {
			break
		}

		for _, call := range pendingToolCalls {
			outcome, err := o.executeTool(ctx, call)
			record := ToolCallRecord{Name: call.Name}
			if call.Arguments != "" {
				record.Arguments = json.RawMessage(call.Arguments)
			}
			if err != nil {
				if o.logger != nil {
					o.logger.WithError(err).WithField("tool", call.Name).Warn("Skipper tool execution failed")
				}
				record.Error = err.Error()
				outcome = ToolOutcome{
					Content: fmt.Sprintf("Tool %s failed: %v", call.Name, err),
					Detail: ToolDetail{
						Title:   fmt.Sprintf("Tool call: %s", call.Name),
						Payload: map[string]any{"error": err.Error()},
					},
				}
			}
			toolCalls = append(toolCalls, record)
			if outcome.Content != "" {
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    outcome.Content,
					Name:       call.Name,
					ToolCallID: call.ID,
				})
			}
			if len(outcome.Sources) > 0 {
				sources = appendSources(sources, outcome.Sources)
			}
			if outcome.Detail.Title != "" {
				details = append(details, outcome.Detail)
			}
		}

		if round == o.maxRounds-1 {
			response.WriteString("\n\n[confidence:unknown]\nReached maximum tool iterations before producing a final answer.\n[sources]\n[/sources]\n")
		}
	}

	blocks := parseConfidenceBlocks(response.String())
	content := joinConfidenceContent(blocks)
	confidence := summarizeConfidence(blocks)
	blockSources := sourcesFromBlocks(blocks)
	sources = appendSources(sources, blockSources)
	if confidence == "" {
		confidence = ConfidenceUnknown
	}

	return OrchestratorResult{
		Content:    content,
		Confidence: confidence,
		Sources:    sources,
		ToolCalls:  toolCalls,
		Details:    details,
		TokenCounts: TokenCounts{
			Input:  inputTokens,
			Output: estimateTokens(content),
		},
	}, nil
}

func (o *Orchestrator) executeTool(ctx context.Context, call llm.ToolCall) (ToolOutcome, error) {
	switch call.Name {
	case "search_knowledge":
		return o.searchKnowledge(ctx, call.Arguments)
	case "search_web":
		return o.searchWebTool(ctx, call.Arguments)
	case "diagnose_rebuffering":
		return o.diagnoseRebuffering(ctx, call.Arguments)
	case "diagnose_buffer_health":
		return o.diagnoseBufferHealth(ctx, call.Arguments)
	case "diagnose_packet_loss":
		return o.diagnosePacketLoss(ctx, call.Arguments)
	case "diagnose_routing":
		return o.diagnoseRouting(ctx, call.Arguments)
	case "get_stream_health_summary":
		return o.getStreamHealthSummary(ctx, call.Arguments)
	case "get_anomaly_report":
		return o.getAnomalyReport(ctx, call.Arguments)
	default:
		return ToolOutcome{}, fmt.Errorf("unknown tool %q", call.Name)
	}
}

type SearchKnowledgeInput struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit,omitempty"`
	TenantScope string `json:"tenant_scope,omitempty"`
}

type SearchKnowledgeResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Snippet    string  `json:"snippet,omitempty"`
	Similarity float64 `json:"similarity,omitempty"`
}

type SearchKnowledgeResponse struct {
	Query   string                  `json:"query"`
	Context string                  `json:"context"`
	Results []SearchKnowledgeResult `json:"results"`
	Sources []Source                `json:"sources"`
}

func (o *Orchestrator) searchKnowledge(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.knowledge == nil || o.embedder == nil {
		return ToolOutcome{}, errors.New("knowledge search unavailable")
	}
	var input SearchKnowledgeInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return ToolOutcome{}, fmt.Errorf("parse search_knowledge arguments: %w", err)
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return ToolOutcome{}, errors.New("query is required")
	}

	limit := input.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}

	tenantIDs := resolveKnowledgeTenants(ctx, input.TenantScope)
	if len(tenantIDs) == 0 {
		return ToolOutcome{}, errors.New("tenant id is required")
	}

	embedding, err := o.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return ToolOutcome{}, err
	}
	metering.RecordEmbedding(ctx)
	metering.RecordSearchQuery(ctx)

	var chunks []knowledge.Chunk
	for _, tenantID := range tenantIDs {
		results, err := o.knowledge.Search(ctx, tenantID, embedding, limit)
		if err != nil {
			return ToolOutcome{}, err
		}
		chunks = append(chunks, results...)
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Similarity > chunks[j].Similarity
	})
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}

	response := mapKnowledgeResponse(query, chunks)
	return ToolOutcome{
		Content: response.Context,
		Sources: response.Sources,
		Detail: ToolDetail{
			Title:   "Tool call: search_knowledge",
			Payload: response,
		},
	}, nil
}

func resolveKnowledgeTenants(ctx context.Context, scope string) []string {
	tenantID := ctxkeys.GetTenantID(ctx)
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "global":
		return []string{tenants.SystemTenantID.String()}
	case "all":
		if tenantID == "" {
			return []string{tenants.SystemTenantID.String()}
		}
		return []string{
			tenantID,
			tenants.SystemTenantID.String(),
		}
	default:
		if tenantID == "" {
			return nil
		}
		return []string{tenantID}
	}
}

func mapKnowledgeResponse(query string, chunks []knowledge.Chunk) SearchKnowledgeResponse {
	results := make([]SearchKnowledgeResult, 0, len(chunks))
	sources := make([]Source, 0, len(chunks))
	for _, chunk := range chunks {
		title := strings.TrimSpace(chunk.SourceTitle)
		if title == "" {
			title = chunk.SourceURL
		}
		snippet := snippetFromContent(chunk.Text)
		results = append(results, SearchKnowledgeResult{
			Title:      title,
			URL:        chunk.SourceURL,
			Snippet:    snippet,
			Similarity: chunk.Similarity,
		})
		sources = append(sources, Source{
			Title: title,
			URL:   chunk.SourceURL,
			Type:  SourceTypeKnowledgeBase,
		})
	}
	return SearchKnowledgeResponse{
		Query:   query,
		Context: formatKnowledgeContext(results),
		Results: results,
		Sources: sources,
	}
}

func formatKnowledgeContext(results []SearchKnowledgeResult) string {
	if len(results) == 0 {
		return "No knowledge base results found."
	}
	var builder strings.Builder
	builder.WriteString("Knowledge base results:\n")
	for i, result := range results {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, result.Title)
		if result.URL != "" {
			fmt.Fprintf(&builder, "URL: %s\n", result.URL)
		}
		if result.Snippet != "" {
			fmt.Fprintf(&builder, "Snippet: %s\n", result.Snippet)
		}
		if i < len(results)-1 {
			builder.WriteString("\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func (o *Orchestrator) searchWebTool(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.searchWeb == nil {
		return ToolOutcome{}, errors.New("search provider unavailable")
	}
	response, err := o.searchWeb.Call(ctx, arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	return ToolOutcome{
		Content: response.Context,
		Sources: response.Sources,
		Detail: ToolDetail{
			Title:   "Tool call: search_web",
			Payload: response,
		},
	}, nil
}

type diagnosticInput struct {
	StreamID    string `json:"stream_id"`
	TimeRange   string `json:"time_range,omitempty"`
	Sensitivity string `json:"sensitivity,omitempty"`
}

type DiagnosticResult struct {
	Status          string         `json:"status"`
	Metrics         map[string]any `json:"metrics"`
	Analysis        string         `json:"analysis"`
	Recommendations []string       `json:"recommendations,omitempty"`
}

func (o *Orchestrator) diagnoseRebuffering(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}

	timeRangeLabel, timeRange := normalizeTimeRange(input.TimeRange)
	resp, err := o.periscope.GetRebufferingEvents(ctx, tenantID, &streamID, nil, timeRange, &periscope.CursorPaginationOpts{First: 200})
	if err != nil {
		return ToolOutcome{}, err
	}

	rebufferCount := len(resp.Events)
	var totalDurationMs int64
	for _, evt := range resp.Events {
		if evt.RebufferStart != nil && evt.RebufferEnd != nil {
			totalDurationMs += evt.RebufferEnd.AsTime().Sub(evt.RebufferStart.AsTime()).Milliseconds()
		}
	}
	avgDurationMs := int64(0)
	if rebufferCount > 0 {
		avgDurationMs = totalDurationMs / int64(rebufferCount)
	}

	status := "healthy"
	analysis := "No rebuffering detected in the time range."
	if rebufferCount > 0 {
		status = "warning"
		analysis = fmt.Sprintf("Detected %d rebuffer events with an average duration of %dms.", rebufferCount, avgDurationMs)
		if rebufferCount > 20 || avgDurationMs > 3000 {
			status = "critical"
			analysis = fmt.Sprintf("Critical rebuffering detected. %d events with avg duration %dms.", rebufferCount, avgDurationMs)
		}
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]any{
			"rebuffer_count":           rebufferCount,
			"avg_rebuffer_duration_ms": avgDurationMs,
			"total_rebuffer_time_ms":   totalDurationMs,
			"time_range":               timeRangeLabel,
		},
		Analysis: analysis,
	}
	return diagnosticOutcome("diagnose_rebuffering", result)
}

func (o *Orchestrator) diagnoseBufferHealth(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}
	timeRangeLabel, timeRange := normalizeTimeRange(input.TimeRange)
	rollupResp, err := o.periscope.GetStreamHealth5m(ctx, tenantID, streamID, timeRange, nil)
	if err != nil {
		return ToolOutcome{}, err
	}
	if len(rollupResp.Records) == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]any{
				"time_range": timeRangeLabel,
			},
			Analysis: "No buffer health data available for this stream in the specified time range.",
		}
		return diagnosticOutcome("diagnose_buffer_health", result)
	}
	var totalBufferHealth float32
	var totalDryCount int32
	var minBufferHealth float32 = 1.0
	for _, record := range rollupResp.Records {
		totalBufferHealth += record.AvgBufferHealth
		totalDryCount += record.BufferDryCount
		if record.AvgBufferHealth < minBufferHealth {
			minBufferHealth = record.AvgBufferHealth
		}
	}
	avgBufferHealth := totalBufferHealth / float32(len(rollupResp.Records))

	status := "healthy"
	analysis := fmt.Sprintf("Average buffer health %.1f%% with %d dry events.", avgBufferHealth*100, totalDryCount)
	if avgBufferHealth < 0.6 || totalDryCount > 3 {
		status = "warning"
		analysis = fmt.Sprintf("Buffer health degraded. Avg %.1f%% with %d dry events.", avgBufferHealth*100, totalDryCount)
	}
	if avgBufferHealth < 0.3 || totalDryCount > 10 {
		status = "critical"
		analysis = fmt.Sprintf("Critical buffer issues. Min health %.1f%% with %d dry events.", minBufferHealth*100, totalDryCount)
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]any{
			"avg_buffer_health": avgBufferHealth,
			"min_buffer_health": minBufferHealth,
			"buffer_dry_count":  totalDryCount,
			"time_range":        timeRangeLabel,
		},
		Analysis: analysis,
	}
	return diagnosticOutcome("diagnose_buffer_health", result)
}

func (o *Orchestrator) diagnosePacketLoss(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}
	timeRangeLabel, timeRange := normalizeTimeRange(input.TimeRange)
	clientResp, err := o.periscope.GetClientMetrics5m(ctx, tenantID, &streamID, nil, timeRange, &periscope.CursorPaginationOpts{First: 200})
	if err != nil {
		return ToolOutcome{}, err
	}

	var lossSum float64
	var maxLoss float64
	lossSamples := 0
	for _, record := range clientResp.Records {
		if record.PacketLossRate != nil {
			v := float64(*record.PacketLossRate)
			lossSum += v
			lossSamples++
			if v > maxLoss {
				maxLoss = v
			}
		}
	}
	if lossSamples == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]any{
				"time_range": timeRangeLabel,
			},
			Analysis: "No packet loss samples available for this stream in the specified time range.",
		}
		return diagnosticOutcome("diagnose_packet_loss", result)
	}
	avgLoss := lossSum / float64(lossSamples)

	status := packetLossStatus(protocolTypeUnknown, avgLoss)
	analysis := fmt.Sprintf("Average packet loss %.2f%% with max %.2f%%.", avgLoss*100, maxLoss*100)
	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]any{
			"avg_loss_percent": avgLoss * 100,
			"max_loss_percent": maxLoss * 100,
			"samples":          lossSamples,
			"time_range":       timeRangeLabel,
		},
		Analysis: analysis,
	}
	return diagnosticOutcome("diagnose_packet_loss", result)
}

func (o *Orchestrator) diagnoseRouting(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}
	timeRangeLabel, timeRange := normalizeTimeRange(input.TimeRange)
	resp, err := o.periscope.GetRoutingEvents(ctx, tenantID, &streamID, timeRange, nil, []string{tenantID}, nil, nil)
	if err != nil {
		return ToolOutcome{}, err
	}

	analysis := fmt.Sprintf("Routing events: %d in %s.", len(resp.Events), timeRangeLabel)
	if len(resp.Events) == 0 {
		analysis = "No routing events available for this stream."
	}
	result := DiagnosticResult{
		Status: "healthy",
		Metrics: map[string]any{
			"routing_events": len(resp.Events),
			"time_range":     timeRangeLabel,
		},
		Analysis: analysis,
	}
	return diagnosticOutcome("diagnose_routing", result)
}

func (o *Orchestrator) getStreamHealthSummary(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}
	timeRangeLabel, timeRange := normalizeTimeRange(input.TimeRange)
	resp, err := o.periscope.GetStreamHealthSummary(ctx, tenantID, &streamID, timeRange)
	if err != nil {
		return ToolOutcome{}, err
	}
	summary := resp.GetSummary()
	if summary == nil {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]any{
				"time_range": timeRangeLabel,
			},
			Analysis: "No stream health summary available for this stream.",
		}
		return diagnosticOutcome("get_stream_health_summary", result)
	}
	result := DiagnosticResult{
		Status: "healthy",
		Metrics: map[string]any{
			"issue_count": summary.TotalIssueCount,
			"avg_bitrate": summary.AvgBitrate,
			"avg_fps":     summary.AvgFps,
			"time_range":  timeRangeLabel,
		},
		Analysis: "Stream health summary retrieved.",
	}
	return diagnosticOutcome("get_stream_health_summary", result)
}

func (o *Orchestrator) getAnomalyReport(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.periscope == nil {
		return ToolOutcome{}, errors.New("diagnostics unavailable")
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return ToolOutcome{}, errors.New("tenant_id is required")
	}
	input, err := parseDiagnosticInput(arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	streamID, err := decodeStreamID(input.StreamID)
	if err != nil {
		return ToolOutcome{}, err
	}

	_, recentRange := normalizeTimeRange("last_1h")
	_, baselineRange := normalizeTimeRange("last_24h")
	recentHealth, err := o.periscope.GetStreamHealth5m(ctx, tenantID, streamID, recentRange, nil)
	if err != nil {
		return ToolOutcome{}, err
	}
	baselineHealth, err := o.periscope.GetStreamHealth5m(ctx, tenantID, streamID, baselineRange, nil)
	if err != nil {
		return ToolOutcome{}, err
	}

	if len(recentHealth.Records) == 0 || len(baselineHealth.Records) == 0 {
		result := DiagnosticResult{
			Status: "no_data",
			Metrics: map[string]any{
				"recent_samples":   len(recentHealth.Records),
				"baseline_samples": len(baselineHealth.Records),
			},
			Analysis: "Insufficient data to detect anomalies.",
		}
		return diagnosticOutcome("get_anomaly_report", result)
	}

	var baselineBitrate int64
	var baselineIssues int32
	for _, r := range baselineHealth.Records {
		baselineBitrate += int64(r.AvgBitrate)
		baselineIssues += r.IssueCount
	}
	baselineBitrate /= int64(len(baselineHealth.Records))
	baselineIssueRate := float64(baselineIssues) / float64(len(baselineHealth.Records))

	var recentBitrate int64
	var recentIssues int32
	for _, r := range recentHealth.Records {
		recentBitrate += int64(r.AvgBitrate)
		recentIssues += r.IssueCount
	}
	recentBitrate /= int64(len(recentHealth.Records))
	recentIssueRate := float64(recentIssues) / float64(len(recentHealth.Records))

	threshold := 0.25
	switch input.Sensitivity {
	case "low":
		threshold = 0.5
	case "high":
		threshold = 0.1
	}

	bitrateChange := float64(recentBitrate-baselineBitrate) / float64(baselineBitrate)
	issueChange := recentIssueRate - baselineIssueRate
	anomalies := []string{}
	if bitrateChange < -threshold {
		anomalies = append(anomalies, fmt.Sprintf("Bitrate dropped %.0f%% from baseline", -bitrateChange*100))
	}
	if issueChange > threshold && baselineIssueRate > 0 {
		anomalies = append(anomalies, fmt.Sprintf("Issue rate increased by %.1fx", recentIssueRate/baselineIssueRate))
	}

	status := "healthy"
	analysis := "No anomalies detected."
	if len(anomalies) > 0 {
		status = "warning"
		analysis = fmt.Sprintf("Detected %d anomalies: %v", len(anomalies), anomalies)
	}

	result := DiagnosticResult{
		Status: status,
		Metrics: map[string]any{
			"baseline_bitrate_kbps":  baselineBitrate / 1000,
			"recent_bitrate_kbps":    recentBitrate / 1000,
			"bitrate_change_percent": bitrateChange * 100,
			"baseline_issue_rate":    baselineIssueRate,
			"recent_issue_rate":      recentIssueRate,
			"anomalies_detected":     len(anomalies),
			"sensitivity":            input.Sensitivity,
		},
		Analysis: analysis,
	}
	return diagnosticOutcome("get_anomaly_report", result)
}

func parseDiagnosticInput(arguments string) (diagnosticInput, error) {
	var input diagnosticInput
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return diagnosticInput{}, fmt.Errorf("parse diagnostic arguments: %w", err)
	}
	input.StreamID = strings.TrimSpace(input.StreamID)
	if input.StreamID == "" {
		return diagnosticInput{}, errors.New("stream_id is required")
	}
	input.TimeRange = strings.TrimSpace(input.TimeRange)
	input.Sensitivity = strings.TrimSpace(input.Sensitivity)
	if input.Sensitivity == "" {
		input.Sensitivity = "medium"
	}
	return input, nil
}

func diagnosticOutcome(name string, result DiagnosticResult) (ToolOutcome, error) {
	payload, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return ToolOutcome{}, fmt.Errorf("format diagnostic result: %w", err)
	}
	return ToolOutcome{
		Content: string(payload),
		Detail: ToolDetail{
			Title:   fmt.Sprintf("Tool call: %s", name),
			Payload: result,
		},
	}, nil
}

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
	default:
		start = now.Add(-1 * time.Hour)
	}
	return tr, &periscope.TimeRangeOpts{StartTime: start, EndTime: now}
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

func decodeStreamID(input string) (string, error) {
	if input == "" {
		return "", errors.New("stream_id is required")
	}
	if typ, id, ok := globalid.Decode(input); ok {
		if typ != globalid.TypeStream {
			return "", fmt.Errorf("invalid stream relay ID type: %s", typ)
		}
		return id, nil
	}
	return input, nil
}

type confidenceStreamFilter struct {
	streamer  TokenStreamer
	pending   string
	inSources bool
}

func newConfidenceStreamFilter(streamer TokenStreamer) *confidenceStreamFilter {
	return &confidenceStreamFilter{streamer: streamer}
}

func (f *confidenceStreamFilter) Write(chunk string) error {
	if f.streamer == nil || chunk == "" {
		return nil
	}
	f.pending += chunk
	lines := strings.Split(f.pending, "\n")
	f.pending = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if err := f.processLine(line, true); err != nil {
			return err
		}
	}
	return nil
}

func (f *confidenceStreamFilter) Flush() error {
	if f.pending == "" {
		return nil
	}
	line := f.pending
	f.pending = ""
	return f.processLine(line, false)
}

func (f *confidenceStreamFilter) processLine(line string, addNewline bool) error {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "[confidence:"):
		return nil
	case trimmed == "[sources]":
		f.inSources = true
		return nil
	case trimmed == "[/sources]":
		f.inSources = false
		return nil
	}
	if f.inSources {
		return nil
	}
	output := line
	if addNewline {
		output += "\n"
	}
	if strings.TrimSpace(output) == "" {
		if addNewline {
			return f.streamer.SendToken("\n")
		}
		return nil
	}
	return f.streamer.SendToken(output)
}

func parseConfidenceBlocks(input string) []ConfidenceBlock {
	var blocks []ConfidenceBlock
	remaining := input
	for {
		start := strings.Index(remaining, "[confidence:")
		if start == -1 {
			break
		}
		endTag := strings.Index(remaining[start:], "]")
		if endTag == -1 {
			break
		}
		tag := remaining[start+len("[confidence:") : start+endTag]
		afterTag := remaining[start+endTag+1:]
		sourcesStart := strings.Index(afterTag, "[sources]")
		if sourcesStart == -1 {
			break
		}
		content := strings.TrimSpace(afterTag[:sourcesStart])
		afterSources := afterTag[sourcesStart+len("[sources]"):]
		sourcesEnd := strings.Index(afterSources, "[/sources]")
		if sourcesEnd == -1 {
			break
		}
		sourcesBlock := afterSources[:sourcesEnd]
		blocks = append(blocks, ConfidenceBlock{
			Content:    content,
			Confidence: Confidence(strings.TrimSpace(tag)),
			Sources:    parseSourcesBlock(sourcesBlock),
		})
		remaining = afterSources[sourcesEnd+len("[/sources]"):]
	}
	if len(blocks) == 0 {
		trimmed := strings.TrimSpace(input)
		if trimmed != "" {
			blocks = append(blocks, ConfidenceBlock{
				Content:    trimmed,
				Confidence: ConfidenceUnknown,
			})
		}
	}
	return blocks
}

func parseSourcesBlock(block string) []Source {
	lines := strings.Split(block, "\n")
	var sources []Source
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line == "" {
			continue
		}
		title, url := splitSourceLine(line)
		sources = append(sources, Source{
			Title: title,
			URL:   url,
			Type:  SourceTypeUnknown,
		})
	}
	return sources
}

func splitSourceLine(line string) (string, string) {
	parts := strings.SplitN(line, "—", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	parts = strings.SplitN(line, " - ", 2)
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return line, ""
}

func joinConfidenceContent(blocks []ConfidenceBlock) string {
	var sections []string
	for _, block := range blocks {
		if strings.TrimSpace(block.Content) == "" {
			continue
		}
		sections = append(sections, strings.TrimSpace(block.Content))
	}
	return strings.TrimSpace(strings.Join(sections, "\n\n"))
}

func summarizeConfidence(blocks []ConfidenceBlock) Confidence {
	if len(blocks) == 0 {
		return ConfidenceUnknown
	}
	rank := map[Confidence]int{
		ConfidenceUnknown:   0,
		ConfidenceBestGuess: 1,
		ConfidenceSourced:   2,
		ConfidenceVerified:  3,
	}
	best := ConfidenceUnknown
	for _, block := range blocks {
		if rank[block.Confidence] > rank[best] {
			best = block.Confidence
		}
	}
	return best
}

func sourcesFromBlocks(blocks []ConfidenceBlock) []Source {
	var sources []Source
	for _, block := range blocks {
		sources = append(sources, block.Sources...)
	}
	return sources
}

func appendSources(existing []Source, incoming []Source) []Source {
	seen := make(map[string]struct{}, len(existing))
	for _, source := range existing {
		key := strings.TrimSpace(source.URL) + "|" + strings.TrimSpace(source.Title)
		seen[key] = struct{}{}
	}
	for _, source := range incoming {
		key := strings.TrimSpace(source.URL) + "|" + strings.TrimSpace(source.Title)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, source)
	}
	return existing
}

func estimateTokens(text string) int {
	return len(strings.Fields(text))
}

func countTokensInMessages(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		total += estimateTokens(msg.Content)
	}
	return total
}

// mergeToolCalls accumulates tool calls across streaming chunks. If a chunk
// carries a call with the same ID as one already seen, its arguments are
// appended (LLMs may split a single call across frames). New IDs are appended.
func mergeToolCalls(existing, incoming []llm.ToolCall) []llm.ToolCall {
	for _, inc := range incoming {
		found := false
		for i, ex := range existing {
			if ex.ID != "" && ex.ID == inc.ID {
				existing[i].Arguments += inc.Arguments
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, inc)
		}
	}
	return existing
}
