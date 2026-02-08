package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"time"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/metering"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
)

const defaultMaxToolRounds = 5

// GatewayToolCaller invokes tools on the Gateway MCP server.
type GatewayToolCaller interface {
	AvailableTools() []llm.Tool
	HasTool(name string) bool
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

type OrchestratorConfig struct {
	LLMProvider    llm.Provider
	Logger         logging.Logger
	SearchWeb      *SearchWebTool
	Knowledge      KnowledgeSearcher
	Embedder       KnowledgeEmbedder
	Reranker       *knowledge.Reranker
	QueryRewriter  *QueryRewriter
	HyDE           *HyDEGenerator
	Gateway        GatewayToolCaller
	MaxRounds      int
	SearchLimit    int
	GlobalTenantID string
}

const defaultGlobalTenantID = "00000000-0000-0000-0000-000000000001"

type Orchestrator struct {
	llmProvider    llm.Provider
	logger         logging.Logger
	searchWeb      *SearchWebTool
	knowledge      KnowledgeSearcher
	embedder       KnowledgeEmbedder
	reranker       *knowledge.Reranker
	queryRewriter  *QueryRewriter
	hyde           *HyDEGenerator
	gateway        GatewayToolCaller
	tools          []llm.Tool
	maxRounds      int
	searchLimit    int
	globalTenantID string
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
		llmProvider:    cfg.LLMProvider,
		logger:         cfg.Logger,
		searchWeb:      cfg.SearchWeb,
		knowledge:      cfg.Knowledge,
		embedder:       cfg.Embedder,
		reranker:       cfg.Reranker,
		queryRewriter:  cfg.QueryRewriter,
		hyde:           cfg.HyDE,
		gateway:        cfg.Gateway,
		tools:          tools,
		maxRounds:      maxRounds,
		searchLimit:    searchLimit,
		globalTenantID: globalTenantID,
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

	// Pre-retrieval: auto-search knowledge base using the user's last message
	// before the first LLM call to save a round-trip.
	if o.knowledge != nil && o.embedder != nil && len(messages) > 0 {
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
					messages = trimMessagesToBudget(messages, maxPromptTokenBudget)
				}
			}
		}
	}

	for round := 0; round < o.maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return OrchestratorResult{}, err
		}

		inputTokens += countTokensInMessages(messages)
		llmStart := time.Now()
		stream, err := o.llmProvider.Complete(ctx, messages, o.toolsForContext(ctx))
		if err != nil {
			llmCallsTotal.WithLabelValues("error").Inc()
			llmDuration.Observe(time.Since(llmStart).Seconds())
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
		llmCallsTotal.WithLabelValues("success").Inc()
		llmDuration.Observe(time.Since(llmStart).Seconds())
		if err := filter.Flush(); err != nil {
			return OrchestratorResult{}, err
		}

		if len(pendingToolCalls) == 0 {
			break
		}

		tes, _ := streamer.(ToolEventStreamer)
		for _, call := range pendingToolCalls {
			if tes != nil {
				_ = tes.SendToolStart(call.Name)
			}
			outcome, err := o.executeTool(ctx, call)
			record := ToolCallRecord{Name: call.Name}
			if call.Arguments != "" {
				record.Arguments = json.RawMessage(call.Arguments)
			}
			errMsg := ""
			if err != nil {
				if o.logger != nil {
					o.logger.WithError(err).WithField("tool", call.Name).Warn("Skipper tool execution failed")
				}
				record.Error = err.Error()
				errMsg = err.Error()
				outcome = ToolOutcome{
					Content: fmt.Sprintf("Tool %s failed: %v", call.Name, err),
					Detail: ToolDetail{
						Title:   fmt.Sprintf("Tool call: %s", call.Name),
						Payload: map[string]any{"error": err.Error()},
					},
				}
			}
			if tes != nil {
				_ = tes.SendToolEnd(call.Name, errMsg)
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

	outputTokens := estimateTokens(content)
	llmTokensTotal.WithLabelValues("input").Add(float64(inputTokens))
	llmTokensTotal.WithLabelValues("output").Add(float64(outputTokens))

	return OrchestratorResult{
		Content:    content,
		Confidence: confidence,
		Sources:    sources,
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
	"search_knowledge":          true,
	"search_web":                true,
	"introspect_schema":         true,
	"generate_query":            true,
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
}

func (o *Orchestrator) toolsForContext(ctx context.Context) []llm.Tool {
	if skipper.GetMode(ctx) != "docs" {
		return o.tools
	}
	return filterDocsTools(o.tools)
}

func filterDocsTools(tools []llm.Tool) []llm.Tool {
	filtered := make([]llm.Tool, 0, len(tools))
	for _, tool := range tools {
		if docsAllowedTools[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func (o *Orchestrator) executeTool(ctx context.Context, call llm.ToolCall) (ToolOutcome, error) {
	if skipper.GetMode(ctx) == "docs" && !docsAllowedTools[call.Name] {
		return ToolOutcome{
			Content: fmt.Sprintf("Tool %q is not available in documentation mode.", call.Name),
		}, nil
	}
	switch call.Name {
	case "search_knowledge":
		return o.searchKnowledge(ctx, call.Arguments)
	case "search_web":
		return o.searchWebTool(ctx, call.Arguments)
	default:
		return o.callGatewayTool(ctx, call)
	}
}

func (o *Orchestrator) callGatewayTool(ctx context.Context, call llm.ToolCall) (ToolOutcome, error) {
	if o.gateway == nil {
		return ToolOutcome{}, fmt.Errorf("tool %q unavailable: gateway not configured", call.Name)
	}
	if !o.gateway.HasTool(call.Name) {
		return ToolOutcome{}, fmt.Errorf("unknown tool %q", call.Name)
	}

	content, err := o.gateway.CallTool(ctx, call.Name, json.RawMessage(call.Arguments))
	if err != nil {
		return ToolOutcome{}, err
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
		Content: response.Context,
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
		Content: response.Context,
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

func trimMessagesToBudget(messages []llm.Message, budget int) []llm.Message {
	if budget <= 0 || len(messages) <= 2 {
		return messages
	}
	total := countTokensInMessages(messages)
	if total <= budget {
		return messages
	}
	for total > budget && len(messages) > 2 {
		total -= estimateTokens(messages[1].Content)
		messages = append(messages[:1], messages[2:]...)
	}
	return messages
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
