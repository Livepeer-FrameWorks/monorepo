package mcpspoke

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/api_consultant/internal/chat"
	"frameworks/api_consultant/internal/knowledge"
	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/llm"
	"frameworks/pkg/logging"
	"frameworks/pkg/search"
	"frameworks/pkg/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// KnowledgeSearcher queries the knowledge store.
type KnowledgeSearcher interface {
	Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error)
	HybridSearch(ctx context.Context, tenantID string, embedding []float32, query string, limit int) ([]knowledge.Chunk, error)
}

// KnowledgeEmbedder generates query embeddings for knowledge search.
type KnowledgeEmbedder interface {
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

// SearchProvider runs web searches.
type SearchProvider interface {
	Search(ctx context.Context, query string, opts search.SearchOptions) ([]search.Result, error)
}

// ConsultantOrchestrator runs a question through the full Skipper pipeline
// (pre-retrieval, query rewriting, HyDE, multi-round tool loop, confidence tagging).
type ConsultantOrchestrator interface {
	Run(ctx context.Context, messages []llm.Message, streamer chat.TokenStreamer) (chat.OrchestratorResult, error)
}

const (
	defaultGlobalTenantID = "00000000-0000-0000-0000-000000000001"
	defaultSearchLimit    = 5
)

// Config configures the Skipper spoke MCP server.
type Config struct {
	Knowledge      KnowledgeSearcher
	Embedder       KnowledgeEmbedder
	Reranker       *knowledge.Reranker
	SearchProvider SearchProvider
	Orchestrator   ConsultantOrchestrator
	Logger         logging.Logger
	GlobalTenantID string
	SearchLimit    int
}

// NewServer creates an MCP server exposing Skipper's specialist tools
// (search_knowledge, search_web) for the Gateway hub to proxy.
func NewServer(cfg Config) *mcp.Server {
	if cfg.GlobalTenantID == "" {
		cfg.GlobalTenantID = defaultGlobalTenantID
	}
	if cfg.SearchLimit <= 0 {
		cfg.SearchLimit = defaultSearchLimit
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "skipper-spoke",
		Version: version.Version,
	}, nil)

	registerSearchKnowledge(srv, cfg)
	registerSearchWeb(srv, cfg)
	registerAskConsultant(srv, cfg)

	return srv
}

// --- search_knowledge ---

