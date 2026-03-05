import Foundation
import Network

class MCPServer {
  static let shared = MCPServer()

  private var listener: NWListener?
  private(set) var port: UInt16 = 0
  private(set) var isRunning = false
  private let queue = DispatchQueue(label: "com.livepeer.frameworks.mcp", qos: .userInitiated)

  private init() {}

  func start() {
    guard !isRunning else { return }

    do {
      let params = NWParameters.tcp
      params.allowLocalEndpointReuse = true
      listener = try NWListener(using: params, on: .any)
    } catch {
      print("[MCP] Failed to create listener: \(error)")
      return
    }

    listener?.stateUpdateHandler = { [weak self] state in
      switch state {
      case .ready:
        guard let self = self, let port = self.listener?.port?.rawValue else { return }
        self.port = port
        self.isRunning = true
        self.writeDiscoveryConfig()
        print("[MCP] Server listening on localhost:\(port)")
      case .failed(let error):
        print("[MCP] Listener failed: \(error)")
        self?.cleanup()
      case .cancelled:
        self?.cleanup()
      default:
        break
      }
    }

    listener?.newConnectionHandler = { [weak self] connection in
      self?.handleConnection(connection)
    }

    listener?.start(queue: queue)
  }

  func stop() {
    listener?.cancel()
    cleanup()
  }

  private func cleanup() {
    isRunning = false
    port = 0
    listener = nil
    removeDiscoveryConfig()
  }

  // MARK: - Connection Handling

  private func handleConnection(_ connection: NWConnection) {
    connection.start(queue: queue)
    receiveHTTPRequest(connection)
  }

  private func receiveHTTPRequest(_ connection: NWConnection) {
    connection.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] data, _, isComplete, error in
      guard let self = self, let data = data, !data.isEmpty else {
        connection.cancel()
        return
      }

      guard let request = self.parseHTTPRequest(data) else {
        self.sendHTTPResponse(connection, status: 400, body: ["error": "Bad request"])
        return
      }

      if request.method == "GET" && request.path == "/health" {
        self.sendHTTPResponse(connection, status: 200, body: ["status": "ok"])
        return
      }

      guard request.method == "POST" else {
        self.sendHTTPResponse(connection, status: 405, body: ["error": "Method not allowed"])
        return
      }

      Task {
        let response = await MCPProtocol.handle(request.body)
        self.sendHTTPResponse(connection, status: 200, body: response)
      }
    }
  }

  // MARK: - HTTP Parsing

  private struct HTTPRequest {
    let method: String
    let path: String
    let body: [String: Any]
  }

  private func parseHTTPRequest(_ data: Data) -> HTTPRequest? {
    guard let raw = String(data: data, encoding: .utf8) else { return nil }

    let parts = raw.components(separatedBy: "\r\n\r\n")
    guard let headerSection = parts.first else { return nil }

    let headerLines = headerSection.components(separatedBy: "\r\n")
    guard let requestLine = headerLines.first else { return nil }

    let tokens = requestLine.split(separator: " ")
    guard tokens.count >= 2 else { return nil }

    let method = String(tokens[0])
    let path = String(tokens[1])

    var body: [String: Any] = [:]
    if parts.count > 1 {
      let bodyStr = parts.dropFirst().joined(separator: "\r\n\r\n")
      if let bodyData = bodyStr.data(using: .utf8),
         let json = try? JSONSerialization.jsonObject(with: bodyData) as? [String: Any] {
        body = json
      }
    }

    return HTTPRequest(method: method, path: path, body: body)
  }

  // MARK: - HTTP Response

  private func sendHTTPResponse(_ connection: NWConnection, status: Int, body: Any) {
    let statusText: String
    switch status {
    case 200: statusText = "OK"
    case 400: statusText = "Bad Request"
    case 405: statusText = "Method Not Allowed"
    default: statusText = "Error"
    }

    guard let jsonData = try? JSONSerialization.data(withJSONObject: body, options: .sortedKeys) else {
      connection.cancel()
      return
    }

    let headers = [
      "HTTP/1.1 \(status) \(statusText)",
      "Content-Type: application/json",
      "Content-Length: \(jsonData.count)",
      "Connection: close",
      "",
      "",
    ].joined(separator: "\r\n")

    var responseData = headers.data(using: .utf8)!
    responseData.append(jsonData)

    connection.send(content: responseData, completion: .contentProcessed { _ in
      connection.cancel()
    })
  }

  // MARK: - Discovery Config

  func writeDiscoveryConfig() {
    let configDir = FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent(".config/frameworks")
    try? FileManager.default.createDirectory(at: configDir, withIntermediateDirectories: true)

    let config: [String: Any] = [
      "mcpServers": [
        "frameworks-desktop": [
          "url": "http://localhost:\(port)/mcp",
          "transport": "http",
        ]
      ]
    ]

    if let data = try? JSONSerialization.data(withJSONObject: config, options: .prettyPrinted) {
      try? data.write(to: configDir.appendingPathComponent("mcp.json"))
    }
  }

  private func removeDiscoveryConfig() {
    let configPath = FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent(".config/frameworks/mcp.json")
    try? FileManager.default.removeItem(at: configPath)
  }
}
