import Foundation

class GatewayClient {
  static let shared = GatewayClient()

  var baseURL: String = ""

  // Read the current tray credential after token rotation.
  var accessToken: String? { boundCredential("user_session") }

  func boundCredential(_ key: String) -> String? {
    guard Self.sameBridge(credentialLoader("user_session_bridge") ?? "", baseURL) else { return nil }
    return credentialLoader(key)
  }

  static func sameBridge(_ left: String, _ right: String) -> Bool {
    guard let left = canonicalBridge(left), let right = canonicalBridge(right) else { return false }
    return left == right
  }

  private static func canonicalBridge(_ value: String) -> String? {
    guard var url = URLComponents(string: value.trimmingCharacters(in: .whitespacesAndNewlines)),
      let scheme = url.scheme?.lowercased(), ["https", "http"].contains(scheme),
      let host = url.host?.lowercased(), !host.isEmpty, url.user == nil, url.password == nil,
      url.query == nil, url.fragment == nil
    else { return nil }
    url.scheme = scheme
    url.host = host
    if (scheme == "https" && url.port == 443) || (scheme == "http" && url.port == 80) { url.port = nil }
    while url.percentEncodedPath.hasSuffix("/") { url.percentEncodedPath.removeLast() }
    return url.string
  }

  private let session: URLSession
  private let credentialLoader: (String) -> String?

  init(configuration: URLSessionConfiguration = .default, credentialLoader: @escaping (String) -> String? = { KeychainHelper.load(key: $0) }) {
    self.credentialLoader = credentialLoader
    configuration.timeoutIntervalForRequest = 15
    session = URLSession(configuration: configuration, delegate: GatewayRedirectPolicy(), delegateQueue: nil)
  }

  // MARK: - REST

  func requestWithResponse(
    method: String,
    path: String,
    body: Data? = nil,
    authenticated: Bool = true,
    headers: [String: String] = [:]
  ) async throws -> (Data, HTTPURLResponse) {
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

    for (key, value) in headers {
      request.setValue(value, forHTTPHeaderField: key)
    }

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

    return (data, httpResponse)
  }

  func request(
    method: String,
    path: String,
    body: Data? = nil,
    authenticated: Bool = true,
    headers: [String: String] = [:]
  ) async throws -> Data {
    let (data, _) = try await requestWithResponse(
      method: method,
      path: path,
      body: body,
      authenticated: authenticated,
      headers: headers
    )
    return data
  }

  // MARK: - GraphQL

  func graphql<T: Decodable>(
    query: String,
    variables: [String: Any]? = nil,
    responseType: T.Type,
    partialData: GraphQLPartialDataPolicy = .strict
  ) async throws -> T {
    var payload: [String: Any] = ["query": query]
    if let variables = variables {
      payload["variables"] = variables
    }

    let body = try JSONSerialization.data(withJSONObject: payload)
    let data = try await request(method: "POST", path: "/graphql", body: body)

    let wrapper = try JSONDecoder().decode(GraphQLResponse<T>.self, from: data)
    return try wrapper.resolved(partialData: partialData)
  }
}

// Gateway requests carry credentials in headers and token-grant bodies. The
// configured Bridge is their destination even when it answers with a redirect.
private final class GatewayRedirectPolicy: NSObject, URLSessionTaskDelegate {
  func urlSession(
    _ session: URLSession, task: URLSessionTask,
    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
    completionHandler: @escaping (URLRequest?) -> Void
  ) {
    completionHandler(nil)
  }
}

// MARK: - Types

struct GraphQLResponse<T: Decodable>: Decodable {
  let data: T?
  let errors: [GraphQLError]?

  func resolved(partialData: GraphQLPartialDataPolicy = .strict) throws -> T {
    let rejected = (errors ?? []).filter { !partialData.allows($0) }
    if !rejected.isEmpty { throw GatewayError.graphqlErrors(rejected.map { $0.message }) }
    guard let data else { throw GatewayError.noData }
    return data
  }
}

enum GraphQLPartialDataPolicy {
  case strict
  case streamList

  func allows(_ error: GraphQLError) -> Bool {
    guard case .streamList = self, let path = error.path, path.count >= 5,
      path[0] == .field("streamsConnection"), path[1] == .field("edges"),
      case .index(let index) = path[2], index >= 0, path[3] == .field("node"),
      path[4] == .field("ingest") || path[4] == .field("metrics")
    else { return false }
    return true
  }
}

struct GraphQLError: Decodable {
  let message: String
  let path: [GraphQLPathComponent]?
}

enum GraphQLPathComponent: Decodable, Equatable {
  case field(String)
  case index(Int)

  init(from decoder: Decoder) throws {
    let value = try decoder.singleValueContainer()
    if let field = try? value.decode(String.self) { self = .field(field) }
    else { self = .index(try value.decode(Int.self)) }
  }
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
    case .httpError(let code, let data):
      if let message = HTTPErrorMessage.decode(from: data) {
        return "HTTP \(code): \(message)"
      }
      return "HTTP \(code)"
    case .graphqlErrors(let msgs): return msgs.joined(separator: ", ")
    case .noData: return "No data in response"
    }
  }
}

private struct HTTPErrorMessage: Decodable {
  let error: String?
  let message: String?

  static func decode(from data: Data) -> String? {
    if let decoded = try? JSONDecoder().decode(HTTPErrorMessage.self, from: data) {
      return decoded.error ?? decoded.message
    }
    return String(data: data, encoding: .utf8)
  }
}
