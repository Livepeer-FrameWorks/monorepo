package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterSupportTools registers support-related MCP tools.
func RegisterSupportTools(server *mcp.Server, serviceClients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// list_support_conversations - List recent support conversations
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "list_support_conversations",
			Description: "List recent support conversations for the authenticated account. Use this when the user asks to browse or search their past tickets without a specific keyword.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ListSupportConversationsInput) (*mcp.CallToolResult, any, error) {
			return handleListSupportConversations(ctx, args, serviceClients, logger)
		},
	)

	// search_support_history - Search past support conversations
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_support_history",
			Description: "Search past support conversations by keyword. Use list_support_conversations instead when the user only asks to see past tickets without naming a specific topic.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SearchSupportHistoryInput) (*mcp.CallToolResult, any, error) {
			return handleSearchSupportHistory(ctx, args, serviceClients, logger)
		},
	)
}

// ListSupportConversationsInput represents input for list_support_conversations.
type ListSupportConversationsInput struct {
	Limit int `json:"limit,omitempty" jsonschema_description:"Maximum number of recent conversations (default 10, max 50)"`
}

// SearchSupportHistoryInput represents input for search_support_history tool.
type SearchSupportHistoryInput struct {
	Query string `json:"query" jsonschema:"required" jsonschema_description:"Search query - keywords to find in conversation subjects and messages"`
	Limit int    `json:"limit,omitempty" jsonschema_description:"Maximum number of results (default 10, max 50)"`
}

// SearchResult represents a search result from support history.
type SearchResult struct {
	ConversationID string `json:"conversation_id"`
	Subject        string `json:"subject"`
	Status         string `json:"status"`
	CreatedAt      string `json:"created_at"`
	MatchedSnippet string `json:"matched_snippet,omitempty"`
	Relevance      string `json:"relevance"`
}

// SearchSupportHistoryResult represents the search results.
type SearchSupportHistoryResult struct {
	Query      string         `json:"query,omitempty"`
	Mode       string         `json:"mode"`
	TotalFound int            `json:"total_found"`
	Results    []SearchResult `json:"results"`
	Hint       string         `json:"hint,omitempty"`
}

