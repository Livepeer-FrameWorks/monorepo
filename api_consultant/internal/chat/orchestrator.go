package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"frameworks/api_consultant/internal/diagnostics"
	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
)

const defaultMaxToolRounds = 6

// GatewayToolCaller invokes tools on the Gateway MCP server.
type GatewayToolCaller interface {
	AvailableTools() []llm.Tool
	HasTool(name string) bool
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

type OrchestratorConfig struct {
	LLMProvider     llm.Provider
	LLMProviderName string
	LLMModelName    string
	Logger          logging.Logger
	SearchWeb       *SearchWebTool
	Knowledge       KnowledgeSearcher
	Embedder        KnowledgeEmbedder
	Reranker        *knowledge.Reranker
	QueryRewriter   *QueryRewriter
	HyDE            *HyDEGenerator
	Gateway         GatewayToolCaller
	Diagnostics     *diagnostics.BaselineEvaluator
	MaxRounds       int
	SearchLimit     int
	GlobalTenantID  string
}

const defaultGlobalTenantID = "00000000-0000-0000-0000-000000000001"

type Orchestrator struct {
	llmProvider     llm.Provider
	llmProviderName string
	llmModelName    string
	logger          logging.Logger
	searchWeb       *SearchWebTool
	knowledge       KnowledgeSearcher
	embedder        KnowledgeEmbedder
	reranker        *knowledge.Reranker
	queryRewriter   *QueryRewriter
	hyde            *HyDEGenerator
	gateway         GatewayToolCaller
	diag            *diagnostics.BaselineEvaluator
	tools           []llm.Tool
	maxRounds       int
	searchLimit     int
	globalTenantID  string
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
	Blocks      []ConfidenceBlock
	ToolCalls   []ToolCallRecord
	Details     []ToolDetail
	TokenCounts TokenCounts
}

type TokenStreamer interface {
	SendToken(token string) error
}

// ToolEventStreamer is an optional extension of TokenStreamer that allows the
// orchestrator to emit tool lifecycle events during streaming. Implementations
// that also satisfy this interface will receive tool_start/tool_end calls.
type ToolEventStreamer interface {
	SendToolStart(toolName string) error
	SendToolEnd(toolName string, errMsg string) error
}

type ToolOutcome struct {
	Content string
	Sources []Source
	Detail  ToolDetail
}

type KnowledgeSearcher interface {
	Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error)
	HybridSearch(ctx context.Context, tenantID string, embedding []float32, query string, limit int) ([]knowledge.Chunk, error)
}