type searchKnowledgeInput struct {
	TenantID    string `json:"tenant_id" jsonschema:"required" jsonschema_description:"Tenant ID for scoping the search"`
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the knowledge base"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 8)"`
	TenantScope string `json:"tenant_scope,omitempty" jsonschema_description:"Scope to search: tenant, global, or all (default tenant)"`
}

type searchKnowledgeResult struct {
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	SourceType     string  `json:"source_type,omitempty"`
	SectionHeading string  `json:"section_heading,omitempty"`
	Snippet        string  `json:"snippet,omitempty"`
	Similarity     float64 `json:"similarity,omitempty"`
}

type searchKnowledgeResponse struct {
	Query   string                  `json:"query"`
	Results []searchKnowledgeResult `json:"results"`
}

func registerSearchKnowledge(srv *mcp.Server, cfg Config) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "search_knowledge",
			Description: "Search the Skipper knowledge base for platform-specific guidance and verified docs.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args searchKnowledgeInput) (*mcp.CallToolResult, any, error) {
			return handleSearchKnowledge(ctx, args, cfg)
		},
	)
}

func handleSearchKnowledge(ctx context.Context, args searchKnowledgeInput, cfg Config) (*mcp.CallToolResult, any, error) {
	if cfg.Knowledge == nil || cfg.Embedder == nil {
		return spokeError("knowledge search unavailable")
	}
	if args.TenantID == "" {
		return spokeError("tenant_id is required")
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return spokeError("query is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = cfg.SearchLimit
	}

	tenantIDs := resolveKnowledgeTenants(args.TenantID, args.TenantScope, cfg.GlobalTenantID)
	embedding, err := cfg.Embedder.EmbedQuery(ctx, query)
	if err != nil {
		if cfg.Logger != nil {
			cfg.Logger.WithError(err).WithField("query", query).Warn("spoke embedding failed")
		}
		return spokeError(fmt.Sprintf("embedding failed: %v", err))
	}

	searchStart := time.Now()
	fetchLimit := limit * 3
	var chunks []knowledge.Chunk
	for _, tid := range tenantIDs {
		results, err := cfg.Knowledge.HybridSearch(ctx, tid, embedding, query, fetchLimit)
		if err != nil {
			if cfg.Logger != nil {
				cfg.Logger.WithError(err).WithField("tenant_id", tid).Warn("spoke knowledge search failed")
			}
			return spokeError(fmt.Sprintf("knowledge search failed: %v", err))
		}
		chunks = append(chunks, results...)
	}
	if cfg.Reranker != nil {
		chunks = cfg.Reranker.Rerank(ctx, query, chunks)
	} else {
		chunks = knowledge.Rerank(query, chunks)
	}
	chunks = knowledge.DeduplicateBySource(chunks, limit, 2)
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Similarity > chunks[j].Similarity
	})
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}
	spokeSearchQueriesTotal.WithLabelValues("search_knowledge").Inc()
	spokeSearchDuration.Observe(time.Since(searchStart).Seconds())
	spokeSearchResultsCount.Observe(float64(len(chunks)))
	if cfg.Logger != nil {
		cfg.Logger.WithField("query", query).WithField("results", len(chunks)).Debug("spoke knowledge search")
	}

	results := make([]searchKnowledgeResult, 0, len(chunks))
	for _, chunk := range chunks {
		title := strings.TrimSpace(chunk.SourceTitle)
		if title == "" {
			title = chunk.SourceURL
		}
		results = append(results, searchKnowledgeResult{
			Title:          title,
			URL:            chunk.SourceURL,
			SourceType:     chunk.SourceType,
			SectionHeading: extractSectionHeading(chunk.Text),
			Snippet:        chunk.Text,
			Similarity:     chunk.Similarity,
		})
	}

	resp := searchKnowledgeResponse{Query: query, Results: results}
	return spokeSuccess(resp)
}

func resolveKnowledgeTenants(tenantID, scope, globalTenantID string) []string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "global":
		return []string{globalTenantID}
	case "all":
		return []string{tenantID, globalTenantID}
	default:
		return []string{tenantID}
	}
}

// --- search_web ---

type searchWebInput struct {
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the web"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 8)"`
	SearchDepth string `json:"search_depth,omitempty" jsonschema_description:"Search depth: basic or advanced (default basic)"`
}

type searchWebResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

type searchWebResponse struct {
	Query   string            `json:"query"`
	Results []searchWebResult `json:"results"`
}

func registerSearchWeb(srv *mcp.Server, cfg Config) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "search_web",
			Description: "Search the public web for documentation or references when the knowledge base is insufficient.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args searchWebInput) (*mcp.CallToolResult, any, error) {
			return handleSearchWeb(ctx, args, cfg)
		},
	)
}

func handleSearchWeb(ctx context.Context, args searchWebInput, cfg Config) (*mcp.CallToolResult, any, error) {
	if cfg.SearchProvider == nil {
		return spokeError("search provider unavailable")
	}
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return spokeError("query is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = cfg.SearchLimit
	}
	depth := strings.TrimSpace(args.SearchDepth)
	if depth == "" {
		depth = "basic"
	}

	searchStart := time.Now()
	results, err := cfg.SearchProvider.Search(ctx, query, search.SearchOptions{
		Limit:       limit,
		SearchDepth: depth,
	})
	if err != nil {
		if cfg.Logger != nil {
			cfg.Logger.WithError(err).WithField("query", query).Warn("spoke web search failed")
		}
		return spokeError(fmt.Sprintf("web search failed: %v", err))
	}
	spokeSearchQueriesTotal.WithLabelValues("search_web").Inc()
	spokeSearchDuration.Observe(time.Since(searchStart).Seconds())
	spokeSearchResultsCount.Observe(float64(len(results)))
	if cfg.Logger != nil {
		cfg.Logger.WithField("query", query).WithField("results", len(results)).Debug("spoke web search")
	}

	mapped := make([]searchWebResult, 0, len(results))
	for _, r := range results {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = r.URL
		}
		mapped = append(mapped, searchWebResult{
			Title:   title,
			URL:     strings.TrimSpace(r.URL),
			Snippet: truncate(r.Content, 320),
			Score:   r.Score,
		})
	}

	resp := searchWebResponse{Query: query, Results: mapped}
	return spokeSuccess(resp)
}