func handleListSupportConversations(ctx context.Context, args ListSupportConversationsInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	limit := normalizeSupportLimit(args.Limit)
	if logger != nil {
		logger.WithField("tenant_id", tenantID).
			WithField("limit", limit).
			WithField("tool", "list_support_conversations").
			Info("MCP support conversation list started")
	}

	resp, err := serviceClients.Deckhand.ListConversations(ctx, tenantID, 1, int32(limit))
	if err != nil {
		if logger != nil {
			logger.WithError(err).
				WithField("tenant_id", tenantID).
				WithField("tool", "list_support_conversations").
				Warn("Failed to list support conversations")
		}
		return toolError(fmt.Sprintf("Failed to list support conversations: %v", err))
	}

	results := supportSearchResults(resp.Conversations, limit)
	result := SearchSupportHistoryResult{
		Mode:       "list",
		TotalFound: len(results),
		Results:    results,
	}
	if resp.TotalCount > 0 {
		result.TotalFound = int(resp.TotalCount)
	}
	if len(results) == 0 {
		result.Hint = "No support conversations were found for this account."
		if logger != nil {
			logger.WithField("tenant_id", tenantID).
				WithField("tool", "list_support_conversations").
				Warn("MCP support conversation list returned no results")
		}
	} else if len(results) >= limit {
		result.Hint = fmt.Sprintf("Showing first %d conversations. Search by keyword for a narrower result.", limit)
	}
	if logger != nil {
		logger.WithField("tenant_id", tenantID).
			WithField("tool", "list_support_conversations").
			WithField("result_count", len(results)).
			WithField("total_count", result.TotalFound).
			Info("MCP support conversation list completed")
	}

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

func handleSearchSupportHistory(ctx context.Context, args SearchSupportHistoryInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	if args.Query == "" {
		return toolError("query is required")
	}

	limit := normalizeSupportLimit(args.Limit)
	if isGenericSupportHistoryQuery(args.Query) {
		if logger != nil {
			logger.WithField("tenant_id", tenantID).
				WithField("query", args.Query).
				WithField("tool", "search_support_history").
				Info("MCP support search treated as generic list request")
		}
		return handleListSupportConversations(ctx, ListSupportConversationsInput{Limit: limit}, serviceClients, logger)
	}

	if logger != nil {
		logger.WithField("tenant_id", tenantID).
			WithField("query", args.Query).
			WithField("limit", limit).
			WithField("tool", "search_support_history").
			Info("MCP support history search started")
	}

	resp, err := serviceClients.Deckhand.SearchConversations(ctx, tenantID, args.Query, 1, int32(limit))
	if err != nil {
		if logger != nil {
			logger.WithError(err).
				WithField("tenant_id", tenantID).
				WithField("query", args.Query).
				WithField("tool", "search_support_history").
				Warn("Failed to search support conversations")
		}
		return toolError(fmt.Sprintf("Failed to search support history: %v", err))
	}

	queryLower := strings.ToLower(args.Query)
	queryTerms := strings.Fields(queryLower)
	var results []SearchResult

	for _, conv := range resp.Conversations {
		subjectLower := strings.ToLower(conv.Subject)

		// Check if subject matches
		matchScore := 0
		for _, term := range queryTerms {
			if strings.Contains(subjectLower, term) {
				matchScore++
			}
		}

		// Check last message if available
		var matchedSnippet string
		if conv.LastMessage != nil {
			contentLower := strings.ToLower(conv.LastMessage.Content)
			for _, term := range queryTerms {
				if strings.Contains(contentLower, term) {
					matchScore++
					// Extract snippet around match
					idx := strings.Index(contentLower, term)
					if idx >= 0 {
						start := idx - 30
						if start < 0 {
							start = 0
						}
						end := idx + len(term) + 50
						if end > len(conv.LastMessage.Content) {
							end = len(conv.LastMessage.Content)
						}
						matchedSnippet = "..." + conv.LastMessage.Content[start:end] + "..."
					}
					break
				}
			}
		}

		relevance := "low"
		if matchScore >= len(queryTerms) {
			relevance = "high"
		} else if matchScore > len(queryTerms)/2 {
			relevance = "medium"
		}

		createdAt := ""
		if conv.CreatedAt != nil {
			createdAt = conv.CreatedAt.AsTime().Format("2006-01-02")
		}

		status := "unknown"
		switch conv.Status {
		case pb.ConversationStatus_CONVERSATION_STATUS_OPEN:
			status = "open"
		case pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED:
			status = "resolved"
		case pb.ConversationStatus_CONVERSATION_STATUS_PENDING:
			status = "pending"
		}

		results = append(results, SearchResult{
			ConversationID: conv.Id,
			Subject:        conv.Subject,
			Status:         status,
			CreatedAt:      createdAt,
			MatchedSnippet: matchedSnippet,
			Relevance:      relevance,
		})

		if len(results) >= limit {
			break
		}
	}

	result := SearchSupportHistoryResult{
		Query:      args.Query,
		Mode:       "search",
		TotalFound: len(results),
		Results:    results,
	}
	if resp.TotalCount > 0 {
		result.TotalFound = int(resp.TotalCount)
	}

	if len(results) == 0 {
		result.Hint = "No matching conversations found. Try different keywords or check the full conversation list using support://conversations resource."
		if logger != nil {
			logger.WithField("tenant_id", tenantID).
				WithField("query", args.Query).
				WithField("tool", "search_support_history").
				WithField("deckhand_total_count", resp.TotalCount).
				Warn("MCP support history search returned no results")
		}
	} else if len(results) >= limit {
		result.Hint = fmt.Sprintf("Showing first %d results. Refine your query for more specific results.", limit)
	}
	if logger != nil {
		logger.WithField("tenant_id", tenantID).
			WithField("query", args.Query).
			WithField("tool", "search_support_history").
			WithField("result_count", len(results)).
			WithField("total_count", result.TotalFound).
			Info("MCP support history search completed")
	}

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

func normalizeSupportLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func isGenericSupportHistoryQuery(query string) bool {
	normalized := strings.Join(strings.Fields(strings.ToLower(query)), " ")
	if normalized == "" {
		return false
	}
	for _, prefix := range []string{"show me ", "find me ", "search ", "show ", "list ", "find ", "browse "} {
		normalized = strings.TrimPrefix(normalized, prefix)
	}
	genericQueries := map[string]bool{
		"my past support tickets":        true,
		"past support tickets":           true,
		"support tickets":                true,
		"my support tickets":             true,
		"my tickets":                     true,
		"tickets":                        true,
		"support history":                true,
		"my support history":             true,
		"past support conversations":     true,
		"support conversations":          true,
		"my support conversations":       true,
		"previous support tickets":       true,
		"previous support conversations": true,
	}
	return genericQueries[normalized]
}

func supportSearchResults(conversations []*pb.DeckhandConversation, limit int) []SearchResult {
	results := make([]SearchResult, 0, len(conversations))
	for _, conv := range conversations {
		if conv == nil {
			continue
		}
		createdAt := ""
		if conv.CreatedAt != nil {
			createdAt = conv.CreatedAt.AsTime().Format("2006-01-02")
		}
		status := "unknown"
		switch conv.Status {
		case pb.ConversationStatus_CONVERSATION_STATUS_OPEN:
			status = "open"
		case pb.ConversationStatus_CONVERSATION_STATUS_RESOLVED:
			status = "resolved"
		case pb.ConversationStatus_CONVERSATION_STATUS_PENDING:
			status = "pending"
		}
		snippet := ""
		if conv.LastMessage != nil {
			snippet = conv.LastMessage.Content
			if len(snippet) > 180 {
				snippet = snippet[:180] + "..."
			}
		}
		results = append(results, SearchResult{
			ConversationID: conv.Id,
			Subject:        conv.Subject,
			Status:         status,
			CreatedAt:      createdAt,
			MatchedSnippet: snippet,
			Relevance:      "recent",
		})
		if len(results) >= limit {
			break
		}
	}
	return results
}