type KnowledgeEmbedder interface {
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	// Start with local tool definitions (search_knowledge, search_web).
	tools := make([]llm.Tool, 0, len(ToolDefinitions)+10)
	for _, tool := range ToolDefinitions {
		tools = append(tools, llm.Tool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}
	// Append Gateway MCP tools (diagnostics, streams, billing, etc.).
	if cfg.Gateway != nil {
		tools = append(tools, cfg.Gateway.AvailableTools()...)
	}
	maxRounds := cfg.MaxRounds
	if maxRounds <= 0 {
		maxRounds = defaultMaxToolRounds
	}
	searchLimit := cfg.SearchLimit
	if searchLimit <= 0 {
		searchLimit = defaultSearchLimit
	}
	globalTenantID := cfg.GlobalTenantID
	if globalTenantID == "" {
		globalTenantID = defaultGlobalTenantID
	}
	return &Orchestrator{
		llmProvider:     cfg.LLMProvider,
		llmProviderName: cfg.LLMProviderName,
		llmModelName:    cfg.LLMModelName,
		logger:          cfg.Logger,
		searchWeb:       cfg.SearchWeb,
		knowledge:       cfg.Knowledge,
		embedder:        cfg.Embedder,
		reranker:        cfg.Reranker,
		queryRewriter:   cfg.QueryRewriter,
		hyde:            cfg.HyDE,
		gateway:         cfg.Gateway,
		diag:            cfg.Diagnostics,
		tools:           tools,
		maxRounds:       maxRounds,
		searchLimit:     searchLimit,
		globalTenantID:  globalTenantID,
	}
}

func (o *Orchestrator) Run(ctx context.Context, messages []llm.Message, streamer TokenStreamer) (OrchestratorResult, error) {
	if o == nil || o.llmProvider == nil {
		return OrchestratorResult{}, errors.New("llm provider is required")
	}

	var response strings.Builder
	var fullResponse strings.Builder
	var assistantResponses []string
	var sources []Source
	var toolCalls []ToolCallRecord
	var details []ToolDetail
	inputTokens := 0
	filter := newConfidenceStreamFilter(streamer)
	lastUserMessage := lastUserContent(messages)
	isMCPInventoryQuestion := isMCPToolInventoryQuestion(lastUserMessage)
	if isMCPInventoryQuestion {
		inventory := o.buildMCPToolInventory(ctx, false)
		messages = appendTrustedSystemContext(messages, formatMCPToolInventoryContext(inventory))
		details = append(details, ToolDetail{
			Title:   "Current MCP tool inventory",
			Payload: inventory,
		})
	}

	// Pre-retrieval: auto-search knowledge base using the user's last message
	// before the first LLM call to save a round-trip.
	if !isMCPInventoryQuestion && o.knowledge != nil && o.embedder != nil && len(messages) > 0 {
		userMsg := messages[len(messages)-1]
		if userMsg.Role == "user" && strings.TrimSpace(userMsg.Content) != "" {
			preResult := o.preRetrieve(ctx, userMsg.Content)
			if preResult.Context != "" {
				contextBlock := guardUntrustedContext("Pre-retrieved knowledge context", preResult.Context, maxPreRetrieveTokens)
				if contextBlock != "" {
					// Inject context into the system message
					for i, msg := range messages {
						if msg.Role == "system" {
							messages[i].Content += "\n\n" + contextBlock
							break
						}
					}
					sources = appendSources(sources, preResult.Sources)
					messages = compactMessages(ctx, messages, maxPromptTokenBudget, o.llmProvider)
				}
			}
		}
	}

	for round := 0; round < o.maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return OrchestratorResult{}, err
		}

		if countTokensInMessages(messages) > maxPromptTokenBudget {
			messages = compactMessages(ctx, messages, maxPromptTokenBudget, o.llmProvider)
		}

		inputTokens += countTokensInMessages(messages)
		llmStart := time.Now()
		stream, err := o.llmProvider.Complete(ctx, messages, o.toolsForContext(ctx))
		if err != nil {
			llmCallsTotal.WithLabelValues(o.llmProviderName, o.llmModelName, "error").Inc()
			llmDuration.WithLabelValues(o.llmProviderName, o.llmModelName).Observe(time.Since(llmStart).Seconds())
			return OrchestratorResult{}, err
		}

		var pendingToolCalls []llm.ToolCall
		var roundResponse strings.Builder
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
				fullResponse.WriteString(chunk.Content)
				roundResponse.WriteString(chunk.Content)
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
		llmCallsTotal.WithLabelValues(o.llmProviderName, o.llmModelName, "success").Inc()
		llmDuration.WithLabelValues(o.llmProviderName, o.llmModelName).Observe(time.Since(llmStart).Seconds())
		if err := filter.Flush(); err != nil {
			return OrchestratorResult{}, err
		}
		if strings.TrimSpace(roundResponse.String()) != "" {
			assistantResponses = append(assistantResponses, roundResponse.String())
		}

		if len(pendingToolCalls) == 0 {
			break
		}

		// Append the assistant message (with tool_use blocks) so the next
		// LLM round sees the tool_use → tool_result pairing it expects.
		messages = append(messages, llm.Message{
			Role:      "assistant",
			Content:   response.String(),
			ToolCalls: pendingToolCalls,
		})
		response.Reset()

		tes, _ := streamer.(ToolEventStreamer)

		type toolResult struct {
			index   int
			record  ToolCallRecord
			outcome ToolOutcome
		}
		results := make([]toolResult, len(pendingToolCalls))
		var sseMu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 3)

		for i, call := range pendingToolCalls {
			wg.Add(1)
			go func(idx int, c llm.ToolCall) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				if tes != nil {
					sseMu.Lock()
					_ = tes.SendToolStart(c.Name)
					sseMu.Unlock()
				}
				outcome, err := o.executeTool(ctx, c)
				record := ToolCallRecord{Name: c.Name}
				if c.Arguments != "" {
					record.Arguments = json.RawMessage(c.Arguments)
				}
				errMsg := ""
				if err != nil {
					if o.logger != nil {
						o.logger.WithError(err).WithField("tool", c.Name).Warn("Skipper tool execution failed")
					}
					record.Error = err.Error()
					errMsg = err.Error()
					outcome = ToolOutcome{
						Content: fmt.Sprintf("Tool %s failed: %v", c.Name, err),
						Detail: ToolDetail{
							Title:   fmt.Sprintf("Tool call: %s", c.Name),
							Payload: map[string]any{"error": err.Error()},
						},
					}
				}
				if tes != nil {
					sseMu.Lock()
					_ = tes.SendToolEnd(c.Name, errMsg)
					sseMu.Unlock()
				}
				results[idx] = toolResult{index: idx, record: record, outcome: outcome}
			}(i, call)
		}
		wg.Wait()

		// Append in original LLM order.
		for _, r := range results {
			toolCalls = append(toolCalls, r.record)
			if r.outcome.Content != "" {
				messages = append(messages, llm.Message{
					Role:       "tool",
					Content:    r.outcome.Content,
					Name:       r.record.Name,
					ToolCallID: pendingToolCalls[r.index].ID,
				})
			}
			if len(r.outcome.Sources) > 0 {
				sources = appendSources(sources, r.outcome.Sources)
			}
			if r.outcome.Detail.Title != "" {
				details = append(details, r.outcome.Detail)
			}
		}

		if round == o.maxRounds-2 {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "[System note: You have one remaining tool round. Synthesize your answer now using the context already gathered. Do not make additional tool calls unless absolutely critical.]",
			})
		}

		if round == o.maxRounds-1 && fullResponse.Len() == 0 {
			fallback := "[confidence:unknown]\nReached maximum tool iterations before producing a final answer.\n[sources]\n[/sources]\n"
			fullResponse.WriteString(fallback)
			assistantResponses = append(assistantResponses, fallback)
		}
	}

	blocks := parseConfidenceTurns(assistantResponses)
	content := joinConfidenceContent(blocks)
	confidence := summarizeConfidence(blocks)
	blockSources := sourcesFromBlocks(blocks)
	sources = appendSources(sources, blockSources)
	if confidence == "" {
		confidence = ConfidenceUnknown
	}

	outputTokens := estimateTokens(content)
	llmTokensTotal.WithLabelValues(o.llmProviderName, o.llmModelName, "input").Add(float64(inputTokens))
	llmTokensTotal.WithLabelValues(o.llmProviderName, o.llmModelName, "output").Add(float64(outputTokens))

	return OrchestratorResult{
		Content:    content,
		Confidence: confidence,
		Sources:    sources,
		Blocks:     blocks,
		ToolCalls:  toolCalls,
		Details:    details,
		TokenCounts: TokenCounts{
			Input:  inputTokens,
			Output: outputTokens,
		},
	}, nil
}

