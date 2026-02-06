package mcpspoke

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"frameworks/api_consultant/internal/knowledge"
	"frameworks/pkg/logging"
	"frameworks/pkg/search"
	"frameworks/pkg/tenants"
	"frameworks/pkg/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// KnowledgeSearcher queries the knowledge store by vector similarity.
type KnowledgeSearcher interface {
	Search(ctx context.Context, tenantID string, embedding []float32, limit int) ([]knowledge.Chunk, error)
}

// KnowledgeEmbedder generates query embeddings for knowledge search.
type KnowledgeEmbedder interface {
	EmbedQuery(ctx context.Context, query string) ([]float32, error)
}

// SearchProvider runs web searches.
type SearchProvider interface {
	Search(ctx context.Context, query string, opts search.SearchOptions) ([]search.Result, error)
}

// Config configures the Skipper spoke MCP server.
type Config struct {
	Knowledge      KnowledgeSearcher
	Embedder       KnowledgeEmbedder
	SearchProvider SearchProvider
	Logger         logging.Logger
}

// NewServer creates an MCP server exposing Skipper's specialist tools
// (search_knowledge, search_web) for the Gateway hub to proxy.
func NewServer(cfg Config) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "skipper-spoke",
		Version: version.Version,
	}, nil)

	registerSearchKnowledge(srv, cfg)
	registerSearchWeb(srv, cfg)

	return srv
}

// --- search_knowledge ---

type searchKnowledgeInput struct {
	TenantID    string `json:"tenant_id" jsonschema:"required" jsonschema_description:"Tenant ID for scoping the search"`
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the knowledge base"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 5)"`
	TenantScope string `json:"tenant_scope,omitempty" jsonschema_description:"Scope to search: tenant, global, or all (default all)"`
}

type searchKnowledgeResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Snippet    string  `json:"snippet,omitempty"`
	Similarity float64 `json:"similarity,omitempty"`
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
		limit = 5
	}

	tenantIDs := resolveKnowledgeTenants(args.TenantID, args.TenantScope)
	embedding, err := cfg.Embedder.EmbedQuery(ctx, query)
	if err != nil {
		return spokeError(fmt.Sprintf("embedding failed: %v", err))
	}

	var chunks []knowledge.Chunk
	for _, tid := range tenantIDs {
		results, err := cfg.Knowledge.Search(ctx, tid, embedding, limit)
		if err != nil {
			return spokeError(fmt.Sprintf("knowledge search failed: %v", err))
		}
		chunks = append(chunks, results...)
	}
	sort.Slice(chunks, func(i, j int) bool {
		return chunks[i].Similarity > chunks[j].Similarity
	})
	if len(chunks) > limit {
		chunks = chunks[:limit]
	}

	results := make([]searchKnowledgeResult, 0, len(chunks))
	for _, chunk := range chunks {
		title := strings.TrimSpace(chunk.SourceTitle)
		if title == "" {
			title = chunk.SourceURL
		}
		results = append(results, searchKnowledgeResult{
			Title:      title,
			URL:        chunk.SourceURL,
			Snippet:    truncate(chunk.Text, 320),
			Similarity: chunk.Similarity,
		})
	}

	resp := searchKnowledgeResponse{Query: query, Results: results}
	return spokeSuccess(resp)
}

func resolveKnowledgeTenants(tenantID, scope string) []string {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case "global":
		return []string{tenants.SystemTenantID.String()}
	case "all":
		return []string{tenantID, tenants.SystemTenantID.String()}
	default:
		return []string{tenantID}
	}
}

// --- search_web ---

type searchWebInput struct {
	Query       string `json:"query" jsonschema:"required" jsonschema_description:"Search query to run against the web"`
	Limit       int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results to return (default 5)"`
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
		limit = 5
	}
	depth := strings.TrimSpace(args.SearchDepth)
	if depth == "" {
		depth = "basic"
	}

	results, err := cfg.SearchProvider.Search(ctx, query, search.SearchOptions{
		Limit:       limit,
		SearchDepth: depth,
	})
	if err != nil {
		return spokeError(fmt.Sprintf("web search failed: %v", err))
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

func truncate(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes-1]) + "…"
}
