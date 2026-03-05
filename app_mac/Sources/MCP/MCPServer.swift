import Foundation

// Local MCP server for AI tool integration (Claude, etc.)
// Exposes FrameWorks capabilities via JSON-RPC 2.0 over HTTP.
class MCPServer {
  static let shared = MCPServer()

  private var listener: Any?  // NWListener when implemented
  private(set) var port: UInt16 = 0
  private(set) var isRunning = false

  private init() {}

  func start() {
    // TODO: Implement NWListener-based HTTP server
    // - Listen on random available port
    // - Handle JSON-RPC 2.0 requests
    // - Route via MCPRouter
    // - Write config to ~/.config/frameworks/mcp.json
    isRunning = true
  }

  func stop() {
    isRunning = false
    listener = nil
  }

  func writeDiscoveryConfig() {
    let configDir = FileManager.default.homeDirectoryForCurrentUser
      .appendingPathComponent(".config/frameworks")
    try? FileManager.default.createDirectory(at: configDir, withIntermediateDirectories: true)

    let config: [String: Any] = [
      "mcpServers": [
        "frameworks": [
          "url": "http://localhost:\(port)/mcp",
          "transport": "http",
        ]
      ]
    ]

    if let data = try? JSONSerialization.data(withJSONObject: config, options: .prettyPrinted) {
      try? data.write(to: configDir.appendingPathComponent("mcp.json"))
    }
  }
}
