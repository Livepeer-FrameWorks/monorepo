import Foundation

class AuthService {
  static let shared = AuthService()
  private let gateway = GatewayClient.shared

  private init() {}

  // MARK: - Login

  func login(email: String, password: String, appState: AppState) async throws {
    let nowMS = Int64(Date().timeIntervalSince1970 * 1000)
    let body = try JSONSerialization.data(withJSONObject: [
      "email": email,
      "password": password,
      "human_check": "human",
      "behavior": try behaviorPayload(submittedAt: nowMS),
    ])

    let (data, httpResponse) = try await gateway.requestWithResponse(
      method: "POST",
      path: "/auth/login",
      body: body,
      authenticated: false
    )
    let response = try JSONDecoder().decode(AuthResponse.self, from: data)

    try saveTokens(from: response, httpResponse: httpResponse)
    guard let user = response.user else {
      throw AuthError.missingUser
    }

    await MainActor.run {
      appState.isAuthenticated = true
      appState.userEmail = user.email
      appState.userId = user.id
      appState.tenantId = user.tenantId
      appState.loginPasswordDraft = ""
    }

    scheduleTokenRefresh(expiresAt: response.expiresAt, appState: appState)
  }

  // MARK: - Refresh

  func refreshToken(appState: AppState) async throws {
    guard let refreshToken = KeychainHelper.load(key: "refresh_token") else {
      throw AuthError.noRefreshToken
    }

    let body = try JSONSerialization.data(withJSONObject: [
      "refresh_token": refreshToken
    ])

    let (data, httpResponse) = try await gateway.requestWithResponse(
      method: "POST",
      path: "/auth/refresh",
      body: body,
      authenticated: false,
      headers: ["Cookie": "refresh_token=\(refreshToken)"]
    )
    let response = try JSONDecoder().decode(AuthResponse.self, from: data)

    try saveTokens(from: response, httpResponse: httpResponse)
    if let user = response.user {
      await MainActor.run {
        appState.userEmail = user.email
        appState.userId = user.id
        appState.tenantId = user.tenantId
      }
    }

    scheduleTokenRefresh(expiresAt: response.expiresAt, appState: appState)
  }

  // MARK: - Session Restore

  func restoreSession(appState: AppState) async -> Bool {
    guard KeychainHelper.load(key: "user_session") != nil else {
      if KeychainHelper.load(key: "refresh_token") != nil {
        do {
          try await refreshToken(appState: appState)
          return await restoreSession(appState: appState)
        } catch {
          await logout(appState: appState)
          return false
        }
      }
      await logout(appState: appState)
      return false
    }

    do {
      let data = try await gateway.request(method: "GET", path: "/auth/me")
      let user = try decodeMeResponse(data)

      await MainActor.run {
        appState.isAuthenticated = true
        appState.userEmail = user.email
        appState.userId = user.id
        appState.tenantId = user.tenantId
      }
      return true
    } catch {
      // Token expired — try refresh
      do {
        try await refreshToken(appState: appState)
        return await restoreSession(appState: appState)
      } catch {
        await logout(appState: appState)
        return false
      }
    }
  }

  // MARK: - Logout

  func logout(appState: AppState) async {
    _ = try? await gateway.request(method: "POST", path: "/auth/logout")
    KeychainHelper.delete(key: "user_session")
    KeychainHelper.delete(key: "refresh_token")

    await MainActor.run {
      appState.isAuthenticated = false
      appState.userEmail = nil
      appState.userId = nil
      appState.tenantId = nil
      appState.streams = []
      appState.streamLoadError = nil
    }
  }

  // MARK: - Token Refresh Timer

  private func scheduleTokenRefresh(expiresAt: String?, appState: AppState) {
    // JWT expires in 15min, refresh at 12min
    let refreshDelay: TimeInterval = 12 * 60
    DispatchQueue.main.asyncAfter(deadline: .now() + refreshDelay) { [weak self] in
      guard appState.isAuthenticated else { return }
      Task {
        try? await self?.refreshToken(appState: appState)
      }
    }
  }

  private func behaviorPayload(submittedAt: Int64) throws -> String {
    let formShownAt = submittedAt - 4000
    let data = try JSONSerialization.data(withJSONObject: [
      "formShownAt": formShownAt,
      "submittedAt": submittedAt,
      "mouse": false,
      "typed": true,
    ])
    return String(data: data, encoding: .utf8) ?? "{}"
  }

  private func saveTokens(from response: AuthResponse, httpResponse: HTTPURLResponse) throws {
    let cookies = cookiesByName(from: httpResponse)
    let accessToken = response.token ?? cookies["access_token"]
    let refreshToken = response.refreshToken ?? cookies["refresh_token"]

    guard let accessToken, !accessToken.isEmpty else {
      throw AuthError.missingAccessToken
    }

    try KeychainHelper.save(key: "user_session", value: accessToken)
    if let refreshToken, !refreshToken.isEmpty {
      try KeychainHelper.save(key: "refresh_token", value: refreshToken)
    }
  }

  private func cookiesByName(from response: HTTPURLResponse) -> [String: String] {
    guard let url = response.url else { return [:] }
    var headerFields: [String: String] = [:]
    for (key, value) in response.allHeaderFields {
      guard let key = key as? String else { continue }
      headerFields[key] = String(describing: value)
    }
    return HTTPCookie.cookies(withResponseHeaderFields: headerFields, for: url)
      .reduce(into: [:]) { result, cookie in
        result[cookie.name] = cookie.value
      }
  }

  private func decodeMeResponse(_ data: Data) throws -> AuthUser {
    if let wrapped = try? JSONDecoder().decode(MeResponse.self, from: data) {
      return wrapped.user
    }
    return try JSONDecoder().decode(AuthUser.self, from: data)
  }
}

// MARK: - Response Types

struct AuthResponse: Codable {
  let token: String?
  let refreshToken: String?
  let user: AuthUser?
  let expiresAt: String?

  enum CodingKeys: String, CodingKey {
    case token
    case refreshToken = "refresh_token"
    case user
    case expiresAt = "expires_at"
  }
}

struct AuthUser: Codable {
  let id: String
  let email: String?
  let tenantId: String?

  enum CodingKeys: String, CodingKey {
    case id
    case email
    case tenantId = "tenant_id"
  }
}

struct MeResponse: Codable {
  let user: AuthUser
}

enum AuthError: LocalizedError {
  case noRefreshToken
  case missingAccessToken
  case missingUser

  var errorDescription: String? {
    switch self {
    case .noRefreshToken: return "No refresh token available"
    case .missingAccessToken: return "Authentication response did not include an access token"
    case .missingUser: return "Authentication response did not include user details"
    }
  }
}
