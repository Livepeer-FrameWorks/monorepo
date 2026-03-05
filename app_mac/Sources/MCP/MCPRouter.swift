import Foundation

// Routes MCP tool calls — local edge tools handled directly via EdgeClient,
// everything else forwarded to gateway MCP by MCPProtocol.
enum MCPRouter {

  private static let localTools: Set<String> = [
    "edge_status", "edge_health", "edge_streams",
    "edge_stream_detail", "edge_clients", "edge_metrics",
    "edge_service_control", "list_nodes",
  ]

  static func isLocalTool(_ name: String) -> Bool {
    localTools.contains(name)
  }

  static func handleToolCall(name: String, arguments: [String: Any]) async throws -> Any {
    switch name {
    case "edge_status":
      let base = try await resolveBaseURL(arguments)
      let result: EdgeStatusResponse = try await EdgeClient.fetch("/api/edge/status", from: base)
      return result
    case "edge_health":
      let base = try await resolveBaseURL(arguments)
      let result: EdgeHealthResponse = try await EdgeClient.fetch("/api/edge/health", from: base)
      return result
    case "edge_streams":
      let base = try await resolveBaseURL(arguments)
      let result: EdgeStreamsResponse = try await EdgeClient.fetch("/api/edge/streams", from: base)
      return result
    case "edge_stream_detail":
      let streamName = arguments["stream_name"] as? String ?? ""
      let base = try await resolveBaseURL(arguments)
      let result: EdgeStreamDetailResponse = try await EdgeClient.fetch("/api/edge/streams/\(streamName)", from: base)
      return result
    case "edge_clients":
      let base = try await resolveBaseURL(arguments)
      let result: EdgeClientsResponse = try await EdgeClient.fetch("/api/edge/clients", from: base)
      return result
    case "edge_metrics":
      let base = try await resolveBaseURL(arguments)
      let result: EdgeMetricsResponse = try await EdgeClient.fetch("/api/edge/metrics", from: base)
      return result
    case "edge_service_control":
      let action = arguments["action"] as? String ?? ""
      let service = arguments["service"] as? String ?? ""
      return try edgeServiceControl(action: action, service: service)
    case "list_nodes":
      let clusterId = arguments["cluster_id"] as? String
      let nodes = try await ClusterService.shared.listNodes(clusterId: clusterId)
      return nodes.map { node -> [String: Any] in
        var dict: [String: Any] = [
          "nodeId": node.nodeId,
          "nodeName": node.nodeName,
          "clusterId": node.clusterId,
          "nodeType": node.nodeType,
        ]
        if let r = node.region { dict["region"] = r }
        if let ip = node.externalIp { dict["externalIp"] = ip }
        if let ip = node.internalIp { dict["internalIp"] = ip }
        if let ls = node.liveState {
          var live: [String: Any] = [:]
          if let h = ls.isHealthy { live["isHealthy"] = h }
          if let s = ls.activeStreams { live["activeStreams"] = s }
          if let c = ls.cpuPercent { live["cpuPercent"] = c }
          dict["liveState"] = live
        }
        return dict
      }
    default:
      throw MCPError.unknownTool(name)
    }
  }

  // MARK: - Node Resolution

  private static func resolveBaseURL(_ arguments: [String: Any]) async throws -> String {
    guard let nodeId = arguments["node_id"] as? String else {
      return EdgeClient.shared.baseURL
    }
    let nodes = try await ClusterService.shared.listNodes()
    guard let node = nodes.first(where: { $0.nodeId == nodeId }),
          let url = node.helmsmanURL else {
      throw MCPError.invalidArgument("Node not found or has no IP: \(nodeId)")
    }
    return url
  }

  // MARK: - Tool Manifest

  private static let nodeIdProp: [String: Any] = [
    "type": "string", "description": "Target node ID (omit for local edge)",
  ]

  static var localToolManifest: [[String: Any]] {
    [
      tool("edge_status", "Get edge node status (operational mode, uptime, version)",
           properties: ["node_id": nodeIdProp]),
      tool("edge_health", "Check edge node health (service states, connectivity)",
           properties: ["node_id": nodeIdProp]),
      tool("edge_streams", "List active streams on an edge node",
           properties: ["node_id": nodeIdProp]),
      tool("edge_stream_detail", "Get detailed info for a specific edge stream",
           properties: [
             "stream_name": ["type": "string", "description": "Stream name"],
             "node_id": nodeIdProp,
           ],
           required: ["stream_name"]),
      tool("edge_clients", "List connected viewers/clients on an edge node",
           properties: ["node_id": nodeIdProp]),
      tool("edge_metrics", "Get edge bandwidth, CPU, memory, and connection metrics",
           properties: ["node_id": nodeIdProp]),
      tool("edge_service_control", "Start, stop, or restart local edge services (helmsman, mistserver, caddy)",
           properties: [
             "action": ["type": "string", "enum": ["start", "stop", "restart"], "description": "Action to perform"],
             "service": ["type": "string", "enum": ["helmsman", "mistserver", "caddy", "all"], "description": "Which service"],
           ],
           required: ["action", "service"]),
      tool("list_nodes", "List infrastructure nodes (edge servers) from the platform",
           properties: [
             "cluster_id": ["type": "string", "description": "Filter by cluster ID"],
           ]),
    ]
  }

  // MARK: - Service Control

  private static func edgeServiceControl(action: String, service: String) throws -> Any {
    let services: [EdgeService]
    if service == "all" {
      services = EdgeService.allCases
    } else {
      guard let svc = EdgeService(rawValue: service) else {
        throw MCPError.invalidArgument("Unknown service: \(service)")
      }
      services = [svc]
    }

    var results: [[String: Any]] = []
    for svc in services {
      let success: Bool
      switch action {
      case "start": success = ServiceManager.start(svc)
      case "stop": success = ServiceManager.stop(svc)
      case "restart":
        _ = ServiceManager.stop(svc)
        success = ServiceManager.start(svc)
      default:
        throw MCPError.invalidArgument("Unknown action: \(action)")
      }
      results.append(["service": svc.rawValue, "action": action, "success": success])
    }
    return results
  }

  // MARK: - Helpers

  private static func tool(
    _ name: String, _ description: String,
    properties: [String: Any]? = nil,
    required: [String]? = nil
  ) -> [String: Any] {
    var t: [String: Any] = ["name": name, "description": description]
    if let properties = properties {
      var schema: [String: Any] = ["type": "object", "properties": properties]
      if let required = required { schema["required"] = required }
      t["inputSchema"] = schema
    } else {
      t["inputSchema"] = ["type": "object"]
    }
    return t
  }
}

enum MCPError: LocalizedError {
  case unknownTool(String)
  case invalidArgument(String)

  var errorDescription: String? {
    switch self {
    case .unknownTool(let name): return "Unknown MCP tool: \(name)"
    case .invalidArgument(let msg): return "Invalid argument: \(msg)"
    }
  }
}
