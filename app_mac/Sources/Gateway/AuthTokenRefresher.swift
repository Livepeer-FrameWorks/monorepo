import Foundation

struct AuthTokenRefresher {
  typealias Transport = (
    _ path: String, _ body: Data, _ headers: [String: String]
  ) async throws -> (Data, HTTPURLResponse)

  var transport: Transport = { path, body, headers in
    try await GatewayClient.shared.requestWithResponse(
      method: "POST", path: path, body: body, authenticated: false, headers: headers)
  }

  func refresh(_ token: String) async throws -> (Data, HTTPURLResponse) {
    let body = try JSONSerialization.data(withJSONObject: [
      "grant_type": "refresh_token", "refresh_token": token,
    ])
    do {
      return try await transport("/auth/oauth/token", body, [:])
    } catch let error as GatewayError {
      guard case .httpError(400, let data) = error,
        let response = try? JSONDecoder().decode(TokenError.self, from: data),
        response.error.hasPrefix("invalid request")
      else { throw error }

      // Older Bridges validate PKCE fields before recognizing refresh grants.
      // Token rejection and service failures must never select the legacy path.
      return try await transport(
        "/auth/refresh", Data("{}".utf8), ["Cookie": "refresh_token=\(token)"])
    }
  }

  private struct TokenError: Decodable {
    let error: String
  }
}
