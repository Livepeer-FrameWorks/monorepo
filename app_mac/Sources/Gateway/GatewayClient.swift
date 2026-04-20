import Foundation

class GatewayClient {
  static let shared = GatewayClient()

  var baseURL: String = ""

  // Computed so the shared Keychain stays the single source of truth —
  // caching would let CLI logout go unnoticed until the next refresh.
  var accessToken: String? { KeychainHelper.load(key: "user_session") }

  private let session: URLSession

  private init() {
    let config = URLSessionConfiguration.default
    config.timeoutIntervalForRequest = 15
    session = URLSession(configuration: config)
  }

  // MARK: - REST

  func request(
    method: String,
    path: String,
    body: Data? = nil,
    authenticated: Bool = true
  ) async throws -> Data {
    let trimmed = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else {
      throw GatewayError.notConfigured
    }
    guard let url = URL(string: trimmed + path) else {
      throw GatewayError.invalidURL(path)
    }

    var request = URLRequest(url: url)
    request.httpMethod = method
    request.setValue("application/json", forHTTPHeaderField: "Content-Type")
    request.setValue("FrameWorks-Desktop/1.0", forHTTPHeaderField: "User-Agent")

    if authenticated, let token = accessToken {
      request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
    }

    if let body = body {
      request.httpBody = body
    }

    let (data, response) = try await session.data(for: request)

    guard let httpResponse = response as? HTTPURLResponse else {
      throw GatewayError.invalidResponse
    }

    guard (200...299).contains(httpResponse.statusCode) else {
      throw GatewayError.httpError(httpResponse.statusCode, data)
    }

    return data
  }

  // MARK: - GraphQL

  func graphql<T: Decodable>(
    query: String,
    variables: [String: Any]? = nil,
    responseType: T.Type
  ) async throws -> T {
    var payload: [String: Any] = ["query": query]
    if let variables = variables {
      payload["variables"] = variables
    }

    let body = try JSONSerialization.data(withJSONObject: payload)
    let data = try await request(method: "POST", path: "/graphql", body: body)

    let wrapper = try JSONDecoder().decode(GraphQLResponse<T>.self, from: data)
    if let errors = wrapper.errors, !errors.isEmpty {
      throw GatewayError.graphqlErrors(errors.map { $0.message })
    }
    guard let result = wrapper.data else {
      throw GatewayError.noData
    }
    return result
  }
}

// MARK: - Types

struct GraphQLResponse<T: Decodable>: Decodable {
  let data: T?
  let errors: [GraphQLError]?
}

struct GraphQLError: Decodable {
  let message: String
}

enum GatewayError: LocalizedError {
  case notConfigured
  case invalidURL(String)
  case invalidResponse
  case httpError(Int, Data)
  case graphqlErrors([String])
  case noData

  var errorDescription: String? {
    switch self {
    case .notConfigured: return "Gateway URL not configured — run 'frameworks setup' to create a context"
    case .invalidURL(let path): return "Invalid URL: \(path)"
    case .invalidResponse: return "Invalid response"
    case .httpError(let code, _): return "HTTP \(code)"
    case .graphqlErrors(let msgs): return msgs.joined(separator: ", ")
    case .noData: return "No data in response"
    }
  }
}
