import Foundation

// Handles local MCP resources — edge node state exposed as readable resources.
// Platform resources (streams://, analytics://, etc.) are forwarded to gateway by MCPProtocol.
enum MCPResources {

  private static let localPrefixes = ["edge://"]

  static func isLocalResource(_ uri: String) -> Bool {
    localPrefixes.contains(where: { uri.hasPrefix($0) })
  }

  static var localResourceManifest: [[String: Any]] {
    [
      ["uri": "edge://status", "name": "Edge Status", "description": "Current edge node operational status", "mimeType": "application/json"],
      ["uri": "edge://health", "name": "Edge Health", "description": "Edge node health check", "mimeType": "application/json"],
      ["uri": "edge://streams", "name": "Edge Streams", "description": "Active streams on this edge node", "mimeType": "application/json"],
      ["uri": "edge://metrics", "name": "Edge Metrics", "description": "Edge bandwidth, CPU, memory metrics", "mimeType": "application/json"],
      ["uri": "edge://clients", "name": "Edge Clients", "description": "Connected viewers on this edge node", "mimeType": "application/json"],
      ["uri": "edge://nodes", "name": "Infrastructure Nodes", "description": "Available edge nodes from the platform", "mimeType": "application/json"],
    ]
  }

  static func read(uri: String) async -> [[String: Any]] {
    if uri == "edge://nodes" {
      return await readNodes(uri: uri)
    }

    let path: String
    switch uri {
    case "edge://status": path = "/api/edge/status"
    case "edge://health": path = "/api/edge/health"
    case "edge://streams": path = "/api/edge/streams"
    case "edge://metrics": path = "/api/edge/metrics"
    case "edge://clients": path = "/api/edge/clients"
    default:
      return [["uri": uri, "mimeType": "application/json", "text": "{\"error\": \"Unknown resource\"}"]]
    }

    do {
      let data = try await fetchEdgeRaw(path)
      return [["uri": uri, "mimeType": "application/json", "text": data]]
    } catch {
      return [["uri": uri, "mimeType": "application/json", "text": "{\"error\": \"\(error.localizedDescription)\"}"]]
    }
  }

  private static func readNodes(uri: String) async -> [[String: Any]] {
    do {
      let nodes = try await ClusterService.shared.listNodes()
      let items: [[String: Any]] = nodes.map { node in
        var dict: [String: Any] = [
          "nodeId": node.nodeId, "nodeName": node.nodeName,
          "clusterId": node.clusterId, "nodeType": node.nodeType,
        ]
        if let r = node.region { dict["region"] = r }
        if let ip = node.externalIp { dict["externalIp"] = ip }
        if let ip = node.internalIp { dict["internalIp"] = ip }
        if let ls = node.liveState, let h = ls.isHealthy { dict["isHealthy"] = h }
        return dict
      }
      if let data = try? JSONSerialization.data(withJSONObject: items, options: .sortedKeys),
         let text = String(data: data, encoding: .utf8) {
        return [["uri": uri, "mimeType": "application/json", "text": text]]
      }
      return [["uri": uri, "mimeType": "application/json", "text": "[]"]]
    } catch {
      return [["uri": uri, "mimeType": "application/json", "text": "{\"error\": \"\(error.localizedDescription)\"}"]]
    }
  }

  private static func fetchEdgeRaw(_ path: String) async throws -> String {
    let baseURL = EdgeClient.shared.baseURL
    guard let url = URL(string: baseURL + path) else { throw EdgeError.invalidURL }

    var request = URLRequest(url: url)
    request.httpMethod = "GET"
    request.setValue("FrameWorks-Desktop/1.0", forHTTPHeaderField: "User-Agent")
    if let token = GatewayClient.shared.accessToken {
      request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    let (data, _) = try await URLSession.shared.data(for: request)
    return String(data: data, encoding: .utf8) ?? "{}"
  }
}
