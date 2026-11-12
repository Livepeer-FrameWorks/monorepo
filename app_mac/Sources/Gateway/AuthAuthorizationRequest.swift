import Foundation

struct AuthAuthorizationRequest {
  typealias Transport = (
    _ path: String, _ body: Data, _ headers: [String: String]
  ) async throws -> (Data, HTTPURLResponse)

  var transport: Transport

  func start(redirectURI: String, codeChallenge: String, state: String) async throws -> String? {
    let body = try JSONSerialization.data(withJSONObject: [
      "client_id": "tray-mac", "redirect_uri": redirectURI,
      "code_challenge": codeChallenge, "code_challenge_method": "S256",
      "scope": "account", "state": state,
    ])
    let data: Data
    do {
      (data, _) = try await transport("/auth/authorize/start", body, Self.clientHeaders())
    } catch let error as GatewayError {
      // Older self-hosted Bridges have no request-preview endpoint.
      guard case .httpError(let status, _) = error, [404, 405, 501].contains(status) else {
        throw error
      }
      return nil
    }
    let response = try JSONDecoder().decode(Response.self, from: data)
    guard !response.requestContext.isEmpty, response.requestContext.utf8.count <= 8192 else {
      throw GatewayError.invalidResponse
    }
    return response.requestContext
  }

  static func authorizationURL(webappBaseURL: String, redirectURI: String, codeChallenge: String, state: String, requestContext: String?) -> URL? {
    let trimmed = webappBaseURL.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty, var base = URLComponents(string: trimmed) else { return nil }
    let prefix = base.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    base.path = prefix.isEmpty ? "/authorize" : "/\(prefix)/authorize"
    base.queryItems = [
      URLQueryItem(name: "client_id", value: "tray-mac"),
      URLQueryItem(name: "redirect_uri", value: redirectURI),
      URLQueryItem(name: "code_challenge", value: codeChallenge),
      URLQueryItem(name: "code_challenge_method", value: "S256"),
      URLQueryItem(name: "state", value: state),
      URLQueryItem(name: "scope", value: "account"),
    ]
    if let requestContext {
      base.queryItems?.append(URLQueryItem(name: "request_context", value: requestContext))
    }
    return base.url
  }

  static func clientHeaders() -> [String: String] {
    #if arch(arm64)
      let architecture = "arm64"
    #else
      let architecture = "x86_64"
    #endif
    return clientHeaders(
      deviceName: ProcessInfo.processInfo.hostName,
      appVersion: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev",
      osVersion: ProcessInfo.processInfo.operatingSystemVersionString,
      architecture: architecture)
  }

  static func clientHeaders(deviceName: String, appVersion: String, osVersion: String, architecture: String) -> [String: String] {
    func bounded(_ value: String, _ limit: Int) -> String {
      let scalars = value.unicodeScalars.filter { !CharacterSet.controlCharacters.contains($0) }.prefix(limit)
      return String(String.UnicodeScalarView(scalars)).trimmingCharacters(in: .whitespacesAndNewlines)
    }
    let label = bounded(deviceName, 200)
    return [
      "X-Device-Label": label.isEmpty ? "FrameWorks for Mac" : label,
      "X-Client-Version": bounded("\(appVersion) (macOS \(osVersion); \(architecture))", 100),
      "User-Agent": bounded("FrameWorks-Tray/\(appVersion) (macOS; \(architecture))", 200),
    ]
  }

  private struct Response: Decodable {
    let requestContext: String
    enum CodingKeys: String, CodingKey { case requestContext = "request_context" }
  }
}
