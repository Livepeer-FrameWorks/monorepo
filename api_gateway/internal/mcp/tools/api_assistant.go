package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/introspection"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	introspectionClient *introspection.Client
	templateLoader      *introspection.TemplateLoader
)

func init() {
	// Initialize introspection client with GraphQL URL from environment
	graphqlURL := os.Getenv("GRAPHQL_URL")
	if graphqlURL == "" {
		graphqlURL = "http://localhost:8080/graphql/"
	}
	introspectionClient = introspection.NewClient(graphqlURL, nil)

	// Initialize and load templates
	templateLoader = introspection.NewTemplateLoader()
	if err := templateLoader.Load(); err != nil {
		// Log but don't fail - templates are optional
		fmt.Fprintf(os.Stderr, "Warning: failed to load templates: %v\n", err)
	}
}

// RegisterAPIAssistantTools registers API integration assistant tools.
func RegisterAPIAssistantTools(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	// Update the introspection client with logger
	graphqlURL := os.Getenv("GRAPHQL_URL")
	if graphqlURL == "" {
		graphqlURL = "http://localhost:8080/graphql/"
	}
	introspectionClient = introspection.NewClient(graphqlURL, logger)

	// introspect_schema - Progressive schema discovery
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "introspect_schema",
			Description: "Explore the GraphQL API schema progressively. Use to discover available queries, mutations, subscriptions, or inspect specific types.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args IntrospectSchemaInput) (*mcp.CallToolResult, any, error) {
			return handleIntrospectSchema(ctx, args, logger)
		},
	)

	// generate_query - Generate ready-to-use queries
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "generate_query",
			Description: "Generate a ready-to-use GraphQL query for a specific field path using real templates from the codebase. Returns an error if no template matches.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GenerateQueryInput) (*mcp.CallToolResult, any, error) {
			return handleGenerateQuery(ctx, args, logger)
		},
	)

	// execute_query - Execute a GraphQL query
	mcp.AddTool(server,
		&mcp.Tool{
			Name:        "execute_query",
			Description: "Execute a GraphQL query or mutation against the API. Use generate_query or introspect_schema first to discover available fields. Authorization is enforced by the API — results are scoped to the caller's tenant.",
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ExecuteQueryInput) (*mcp.CallToolResult, any, error) {
			return handleExecuteQuery(ctx, args, logger)
		},
	)
}

// IntrospectSchemaInput represents input for introspect_schema tool.
type IntrospectSchemaInput struct {
	Focus string `json:"focus" jsonschema:"required" jsonschema_description:"What to explore: query mutation subscription or a type name"`
	Depth int    `json:"depth,omitempty" jsonschema_description:"Exploration depth 1-4 (default 2). 1=field names only 2=+args 3=+nested types 4=full details"`
}

// IntrospectSchemaResult is the result of schema introspection.
type IntrospectSchemaResult struct {
	Focus  string                       `json:"focus"`
	Depth  int                          `json:"depth"`
	Fields []introspection.FieldSummary `json:"fields,omitempty"`
	Type   *introspection.TypeSummary   `json:"type,omitempty"`
	Hint   string                       `json:"hint"`
}

func handleIntrospectSchema(ctx context.Context, args IntrospectSchemaInput, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	depth := args.Depth
	if depth < 1 || depth > 4 {
		depth = 2
	}

	focus := strings.ToLower(args.Focus)
	var result IntrospectSchemaResult
	result.Focus = args.Focus
	result.Depth = depth

	switch focus {
	case "query", "mutation", "subscription":
		fields, err := introspectionClient.GetRootFields(ctx, focus, depth)
		if err != nil {
			return toolError(fmt.Sprintf("Failed to introspect %s type: %v", focus, err))
		}
		result.Fields = fields
		result.Hint = fmt.Sprintf("Found %d %s fields. Use generate_query to get a ready-to-use query for any field.", len(fields), focus)

	default:
		// Treat as type name
		typeSummary, err := introspectionClient.GetType(ctx, args.Focus, depth)
		if err != nil {
			return toolError(fmt.Sprintf("Type not found: %s. Try focus='query' to see available queries.", args.Focus))
		}
		result.Type = typeSummary
		result.Hint = fmt.Sprintf("Type %s (%s) with %d fields.", typeSummary.Name, typeSummary.Kind, len(typeSummary.Fields))
	}

	return toolSuccessJSON(result)
}

