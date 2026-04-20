import Foundation

class AuthService {
  static let shared = AuthService()
  private let gateway = GatewayClient.shared

  private init() {}

  // MARK: - Login

  func login(email: String, password: String, appState: AppState) async throws {
    let body = try JSONSerialization.data(withJSONObject: [
      "email": email,
      "password": password,
    ])

    let data = try await gateway.request(method: "POST", path: "/auth/login", body: body, authenticated: false)
    let response = try JSONDecoder().decode(AuthResponse.self, from: data)

    try KeychainHelper.save(key: "user_session", value: response.token)
    try KeychainHelper.save(key: "refresh_token", value: response.refreshToken)

    await MainActor.run {
      appState.isAuthenticated = true
      appState.userEmail = response.user.email
      appState.userId = response.user.id
      appState.tenantId = response.user.tenantId
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

    let data = try await gateway.request(method: "POST", path: "/auth/refresh", body: body, authenticated: false)
    let response = try JSONDecoder().decode(AuthResponse.self, from: data)

    try KeychainHelper.save(key: "user_session", value: response.token)
    try KeychainHelper.save(key: "refresh_token", value: response.refreshToken)

    scheduleTokenRefresh(expiresAt: response.expiresAt, appState: appState)
  }

  // MARK: - Session Restore

  func restoreSession(appState: AppState) async -> Bool {
    guard KeychainHelper.load(key: "user_session") != nil else {
      return false
    }

    do {
      let data = try await gateway.request(method: "GET", path: "/auth/me")
      let response = try JSONDecoder().decode(MeResponse.self, from: data)

      await MainActor.run {
        appState.isAuthenticated = true
        appState.userEmail = response.user.email
        appState.userId = response.user.id
        appState.tenantId = response.user.tenantId
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
    try? await gateway.request(method: "POST", path: "/auth/logout")
    KeychainHelper.delete(key: "user_session")
    KeychainHelper.delete(key: "refresh_token")

    await MainActor.run {
      appState.isAuthenticated = false
      appState.userEmail = nil
      appState.userId = nil
      appState.tenantId = nil
      appState.streams = []
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
}

// MARK: - Response Types

struct AuthResponse: Codable {
  let token: String
  let refreshToken: String
  let user: AuthUser
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

  var errorDescription: String? {
    switch self {
    case .noRefreshToken: return "No refresh token available"
    }
  }
}
