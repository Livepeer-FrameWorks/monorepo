import Foundation

// Routes MCP tool calls to the appropriate service.
enum MCPRouter {
  static func handleToolCall(name: String, arguments: [String: Any]) async throws -> Any {
    switch name {
    case "list_streams":
      return try await StreamService.shared.listStreams()

    case "get_analytics":
      return try await AnalyticsService.shared.fetchOverview()

    case "list_nodes":
      return try await ClusterService.shared.listNodes()

    case "edge_status":
      return try await EdgeClient.shared.fetchStatus()

    case "edge_health":
      return try await EdgeClient.shared.fetchHealth()

    case "edge_streams":
      return try await EdgeClient.shared.fetchStreams()

    case "edge_metrics":
      return try await EdgeClient.shared.fetchMetrics()

    case "ask_consultant":
      let question = arguments["question"] as? String ?? ""
      let mode = arguments["mode"] as? String ?? "full"
      return try await SkipperService.shared.ask(question: question, mode: mode)

    default:
      throw MCPError.unknownTool(name)
    }
  }

  static var toolManifest: [[String: Any]] {
    [
      ["name": "list_streams", "description": "List all streams"],
      ["name": "get_analytics", "description": "Get analytics overview"],
      ["name": "list_nodes", "description": "List cluster nodes"],
      ["name": "edge_status", "description": "Get local edge node status"],
      ["name": "edge_health", "description": "Get local edge node health"],
      ["name": "edge_streams", "description": "Get active edge streams"],
      ["name": "edge_metrics", "description": "Get edge bandwidth and resource metrics"],
      [
        "name": "ask_consultant",
        "description": "Ask the Skipper AI consultant a question",
        "inputSchema": [
          "type": "object",
          "properties": [
            "question": ["type": "string", "description": "The question to ask"],
            "mode": ["type": "string", "enum": ["full", "docs"], "default": "full"],
          ],
          "required": ["question"],
        ],
      ],
    ]
  }
}

enum MCPError: LocalizedError {
  case unknownTool(String)

  var errorDescription: String? {
    switch self {
    case .unknownTool(let name): return "Unknown MCP tool: \(name)"
    }
  }
}