// GenerateQueryInput represents input for generate_query tool.
type GenerateQueryInput struct {
	FieldName     string `json:"field_name,omitempty" jsonschema_description:"The field to generate a query for (e.g. streamsConnection). Use field_path for nested fields."`
	FieldPath     string `json:"field_path,omitempty" jsonschema_description:"Dot-separated path for nested fields (e.g. analytics.usage.streaming.viewerHoursHourlyConnection)"`
	OperationType string `json:"operation_type,omitempty" jsonschema_description:"query mutation or subscription (default: query)"`
}

// GenerateQueryResult is the result of query generation.
type GenerateQueryResult struct {
	Query     string                 `json:"query"`
	Variables map[string]interface{} `json:"variables"`
	Source    string                 `json:"source"`
	Hints     []string               `json:"hints,omitempty"`
}

func handleGenerateQuery(ctx context.Context, args GenerateQueryInput, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	opType := args.OperationType
	if opType == "" {
		opType = "query"
	}
	opType = strings.ToLower(opType)

	fieldPath := strings.TrimSpace(args.FieldPath)
	if fieldPath == "" {
		fieldPath = strings.TrimSpace(args.FieldName)
	}
	if fieldPath == "" {
		return toolError("field_name or field_path is required")
	}

	// Try template first
	template := templateLoader.FindByField(opType, fieldPath)
	if template != nil {
		result := GenerateQueryResult{
			Query:     template.Query,
			Variables: template.Variables,
			Source:    "template",
			Hints:     getQueryHints(template.Query),
		}
		return toolSuccessJSON(result)
	}

	if _, err := introspectionClient.FindFieldPath(ctx, opType, fieldPath, 2); err != nil {
		fields, listErr := introspectionClient.GetRootFields(ctx, opType, 1)
		if listErr != nil {
			return toolError(fmt.Sprintf("Failed to validate field path: %v", err))
		}

		suggestions := findSimilarFields(fieldPath, fields)
		msg := fmt.Sprintf("Field path '%s' not found in %s type.", fieldPath, opType)
		if len(suggestions) > 0 {
			msg += " Did you mean: " + strings.Join(suggestions, ", ") + "?"
		}
		return toolError(msg)
	}

	return toolError(fmt.Sprintf("No template found for %s.%s. Use introspect_schema to explore the schema or update templates in pkg/graphql/operations.", opType, fieldPath))
}

// ExecuteQueryInput represents input for execute_query tool.
type ExecuteQueryInput struct {
	Query     string         `json:"query" jsonschema:"required" jsonschema_description:"GraphQL query or mutation to execute"`
	Variables map[string]any `json:"variables,omitempty" jsonschema_description:"Variables for the query"`
}

func handleExecuteQuery(ctx context.Context, args ExecuteQueryInput, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	query := strings.TrimSpace(args.Query)
	if query == "" {
		return toolError("query is required")
	}

	result, err := introspectionClient.ExecuteQuery(ctx, query, args.Variables)
	if err != nil {
		return toolError(fmt.Sprintf("Query execution failed: %v", err))
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(result)},
		},
	}, result, nil
}

// findSimilarFields finds fields with similar names.
func findSimilarFields(target string, fields []introspection.FieldSummary) []string {
	target = strings.ToLower(target)
	var matches []string

	for _, f := range fields {
		if strings.Contains(strings.ToLower(f.Name), target) ||
			strings.Contains(target, strings.ToLower(f.Name)) {
			matches = append(matches, f.Name)
			if len(matches) >= 5 {
				break
			}
		}
	}

	return matches
}

// getQueryHints returns helpful hints based on query content.
func getQueryHints(query string) []string {
	var hints []string
	lower := strings.ToLower(query)

	if strings.Contains(lower, "connection") {
		hints = append(hints, "This is a paginated connection. Use page: { first: N, after: cursor } for pagination.")
	}
	if strings.Contains(lower, "analytics") {
		hints = append(hints, "Analytics queries benefit from timeRange filtering.")
	}
	if strings.Contains(lower, "streamid") {
		hints = append(hints, "streamId is a Relay global ID (not the public UUID).")
	}
	if strings.Contains(lower, "subscription") {
		hints = append(hints, "Subscriptions require WebSocket connection to /graphql/ws")
	}

	return hints
}
