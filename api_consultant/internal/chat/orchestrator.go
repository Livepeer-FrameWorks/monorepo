package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/metering"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/tenants"
)

const defaultMaxToolRounds = 5

// GatewayToolCaller invokes tools on the Gateway MCP server.
type GatewayToolCaller interface {
	AvailableTools() []llm.Tool
	HasTool(name string) bool
	CallTool(ctx context.Context, name string, arguments json.RawMessage) (string, error)
}

type OrchestratorConfig struct {
	LLMProvider llm.Provider
	Logger      logging.Logger
	SearchWeb   *SearchWebTool
	Knowledge   KnowledgeSearcher
	Embedder    KnowledgeEmbedder
	Gateway     GatewayToolCaller
	MaxRounds   int
}

type Orchestrator struct {
	llmProvider llm.Provider
	logger      logging.Logger
	searchWeb   *SearchWebTool
	knowledge   KnowledgeSearcher
	embedder    KnowledgeEmbedder
	gateway     GatewayToolCaller
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

type KnowledgeSearcher interface {
	Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error)
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
	return &Orchestrator{
		llmProvider: cfg.LLMProvider,
		logger:      cfg.Logger,
		searchWeb:   cfg.SearchWeb,
		knowledge:   cfg.Knowledge,
		embedder:    cfg.Embedder,
		gateway:     cfg.Gateway,
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
