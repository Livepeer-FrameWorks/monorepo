package chat

type ToolDefinition struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolDefinitions contains only local tools handled directly by Skipper.
// Platform tools (diagnostics, streams, billing) are discovered from
// the Gateway MCP at startup.
var ToolDefinitions = []ToolDefinition{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "search_knowledge",
			Description: "Search the Skipper knowledge base for platform-specific guidance and verified docs.",
			Parameters: toolParams(
				map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query to run against the knowledge base.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default 8).",
					},
					"tenant_scope": map[string]any{
						"type":        "string",
						"description": "Scope to search: tenant, global, or all (default all).",
					},
				},
				[]string{"query"},
			),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "search_web",
			Description: "Search the public web for documentation or references when the knowledge base is insufficient.",
			Parameters: toolParams(
				map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query to run against the web.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of results to return (default 8).",
					},
					"search_depth": map[string]any{
						"type":        "string",
						"description": "Search depth: basic or advanced (default basic).",
					},
				},
				[]string{"query"},
			),
		},
	},
}

func toolParams(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}