// docsAllowedTools lists tools permitted when mode=docs.
// Read-only search, introspection, stream reads, and diagnostic tools only;
// all mutation tools blocked.
var docsAllowedTools = map[string]bool{
	"list_mcp_tools":            true,
	"search_knowledge":          true,
	"search_web":                true,
	"introspect_schema":         true,
	"generate_query":            true,
	"execute_query":             true,
	"get_stream":                true,
	"list_streams":              true,
	"get_stream_health":         true,
	"get_stream_metrics":        true,
	"check_stream_health":       true,
	"diagnose_rebuffering":      true,
	"diagnose_buffer_health":    true,
	"diagnose_packet_loss":      true,
	"diagnose_routing":          true,
	"get_stream_health_summary": true,
	"get_anomaly_report":        true,
	"search_support_history":    true,
}

// spokeMutationBlocklist lists mutation tools blocked in spoke mode.
// Spoke callers (external agents via MCP) should not trigger mutations
// through ask_consultant — those should go directly via Gateway MCP tools.
var spokeMutationBlocklist = map[string]bool{
	"create_stream":          true,
	"update_stream":          true,
	"delete_stream":          true,
	"refresh_stream_key":     true,
	"create_clip":            true,
	"delete_clip":            true,
	"start_dvr":              true,
	"stop_dvr":               true,
	"create_vod_upload":      true,
	"complete_vod_upload":    true,
	"abort_vod_upload":       true,
	"delete_vod_asset":       true,
	"topup_balance":          true,
	"submit_payment":         true,
	"update_billing_details": true,
}

// heartbeatAllowedTools lists tools permitted when mode=heartbeat.
// The heartbeat agent already has metrics from direct Periscope gRPC calls;
// it only needs the local knowledge base for context.
var heartbeatAllowedTools = map[string]bool{
	"search_knowledge": true,
}

var localSkipperTools = map[string]bool{
	"list_mcp_tools":   true,
	"search_knowledge": true,
	"search_web":       true,
}

func (o *Orchestrator) toolsForContext(ctx context.Context) []llm.Tool {
	switch skipper.GetMode(ctx) {
	case "docs":
		return filterTools(o.tools, docsAllowedTools)
	case "spoke":
		return excludeTools(o.tools, spokeMutationBlocklist)
	case "heartbeat":
		return filterTools(o.tools, heartbeatAllowedTools)
	default:
		return o.tools
	}
}

