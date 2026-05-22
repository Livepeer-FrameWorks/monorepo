import Foundation

class EdgeClient {
  static let shared = EdgeClient()

  var baseURL = "http://localhost:18007"
  private let session: URLSession

  private init() {
    let config = URLSessionConfiguration.default
    config.timeoutIntervalForRequest = 5
    session = URLSession(configuration: config)
  }

  func isReachable() async -> Bool {
    do {
      let _: EdgeHealthResponse = try await get("/api/edge/health")
      return true
    } catch {
      return false
    }
  }

  func fetchStatus() async throws -> EdgeStatusResponse {
    try await get("/api/edge/status")
  }

  func fetchHealth() async throws -> EdgeHealthResponse {
    try await get("/api/edge/health")
  }

  func fetchStreams() async throws -> EdgeStreamsResponse {
    try await get("/api/edge/streams")
  }

  func fetchMetrics() async throws -> EdgeMetricsResponse {
    try await get("/api/edge/metrics")
  }

  func fetchStreamDetail(_ streamName: String) async throws -> EdgeStreamDetailResponse {
    try await get("/api/edge/streams/\(Self.pathComponent(streamName))")
  }

  func fetchClients() async throws -> EdgeClientsResponse {
    try await get("/api/edge/clients")
  }

  // Fetch from an arbitrary helmsman instance (for targeting remote nodes)
  static func fetch<T: Decodable>(_ path: String, from targetURL: String) async throws -> T {
    guard let url = URL(string: targetURL + path) else {
      throw EdgeError.invalidURL
    }

    var request = URLRequest(url: url)
    request.httpMethod = "GET"
    request.timeoutInterval = 5
    request.setValue("FrameWorks-Desktop/1.0", forHTTPHeaderField: "User-Agent")

    if let token = GatewayClient.shared.accessToken {
      request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    let (data, response) = try await URLSession.shared.data(for: request)

    guard let httpResponse = response as? HTTPURLResponse else {
      throw EdgeError.requestFailed(0, nil)
    }
    guard (200...299).contains(httpResponse.statusCode) else {
      throw EdgeError.requestFailed(httpResponse.statusCode, String(data: data, encoding: .utf8))
    }

    return try JSONDecoder().decode(T.self, from: data)
  }

  static func pathComponent(_ value: String) -> String {
    var allowed = CharacterSet.urlPathAllowed
    allowed.remove(charactersIn: "/")
    return value.addingPercentEncoding(withAllowedCharacters: allowed) ?? value
  }

  private func get<T: Decodable>(_ path: String) async throws -> T {
    guard let url = URL(string: baseURL + path) else {
      throw EdgeError.invalidURL
    }

    var request = URLRequest(url: url)
    request.httpMethod = "GET"
    request.setValue("FrameWorks-Desktop/1.0", forHTTPHeaderField: "User-Agent")

    if let token = GatewayClient.shared.accessToken {
      request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    let (data, response) = try await session.data(for: request)

    guard let httpResponse = response as? HTTPURLResponse else {
      throw EdgeError.requestFailed(0, nil)
    }
    guard (200...299).contains(httpResponse.statusCode) else {
      throw EdgeError.requestFailed(httpResponse.statusCode, String(data: data, encoding: .utf8))
    }

    return try JSONDecoder().decode(T.self, from: data)
  }
}

enum EdgeError: LocalizedError {
  case invalidURL
  case requestFailed(Int, String?)

  var errorDescription: String? {
    switch self {
    case .invalidURL: return "Invalid edge URL"
    case .requestFailed(let status, let body):
      let detail = body?.trimmingCharacters(in: .whitespacesAndNewlines)
      if status > 0, let detail, !detail.isEmpty {
        return "Edge request failed with HTTP \(status): \(detail)"
      }
      if status > 0 {
        return "Edge request failed with HTTP \(status)"
      }
      return "Edge request failed"
    }
  }
}
