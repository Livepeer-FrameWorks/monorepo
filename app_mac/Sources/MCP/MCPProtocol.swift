import Foundation

// Handles MCP JSON-RPC 2.0 requests.
// Acts as a spoke to the gateway MCP server, forwarding most requests
// and injecting local-only tools for edge service management.
enum MCPProtocol {

  static func handle(_ request: [String: Any]) async -> [String: Any] {
    guard let method = request["method"] as? String else {
      return errorResponse(id: request["id"], code: -32600, message: "Invalid request")
    }

    let id = request["id"]
    let params = request["params"] as? [String: Any] ?? [:]

    switch method {
    case "initialize":
      return initializeResponse(id: id)
    case "ping":
      return successResponse(id: id, result: [:])
    case "tools/list":
      return await toolsList(id: id)
    case "tools/call":
      return await toolsCall(id: id, params: params)
    case "resources/list":
      return await resourcesList(id: id)
    case "resources/read":
      return await resourcesRead(id: id, params: params)
    default:
      // Forward unknown methods to gateway MCP
      return await forwardToGateway(request)
    }
  }

  // MARK: - Initialize

  private static func initializeResponse(id: Any?) -> [String: Any] {
    successResponse(id: id, result: [
      "protocolVersion": "2024-11-05",
      "capabilities": [
        "tools": ["listChanged": false],
        "resources": ["subscribe": false, "listChanged": false],
      ],
      "serverInfo": [
        "name": "frameworks-desktop",
        "version": "0.0.1",
      ],
    ])
  }

  // MARK: - Tools

  private static func toolsList(id: Any?) async -> [String: Any] {
    // Get gateway tools + merge local-only tools
    var tools = await gatewayToolsList()
    tools.append(contentsOf: MCPRouter.localToolManifest)
    return successResponse(id: id, result: ["tools": tools])
  }

  private static func toolsCall(id: Any?, params: [String: Any]) async -> [String: Any] {
    guard let name = params["name"] as? String else {
      return errorResponse(id: id, code: -32602, message: "Missing tool name")
    }

    let arguments = params["arguments"] as? [String: Any] ?? [:]

    // Local-only tools handled directly
    if MCPRouter.isLocalTool(name) {
      do {
        let result = try await MCPRouter.handleToolCall(name: name, arguments: arguments)
        let text: String
        if let data = try? JSONSerialization.data(withJSONObject: result, options: .sortedKeys),
           let str = String(data: data, encoding: .utf8) {
          text = str
        } else {
          text = String(describing: result)
        }
        return successResponse(id: id, result: [
          "content": [["type": "text", "text": text]],
        ])
      } catch {
        return successResponse(id: id, result: [
          "content": [["type": "text", "text": "Error: \(error.localizedDescription)"]],
          "isError": true,
        ])
      }
    }

    // Forward to gateway MCP
    return await forwardToGateway([
      "jsonrpc": "2.0",
      "id": id as Any,
      "method": "tools/call",
      "params": params,
    ])
  }

  // MARK: - Resources

  private static func resourcesList(id: Any?) async -> [String: Any] {
    // Get gateway resources + merge local resources
    var resources = await gatewayResourcesList()
    resources.append(contentsOf: MCPResources.localResourceManifest)
    return successResponse(id: id, result: ["resources": resources])
  }

  private static func resourcesRead(id: Any?, params: [String: Any]) async -> [String: Any] {
    guard let uri = params["uri"] as? String else {
      return errorResponse(id: id, code: -32602, message: "Missing resource URI")
    }

    // Local resources handled directly
    if MCPResources.isLocalResource(uri) {
      let contents = await MCPResources.read(uri: uri)
      return successResponse(id: id, result: ["contents": contents])
    }

    // Forward to gateway
    return await forwardToGateway([
      "jsonrpc": "2.0",
      "id": id as Any,
      "method": "resources/read",
      "params": params,
    ])
  }

  // MARK: - Gateway Proxy

  private static func forwardToGateway(_ request: [String: Any]) async -> [String: Any] {
    let gateway = GatewayClient.shared
    guard let token = gateway.accessToken else {
      return errorResponse(id: request["id"], code: -32001, message: "Not authenticated to gateway")
    }

    let mcpURL = gateway.baseURL + "/mcp"
    guard let url = URL(string: mcpURL) else {
      return errorResponse(id: request["id"], code: -32002, message: "Invalid gateway URL")
    }

    guard let body = try? JSONSerialization.data(withJSONObject: request) else {
      return errorResponse(id: request["id"], code: -32603, message: "Failed to serialize request")
    }

    var urlRequest = URLRequest(url: url)
    urlRequest.httpMethod = "POST"
    urlRequest.httpBody = body
    urlRequest.setValue("application/json", forHTTPHeaderField: "Content-Type")
    urlRequest.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    urlRequest.setValue("FrameWorks-Desktop/1.0", forHTTPHeaderField: "User-Agent")
    urlRequest.timeoutInterval = 30

    do {
      let (data, response) = try await URLSession.shared.data(for: urlRequest)
      guard let http = response as? HTTPURLResponse, (200...299).contains(http.statusCode) else {
        return errorResponse(id: request["id"], code: -32003, message: "Gateway returned error")
      }
      if let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
        return json
      }
      return errorResponse(id: request["id"], code: -32603, message: "Invalid gateway response")
    } catch {
      return errorResponse(id: request["id"], code: -32000, message: "Gateway unreachable: \(error.localizedDescription)")
    }
  }

  private static func gatewayToolsList() async -> [[String: Any]] {
    let response = await forwardToGateway([
      "jsonrpc": "2.0",
      "id": "tools-list",
      "method": "tools/list",
      "params": [:] as [String: Any],
    ])
    if let result = response["result"] as? [String: Any],
       let tools = result["tools"] as? [[String: Any]] {
      return tools
    }
    return []
  }

  private static func gatewayResourcesList() async -> [[String: Any]] {
    let response = await forwardToGateway([
      "jsonrpc": "2.0",
      "id": "resources-list",
      "method": "resources/list",
      "params": [:] as [String: Any],
    ])
    if let result = response["result"] as? [String: Any],
       let resources = result["resources"] as? [[String: Any]] {
      return resources
    }
    return []
  }

  // MARK: - JSON-RPC Helpers

  private static func successResponse(id: Any?, result: [String: Any]) -> [String: Any] {
    ["jsonrpc": "2.0", "id": id as Any, "result": result]
  }

  private static func errorResponse(id: Any?, code: Int, message: String) -> [String: Any] {
    ["jsonrpc": "2.0", "id": id as Any, "error": ["code": code, "message": message]]
  }
}