// --- ask_consultant ---

type askConsultantInput struct {
	TenantID string `json:"tenant_id" jsonschema:"required" jsonschema_description:"Tenant ID (injected by Gateway)"`
	Question string `json:"question" jsonschema:"required" jsonschema_description:"Question for the AI video streaming consultant"`
	Mode     string `json:"mode,omitempty" jsonschema_description:"Set to docs for read-only mode (default full)"`
}

type askConsultantSource struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Type  string `json:"type"`
}

type askConsultantResponse struct {
	Answer     string                `json:"answer"`
	Confidence string                `json:"confidence"`
	Sources    []askConsultantSource `json:"sources"`
	ToolsUsed  []string              `json:"tools_used"`
}

// discardStreamer is a TokenStreamer that discards all tokens.
// MCP tools return complete results, so streaming is unnecessary.
type discardStreamer struct{}

func (discardStreamer) SendToken(string) error { return nil }

func registerAskConsultant(srv *mcp.Server, cfg Config) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "ask_consultant",
			Description: "Ask the AI video streaming consultant a question. Runs the full Skipper pipeline (knowledge retrieval, web search, reasoning) and returns an answer with confidence tagging and source citations.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, args askConsultantInput) (*mcp.CallToolResult, any, error) {
			return handleAskConsultant(ctx, args, cfg)
		},
	)
}

func handleAskConsultant(ctx context.Context, args askConsultantInput, cfg Config) (*mcp.CallToolResult, any, error) {
	if cfg.Orchestrator == nil {
		return spokeError("consultant unavailable")
	}
	if args.TenantID == "" {
		return spokeError("tenant_id is required")
	}
	question := strings.TrimSpace(args.Question)
	if question == "" {
		return spokeError("question is required")
	}

	// Set tenant context so the orchestrator scopes knowledge search
	// and Gateway tool calls to the correct tenant.
	ctx = skipper.WithTenantID(ctx, args.TenantID)

	systemContent := chat.SystemPrompt
	if strings.EqualFold(strings.TrimSpace(args.Mode), "docs") {
		systemContent += chat.DocsSystemPromptSuffix
	}

	messages := []llm.Message{
		{Role: "system", Content: systemContent},
		{Role: "user", Content: question},
	}

	result, err := cfg.Orchestrator.Run(ctx, messages, discardStreamer{})
	if err != nil {
		if cfg.Logger != nil {
			cfg.Logger.WithError(err).WithField("question", question).Warn("ask_consultant failed")
		}
		return spokeError(fmt.Sprintf("consultant error: %v", err))
	}

	sources := make([]askConsultantSource, 0, len(result.Sources))
	for _, s := range result.Sources {
		sources = append(sources, askConsultantSource{
			Title: s.Title,
			URL:   s.URL,
			Type:  string(s.Type),
		})
	}
	toolsUsed := make([]string, 0, len(result.ToolCalls))
	seen := make(map[string]bool)
	for _, tc := range result.ToolCalls {
		if !seen[tc.Name] {
			toolsUsed = append(toolsUsed, tc.Name)
			seen[tc.Name] = true
		}
	}

	resp := askConsultantResponse{
		Answer:     result.Content,
		Confidence: string(result.Confidence),
		Sources:    sources,
		ToolsUsed:  toolsUsed,
	}
	spokeSearchQueriesTotal.WithLabelValues("ask_consultant").Inc()
	return spokeSuccess(resp)
}

// --- helpers ---

func spokeError(message string) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
		IsError: true,
	}, nil, nil
}

func spokeSuccess(result any) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return spokeError(fmt.Sprintf("failed to format result: %v", err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, result, nil
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

func truncate(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}