func filterTools(tools []llm.Tool, allowed map[string]bool) []llm.Tool {
	filtered := make([]llm.Tool, 0, len(tools))
	for _, tool := range tools {
		if allowed[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func excludeTools(tools []llm.Tool, blocked map[string]bool) []llm.Tool {
	filtered := make([]llm.Tool, 0, len(tools))
	for _, tool := range tools {
		if !blocked[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (o *Orchestrator) modeAllowsTool(ctx context.Context, name string) bool {
	switch skipper.GetMode(ctx) {
	case "docs":
		return docsAllowedTools[name]
	case "spoke":
		return !spokeMutationBlocklist[name]
	case "heartbeat":
		return heartbeatAllowedTools[name]
	default:
		return true
	}
}

var modeLabels = map[string]string{
	"docs":      "documentation",
	"spoke":     "consultant",
	"heartbeat": "heartbeat",
}

func (o *Orchestrator) executeTool(ctx context.Context, call llm.ToolCall) (ToolOutcome, error) {
	if !o.modeAllowsTool(ctx, call.Name) {
		label := modeLabels[skipper.GetMode(ctx)]
		return ToolOutcome{
			Content: fmt.Sprintf("Tool %q is not available in %s mode.", call.Name, label),
		}, nil
	}
	switch call.Name {
	case "list_mcp_tools":
		return o.listMCPTools(ctx, call.Arguments)
	case "search_knowledge":
		return o.searchKnowledge(ctx, call.Arguments)
	case "search_web":
		return o.searchWebTool(ctx, call.Arguments)
	default:
		return o.callGatewayTool(ctx, call)
	}
}

type ListMCPToolsInput struct {
	IncludeInternal bool `json:"include_internal,omitempty"`
}

type MCPToolInfo struct {
	Name                   string `json:"name"`
	Category               string `json:"category,omitempty"`
	Description            string `json:"description,omitempty"`
	AvailableInCurrentMode bool   `json:"available_in_current_mode"`
}

type MCPToolInventory struct {
	Discovery            string        `json:"discovery"`
	CurrentMode          string        `json:"current_mode,omitempty"`
	GatewayToolCount     int           `json:"gateway_tool_count"`
	GatewayTools         []MCPToolInfo `json:"gateway_tools"`
	SkipperInternalTools []MCPToolInfo `json:"skipper_internal_tools,omitempty"`
	Notes                []string      `json:"notes"`
}

func (o *Orchestrator) listMCPTools(ctx context.Context, arguments string) (ToolOutcome, error) {
	var input ListMCPToolsInput
	if strings.TrimSpace(arguments) != "" {
		if err := json.Unmarshal([]byte(arguments), &input); err != nil {
			return ToolOutcome{}, fmt.Errorf("parse list_mcp_tools arguments: %w", err)
		}
	}
	inventory := o.buildMCPToolInventory(ctx, input.IncludeInternal)
	contentBytes, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return ToolOutcome{}, err
	}
	return ToolOutcome{
		Content: string(contentBytes),
		Detail: ToolDetail{
			Title:   "Tool call: list_mcp_tools",
			Payload: inventory,
		},
	}, nil
}

func (o *Orchestrator) buildMCPToolInventory(ctx context.Context, includeInternal bool) MCPToolInventory {
	mode := skipper.GetMode(ctx)
	gatewayTools := make([]MCPToolInfo, 0, len(o.tools))
	internalTools := make([]MCPToolInfo, 0, len(localSkipperTools))
	for _, tool := range o.tools {
		info := MCPToolInfo{
			Name:                   tool.Name,
			Category:               mcpToolCategory(tool.Name),
			Description:            tool.Description,
			AvailableInCurrentMode: o.modeAllowsTool(ctx, tool.Name),
		}
		if localSkipperTools[tool.Name] {
			if includeInternal {
				internalTools = append(internalTools, info)
			}
			continue
		}
		gatewayTools = append(gatewayTools, info)
	}
	sort.Slice(gatewayTools, func(i, j int) bool {
		if gatewayTools[i].Category == gatewayTools[j].Category {
			return gatewayTools[i].Name < gatewayTools[j].Name
		}
		return gatewayTools[i].Category < gatewayTools[j].Category
	})
	sort.Slice(internalTools, func(i, j int) bool {
		return internalTools[i].Name < internalTools[j].Name
	})
	return MCPToolInventory{
		Discovery:            "Gateway MCP tools/list discovered by Skipper at startup. External MCP clients should call protocol-level tools/list on the Gateway for the exact live inventory.",
		CurrentMode:          mode,
		GatewayToolCount:     len(gatewayTools),
		GatewayTools:         gatewayTools,
		SkipperInternalTools: internalTools,
		Notes: []string{
			"introspect_schema explores the GraphQL schema; it is not MCP tool inventory discovery.",
			"Docs mode can execute only tools marked available_in_current_mode=true. External agents should call dedicated Gateway MCP tools directly for mutations.",
			"search_knowledge and search_web are Skipper helper tools, not public Gateway MCP tools.",
		},
	}
}

func mcpToolCategory(name string) string {
	switch name {
	case "update_billing_details":
		return "Account & Auth"
	case "get_payment_options", "submit_payment":
		return "Payment"
	case "topup_balance", "check_topup":
		return "Billing"
	case "create_stream", "update_stream", "delete_stream", "refresh_stream_key":
		return "Streams"
	case "create_clip", "delete_clip":
		return "Clips"
	case "start_dvr", "stop_dvr":
		return "DVR"
	case "create_vod_upload", "complete_vod_upload", "abort_vod_upload", "delete_vod_asset", "get_vod_upload_status":
		return "VOD"
	case "resolve_playback_endpoint":
		return "Playback"
	case "diagnose_rebuffering", "diagnose_buffer_health", "diagnose_packet_loss", "diagnose_routing", "get_stream_health_summary", "get_anomaly_report":
		return "QoE Diagnostics"
	case "search_support_history":
		return "Support"
	case "introspect_schema", "generate_query", "execute_query":
		return "Schema"
	case "ask_consultant":
		return "Knowledge"
	case "browse_marketplace", "subscribe_to_cluster", "set_preferred_cluster", "create_private_cluster", "create_enrollment_token", "get_node_info", "manage_node", "set_node_mode", "get_node_health":
		return "Infrastructure"
	case "list_mcp_tools", "search_knowledge", "search_web":
		return "Skipper Internal"
	default:
		return "Other"
	}
}

func (o *Orchestrator) callGatewayTool(ctx context.Context, call llm.ToolCall) (ToolOutcome, error) {
	if o.gateway == nil {
		return ToolOutcome{}, fmt.Errorf("tool %q unavailable: gateway not configured", call.Name)
	}
	if !o.gateway.HasTool(call.Name) {
		return ToolOutcome{}, fmt.Errorf("unknown tool %q", call.Name)
	}

	// Block GraphQL mutations via execute_query in restricted modes.
	if call.Name == "execute_query" && isMutationRestricted(ctx) {
		if isMutationQuery(call.Arguments) {
			return ToolOutcome{
				Content: "Mutations are not allowed in this mode. Use execute_query for read-only queries only.",
			}, nil
		}
	}

	content, err := o.gateway.CallTool(ctx, call.Name, json.RawMessage(call.Arguments))
	if err != nil {
		return ToolOutcome{}, err
	}

	// Enrich diagnostic tool output with baseline context (read-only).
	if o.diag != nil && isDiagnosticTool(call.Name) {
		content = o.enrichDiagnosticOutput(ctx, call, content)
	}

	var payload any
	if json.Valid([]byte(content)) {
		payload = json.RawMessage(content)
	} else {
		payload = map[string]string{"result": content}
	}

	return ToolOutcome{
		Content: content,
		Detail: ToolDetail{
			Title:   fmt.Sprintf("Tool call: %s", call.Name),
			Payload: payload,
		},
	}, nil
}

func isMutationRestricted(ctx context.Context) bool {
	mode := skipper.GetMode(ctx)
	return mode == "docs" || mode == "spoke"
}

func isMutationQuery(arguments string) bool {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return false
	}
	// Strip GraphQL line comments and blank lines to find the first operation token.
	for _, line := range strings.Split(args.Query, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.HasPrefix(strings.ToLower(line), "mutation")
	}
	return false
}

var diagnosticTools = map[string]bool{
	"diagnose_rebuffering":   true,
	"diagnose_buffer_health": true,
	"diagnose_packet_loss":   true,
	"diagnose_routing":       true,
	"check_stream_health":    true,
	"get_anomaly_report":     true,
}

func isDiagnosticTool(name string) bool {
	return diagnosticTools[name]
}

// enrichDiagnosticOutput appends baseline deviations and correlation hypotheses
// to a diagnostic tool's response, giving the LLM targeted context.
func (o *Orchestrator) enrichDiagnosticOutput(ctx context.Context, call llm.ToolCall, content string) string {
	tenantID := skipper.GetTenantID(ctx)
	if tenantID == "" {
		return content
	}

	// Extract stream_id from tool arguments if present.
	streamID := ""
	var args struct {
		StreamID string `json:"stream_id"`
	}
	if err := json.Unmarshal([]byte(call.Arguments), &args); err == nil {
		streamID = args.StreamID
	}

	metrics := parseDiagnosticMetrics(content)
	if len(metrics) == 0 {
		return content
	}

	// Try stream-specific baseline; fall back to tenant-wide (stream_id="")
	// since heartbeat writes baselines under stream_id="".
	deviations, err := o.diag.Deviations(ctx, tenantID, streamID, metrics)
	if (err != nil || len(deviations) == 0) && streamID != "" {
		deviations, err = o.diag.Deviations(ctx, tenantID, "", metrics)
	}
	if err != nil || len(deviations) == 0 {
		return content
	}

	correlations := diagnostics.Correlate(deviations)

	var b strings.Builder
	b.WriteString(content)
	b.WriteString("\n\n--- Baseline Context ---\n")
	for _, d := range deviations {
		fmt.Fprintf(&b, "- %s\n", d.String())
	}
	if len(correlations) > 0 {
		b.WriteString("\nCorrelation Hypotheses:\n")
		for _, c := range correlations {
			fmt.Fprintf(&b, "- %s (confidence %.2f)\n", c.Hypothesis, c.Confidence)
		}
	}
	return b.String()
}

// parseDiagnosticMetrics attempts to extract numeric metric values from a
// diagnostic tool response. Gateway tools return DiagnosticResult with a
// nested "metrics" object — we check both the top level and that nested level.
func parseDiagnosticMetrics(content string) map[string]float64 {
	metrics := make(map[string]float64)
	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return metrics
	}
	knownMetrics := map[string]string{
		"avg_buffer_health":     "avg_buffer_health",
		"buffer_health":         "avg_buffer_health",
		"avg_fps":               "avg_fps",
		"fps":                   "avg_fps",
		"avg_bitrate":           "avg_bitrate",
		"avg_bitrate_kbps":      "avg_bitrate",
		"bitrate":               "avg_bitrate",
		"avg_packet_loss":       "avg_packet_loss",
		"packet_loss":           "avg_packet_loss",
		"avg_packet_loss_rate":  "avg_packet_loss",
		"rebuffer_count":        "total_rebuffer_count",
		"total_rebuffer_count":  "total_rebuffer_count",
		"total_issue_count":     "total_issue_count",
		"issue_count":           "total_issue_count",
		"total_issues":          "total_issue_count",
		"avg_bandwidth_in":      "avg_bandwidth_in",
		"bandwidth_in":          "avg_bandwidth_in",
		"avg_bandwidth_out":     "avg_bandwidth_out",
		"avg_bandwidth_out_bps": "avg_bandwidth_out",
		"bandwidth_out":         "avg_bandwidth_out",
		"active_sessions":       "active_sessions",
		"sessions":              "active_sessions",
	}
	// Gateway DiagnosticResult nests values under "metrics".
	sources := []map[string]any{raw}
	if nested, ok := raw["metrics"].(map[string]any); ok {
		sources = append(sources, nested)
	}
	// Sort keys so that when multiple aliases map to the same canonical name,
	// the first alphabetically wins (deterministic across Go map iterations).
	sortedKeys := make([]string, 0, len(knownMetrics))
	for k := range knownMetrics {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)
	for _, src := range sources {
		for _, key := range sortedKeys {
			canonical := knownMetrics[key]
			if _, already := metrics[canonical]; already {
				continue
			}
			if v, ok := src[key]; ok {
				switch val := v.(type) {
				case float64:
					metrics[canonical] = val
				case json.Number:
					if f, err := val.Float64(); err == nil {
						metrics[canonical] = f
					}
				}
			}
		}
	}
	return metrics
}

type SearchKnowledgeInput struct {
	Query       string `json:"query"`
	Limit       int    `json:"limit,omitempty"`
	TenantScope string `json:"tenant_scope,omitempty"`
}

type SearchKnowledgeResult struct {
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	Snippet        string  `json:"snippet,omitempty"`
	Similarity     float64 `json:"similarity,omitempty"`
	SourceType     string  `json:"source_type,omitempty"`
	SectionHeading string  `json:"section_heading,omitempty"`
}

type SearchKnowledgeResponse struct {
	Query   string                  `json:"query"`
	Context string                  `json:"context"`
	Results []SearchKnowledgeResult `json:"results"`
	Sources []Source                `json:"sources"`
}

// preRetrieve runs a quick knowledge search using the user's message and
// returns formatted context. Errors are silently ignored — pre-retrieval is
// best-effort and the LLM can still call search_knowledge explicitly.
func (o *Orchestrator) preRetrieve(ctx context.Context, query string) SearchKnowledgeResponse {
	start := time.Now()
	embedding, err := o.embedder.EmbedQuery(ctx, query)
	if err != nil {
		return SearchKnowledgeResponse{}
	}
	tenantIDs := o.resolveKnowledgeTenants(ctx, "all")
	if len(tenantIDs) == 0 {
		return SearchKnowledgeResponse{}
	}
	var chunks []knowledge.Chunk
	for _, tid := range tenantIDs {
		results, err := o.knowledge.HybridSearch(ctx, tid, embedding, query, 5)
		if err != nil {
			continue
		}
		chunks = append(chunks, results...)
	}
	if o.reranker != nil {
		chunks = o.reranker.Rerank(ctx, query, chunks)
	} else {
		chunks = knowledge.Rerank(query, chunks)
	}
	chunks = knowledge.DeduplicateBySource(chunks, 3, 2)
	searchQueriesTotal.WithLabelValues("pre_retrieval").Inc()
	searchDuration.Observe(time.Since(start).Seconds())
	searchResultsCount.Observe(float64(len(chunks)))
	if len(chunks) == 0 {
		return SearchKnowledgeResponse{}
	}
	return mapKnowledgeResponse(query, chunks)
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
		limit = o.searchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	tenantIDs := o.resolveKnowledgeTenants(ctx, input.TenantScope)
	if len(tenantIDs) == 0 {
		return ToolOutcome{}, errors.New("tenant id is required")
	}

	// Rewrite the conversational query into a search-optimized form.
	searchQuery := query
	if o.queryRewriter != nil {
		searchQuery = o.queryRewriter.Rewrite(ctx, query)
	}

	searchStart := time.Now()

	// Use HyDE embedding when enabled; fall back to regular query embedding.
	var embedding []float32
	if o.hyde != nil {
		hydeVec, hydeErr := o.hyde.GenerateAndEmbed(ctx, searchQuery)
		if hydeErr == nil && hydeVec != nil {
			embedding = hydeVec
		}
	}
	if embedding == nil {
		var err error
		embedding, err = o.embedder.EmbedQuery(ctx, searchQuery)
		if err != nil {
			return ToolOutcome{}, err
		}
	}
	metering.RecordEmbedding(ctx)
	metering.RecordSearchQuery(ctx)

	// Over-fetch 3x to allow source-level deduplication and reranking.
	fetchLimit := limit * 3
	var chunks []knowledge.Chunk
	var (
		searchErr  error
		anySuccess bool
	)
	for _, tenantID := range tenantIDs {
		results, err := o.knowledge.HybridSearch(ctx, tenantID, embedding, searchQuery, fetchLimit)
		if err != nil {
			searchErr = err
			if o.logger != nil {
				o.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Knowledge search failed for tenant")
			}
			continue
		}
		anySuccess = true
		chunks = append(chunks, results...)
	}
	if len(chunks) == 0 && searchErr != nil && !anySuccess {
		return ToolOutcome{}, searchErr
	}
	if o.reranker != nil {
		chunks = o.reranker.Rerank(ctx, searchQuery, chunks)
	} else {
		chunks = knowledge.Rerank(searchQuery, chunks)
	}
	chunks = knowledge.DeduplicateBySource(chunks, limit, 2)
	searchQueriesTotal.WithLabelValues("tool_call").Inc()
	searchDuration.Observe(time.Since(searchStart).Seconds())
	searchResultsCount.Observe(float64(len(chunks)))

	response := mapKnowledgeResponse(query, chunks)
	return ToolOutcome{
		Content: guardUntrustedContext("Knowledge search results", response.Context, maxToolContextTokens),
		Sources: response.Sources,
		Detail: ToolDetail{
			Title:   "Tool call: search_knowledge",
			Payload: response,
		},
	}, nil
}

func (o *Orchestrator) resolveKnowledgeTenants(ctx context.Context, scope string) []string {
	tenantID := skipper.GetTenantID(ctx)
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "global":
		return []string{o.globalTenantID}
	case "all":
		if tenantID == "" {
			return []string{o.globalTenantID}
		}
		return []string{
			tenantID,
			o.globalTenantID,
		}
	case "tenant":
		if tenantID == "" {
			return nil
		}
		return []string{tenantID}
	default: // "all" or empty — match documented default
		if tenantID == "" {
			return []string{o.globalTenantID}
		}
		return []string{tenantID, o.globalTenantID}
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
		sourceType := "Knowledge Base"
		if st, ok := chunk.Metadata["source_type"].(string); ok && st != "" {
			sourceType = st
		}
		heading := extractSectionHeading(chunk.Text)
		results = append(results, SearchKnowledgeResult{
			Title:          title,
			URL:            chunk.SourceURL,
			Snippet:        chunk.Text,
			Similarity:     chunk.Similarity,
			SourceType:     sourceType,
			SectionHeading: heading,
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

func lastUserContent(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return strings.TrimSpace(messages[i].Content)
		}
	}
	return ""
}

func isMCPToolInventoryQuestion(query string) bool {
	q := strings.ToLower(query)
	if !strings.Contains(q, "mcp") {
		return false
	}
	toolTerms := []string{"tool", "tools", "capabilities", "available", "inventory", "list", "what can"}
	for _, term := range toolTerms {
		if strings.Contains(q, term) {
			return true
		}
	}
	return false
}

func appendTrustedSystemContext(messages []llm.Message, contextBlock string) []llm.Message {
	if strings.TrimSpace(contextBlock) == "" {
		return messages
	}
	contextBlock = "\n\n" + contextBlock
	for i := range messages {
		if messages[i].Role == "system" {
			messages[i].Content += contextBlock
			return messages
		}
	}
	return append([]llm.Message{{Role: "system", Content: strings.TrimSpace(contextBlock)}}, messages...)
}

func formatMCPToolInventoryContext(inventory MCPToolInventory) string {
	var b strings.Builder
	b.WriteString("Trusted current FrameWorks MCP tool inventory\n")
	b.WriteString(inventory.Discovery)
	b.WriteString("\n")
	if inventory.CurrentMode != "" {
		fmt.Fprintf(&b, "Current Skipper mode: %s\n", inventory.CurrentMode)
	}
	fmt.Fprintf(&b, "Gateway tool count: %d\n", inventory.GatewayToolCount)
	currentCategory := ""
	for _, tool := range inventory.GatewayTools {
		if tool.Category != currentCategory {
			currentCategory = tool.Category
			fmt.Fprintf(&b, "\n%s:\n", currentCategory)
		}
		availability := "not executable in this Skipper mode"
		if tool.AvailableInCurrentMode {
			availability = "executable in this Skipper mode"
		}
		fmt.Fprintf(&b, "- %s (%s): %s\n", tool.Name, availability, tool.Description)
	}
	if len(inventory.Notes) > 0 {
		b.WriteString("\nNotes:\n")
		for _, note := range inventory.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	return strings.TrimSpace(b.String())
}

func extractSectionHeading(text string) string {
	for _, line := range strings.SplitN(text, "\n", 5) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return ""
}

func formatKnowledgeContext(results []SearchKnowledgeResult) string {
	if len(results) == 0 {
		return "No knowledge base results found."
	}
	var builder strings.Builder
	builder.WriteString("Knowledge base results:\n\n")
	for i, result := range results {
		label := result.Title
		if result.SourceType != "" {
			label = result.SourceType + ": " + label
		}
		if result.SectionHeading != "" {
			label += " > " + result.SectionHeading
		}
		fmt.Fprintf(&builder, "[%d. %s | Relevance: %.2f]\n", i+1, label, result.Similarity)
		if result.URL != "" {
			fmt.Fprintf(&builder, "Source: %s\n", result.URL)
		}
		if result.Snippet != "" {
			builder.WriteString(result.Snippet)
			builder.WriteString("\n")
		}
		if i < len(results)-1 {
			builder.WriteString("---\n")
		}
	}
	return strings.TrimSpace(builder.String())
}

func (o *Orchestrator) searchWebTool(ctx context.Context, arguments string) (ToolOutcome, error) {
	if o.searchWeb == nil {
		return ToolOutcome{}, errors.New("search provider unavailable")
	}
	// Rewrite the query before passing to the web search provider.
	if o.queryRewriter != nil {
		var input struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(arguments), &input); err == nil && input.Query != "" {
			rewritten := o.queryRewriter.Rewrite(ctx, input.Query)
			if rewritten != input.Query {
				var raw map[string]json.RawMessage
				if err := json.Unmarshal([]byte(arguments), &raw); err == nil {
					if b, err := json.Marshal(rewritten); err == nil {
						raw["query"] = b
						if patched, err := json.Marshal(raw); err == nil {
							arguments = string(patched)
						}
					}
				}
			}
		}
	}
	response, err := o.searchWeb.Call(ctx, arguments)
	if err != nil {
		return ToolOutcome{}, err
	}
	return ToolOutcome{
		Content: guardUntrustedContext("Web search results", response.Context, maxToolContextTokens),
		Sources: response.Sources,
		Detail: ToolDetail{
			Title:   "Tool call: search_web",
			Payload: response,
		},
	}, nil
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
	switch trimmed {
	case "[sources]":
		f.inSources = true
		return nil
	case "[/sources]":
		f.inSources = false
		return nil
	}
	if f.inSources {
		return nil
	}
	// Strip confidence markup wherever it appears in the line.
	line = stripConfidenceTags(line)
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

func stripConfidenceTags(s string) string {
	s = strings.ReplaceAll(s, "[/confidence]", "")
	for {
		start := strings.Index(s, "[confidence:")
		if start == -1 {
			return s
		}
		end := strings.Index(s[start:], "]")
		if end == -1 {
			return s
		}
		s = s[:start] + s[start+end+1:]
	}
}

func parseConfidenceTurns(turns []string) []ConfidenceBlock {
	var blocks []ConfidenceBlock
	for _, turn := range turns {
		parsed := parseConfidenceBlocks(turn)
		blocks = append(blocks, parsed...)
	}
	return blocks
}

func parseConfidenceBlocks(input string) []ConfidenceBlock {
	var blocks []ConfidenceBlock
	remaining := input
	for {
		start := strings.Index(remaining, "[confidence:")
		if start == -1 {
			break
		}
		if preamble := strings.TrimSpace(stripConfidenceTags(remaining[:start])); preamble != "" {
			blocks = append(blocks, ConfidenceBlock{
				Content:    preamble,
				Confidence: ConfidenceUnknown,
			})
		}
		endTag := strings.Index(remaining[start:], "]")
		if endTag == -1 {
			break
		}
		tag := remaining[start+len("[confidence:") : start+endTag]
		afterTag := remaining[start+endTag+1:]
		nextStart := strings.Index(afterTag, "[confidence:")
		section := afterTag
		if nextStart != -1 {
			section = afterTag[:nextStart]
		}

		content := section
		var parsedSources []Source
		if sourcesStart := strings.Index(section, "[sources]"); sourcesStart != -1 {
			content = section[:sourcesStart]
			afterSources := section[sourcesStart+len("[sources]"):]
			if sourcesEnd := strings.Index(afterSources, "[/sources]"); sourcesEnd != -1 {
				parsedSources = parseSourcesBlock(afterSources[:sourcesEnd])
				trailing := strings.TrimSpace(stripConfidenceTags(afterSources[sourcesEnd+len("[/sources]"):]))
				if trailing != "" {
					content = strings.TrimSpace(content)
					if content != "" {
						content += "\n\n" + trailing
					} else {
						content = trailing
					}
				}
			}
		}
		content = strings.TrimSpace(stripConfidenceTags(content))
		if content != "" || len(parsedSources) > 0 {
			blocks = append(blocks, ConfidenceBlock{
				Content:    content,
				Confidence: Confidence(strings.TrimSpace(tag)),
				Sources:    parsedSources,
			})
		}
		if nextStart == -1 {
			break
		}
		remaining = afterTag[nextStart:]
	}
	if len(blocks) == 0 {
		trimmed := strings.TrimSpace(stripConfidenceTags(input))
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
	parts := strings.SplitN(line, "\u2014", 2)
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
				existing[i].Arguments = inc.Arguments
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
