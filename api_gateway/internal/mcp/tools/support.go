package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterSupportTools registers support-related MCP tools.
func RegisterSupportTools(server *mcp.Server, serviceClients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// search_support_history - Search past support conversations
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "search_support_history",
			Description: "Search past support conversations by keyword. Useful for finding previous solutions to similar issues.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SearchSupportHistoryInput) (*mcp.CallToolResult, any, error) {
			return handleSearchSupportHistory(ctx, args, serviceClients, logger)
		},
	)
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
	Query      string         `json:"query"`
	TotalFound int            `json:"total_found"`
	Results    []SearchResult `json:"results"`
	Hint       string         `json:"hint,omitempty"`
}

func handleSearchSupportHistory(ctx context.Context, args SearchSupportHistoryInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	if args.Query == "" {
		return toolError("query is required")
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	resp, err := serviceClients.Deckhand.SearchConversations(ctx, tenantID, args.Query, 1, int32(limit))
	if err != nil {
		logger.WithError(err).Warn("Failed to search conversations")
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
		TotalFound: len(results),
		Results:    results,
	}
	if resp.TotalCount > 0 {
		result.TotalFound = int(resp.TotalCount)
	}

	if len(results) == 0 {
		result.Hint = "No matching conversations found. Try different keywords or check the full conversation list using support://conversations resource."
	} else if len(results) >= limit {
		result.Hint = fmt.Sprintf("Showing first %d results. Refine your query for more specific results.", limit)
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
