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
						"description": "Maximum number of results to return (default 5).",
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
						"description": "Maximum number of results to return (default 5).",
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
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "diagnose_rebuffering",
			Description: "Analyze rebuffering events for a stream and suggest remediation steps.",
			Parameters:  streamDiagnosticParams(),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "diagnose_buffer_health",
			Description: "Analyze buffer health and state transitions to identify dry buffer events and quality fluctuations.",
			Parameters:  streamDiagnosticParams(),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "diagnose_packet_loss",
			Description: "Analyze packet loss for a stream with protocol-aware guidance.",
			Parameters:  streamDiagnosticParams(),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "diagnose_routing",
			Description: "Analyze CDN routing decisions for a stream and highlight geographic distribution patterns.",
			Parameters:  streamDiagnosticParams(),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_stream_health_summary",
			Description: "Get aggregated health metrics for a stream over a time range.",
			Parameters:  streamDiagnosticParams(),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_anomaly_report",
			Description: "Detect anomalies across stream metrics by comparing recent performance to baseline.",
			Parameters: toolParams(
				map[string]any{
					"stream_id": map[string]any{
						"type":        "string",
						"description": "Relay ID or stream_id to analyze.",
					},
					"sensitivity": map[string]any{
						"type":        "string",
						"description": "Anomaly sensitivity: low, medium (default), high.",
					},
				},
				[]string{"stream_id"},
			),
		},
	},
}

func streamDiagnosticParams() map[string]any {
	return toolParams(
		map[string]any{
			"stream_id": map[string]any{
				"type":        "string",
				"description": "Relay ID or stream_id to analyze.",
			},
			"time_range": map[string]any{
				"type":        "string",
				"description": "Time range: last_1h (default), last_6h, last_24h, last_7d.",
			},
		},
		[]string{"stream_id"},
	)
}

func toolParams(properties map[string]any, required []string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}
