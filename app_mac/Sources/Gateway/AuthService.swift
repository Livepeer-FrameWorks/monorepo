import AppKit
import CryptoKit
import Foundation
import Network
import Security

class AuthService {
  static let shared = AuthService()
  private let gateway = GatewayClient.shared
  private let browserLoginTimeoutNanoseconds: UInt64 = 5 * 60 * 1_000_000_000
  private var browserLoginServer: BrowserLoginCallbackServer?

  private init() {}

  // MARK: - Login

  func loginWithBrowser(appState: AppState) async throws {
    let verifier = try randomBase64URL(byteCount: 32)
    let challenge = base64URLEncode(Data(SHA256.hash(data: Data(verifier.utf8))))
    let state = try randomBase64URL(byteCount: 16)
    let webappBaseURL = try await fetchWebappBaseURL()

    let server = BrowserLoginCallbackServer()
    browserLoginServer?.stop()
    browserLoginServer = server
    let port = try await server.start()

    let redirectURI = "http://127.0.0.1:\(port)/callback"
    guard let authorizeURL = makeAuthorizeURL(
      webappBaseURL: webappBaseURL,
      redirectURI: redirectURI,
      codeChallenge: challenge,
      state: state
    ) else {
      server.stop()
      throw AuthError.invalidAuthorizationURL
    }

    guard NSWorkspace.shared.open(authorizeURL) else {
      server.stop()
      throw AuthError.browserOpenFailed
    }

    defer {
      server.stop()
      if browserLoginServer === server {
        browserLoginServer = nil
      }
    }

    let callback = try await waitForBrowserCallback(from: server)
    if !callback.manual || !callback.state.isEmpty, !constantTimeEqual(callback.state, state) {
      throw AuthError.stateMismatch
    }
    if let error = callback.error {
      throw AuthError.authorizationDenied(error)
    }
    guard let code = callback.code, !code.isEmpty else {
      throw AuthError.missingCallbackCode
    }

    let response = try await exchangeAuthorizationCode(
      code: code,
      verifier: verifier,
      redirectURI: redirectURI
    )
    try saveTokens(from: response, httpResponse: nil)
    guard let user = response.user else {
      throw AuthError.missingUser
    }

    await MainActor.run {
      appState.isAuthenticated = true
      appState.userEmail = user.email
      appState.userId = user.id
      appState.tenantId = user.tenantId
    }

    scheduleTokenRefresh(expiresAt: response.expiresAt, appState: appState)
  }

  func cancelBrowserLogin() {
    browserLoginServer?.stop()
    browserLoginServer = nil
  }

  func submitBrowserLoginCallback(_ value: String) throws {
    guard let server = browserLoginServer else {
      throw AuthError.noPendingBrowserLogin
    }
    server.submitManualCallback(try parseManualCallback(value))
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

  private func makeAuthorizeURL(
    webappBaseURL: String,
    redirectURI: String,
    codeChallenge: String,
    state: String
  ) -> URL? {
    let trimmed = webappBaseURL.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty, var base = URLComponents(string: trimmed) else { return nil }
    base.path = pathByAppending("authorize", to: base.path)
    base.queryItems = [
      URLQueryItem(name: "client_id", value: "tray-mac"),
      URLQueryItem(name: "redirect_uri", value: redirectURI),
      URLQueryItem(name: "code_challenge", value: codeChallenge),
      URLQueryItem(name: "code_challenge_method", value: "S256"),
      URLQueryItem(name: "state", value: state),
      URLQueryItem(name: "scope", value: "account"),
    ]
    return base.url
  }

  private func fetchWebappBaseURL() async throws -> String {
    let data = try await gateway.request(
      method: "GET",
      path: "/auth/webapp-url",
      authenticated: false
    )
    let response = try JSONDecoder().decode(WebappURLResponse.self, from: data)
    let webappURL = response.webappURL.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !webappURL.isEmpty else {
      throw AuthError.missingWebappURL
    }
    return webappURL
  }

  private func waitForBrowserCallback(from server: BrowserLoginCallbackServer) async throws -> BrowserLoginCallback {
    try await withThrowingTaskGroup(of: BrowserLoginCallback.self) { group in
      group.addTask {
        try await server.waitForCallback()
      }
      group.addTask { [browserLoginTimeoutNanoseconds] in
        try await Task.sleep(nanoseconds: browserLoginTimeoutNanoseconds)
        server.stop()
        throw AuthError.browserLoginTimedOut
      }

      guard let callback = try await group.next() else {
        throw AuthError.browserLoginTimedOut
      }
      group.cancelAll()
      return callback
    }
  }

  private func pathByAppending(_ component: String, to path: String) -> String {
    let trimmedPath = path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    if trimmedPath.isEmpty {
      return "/\(component)"
    }
    return "/\(trimmedPath)/\(component)"
  }

  private func parseManualCallback(_ value: String) throws -> BrowserLoginCallback {
    let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else {
      throw AuthError.invalidManualCallback
    }
    if let components = URLComponents(string: trimmed),
       let items = components.queryItems,
       items.contains(where: { $0.name == "code" || $0.name == "error" }) {
      let field: (String) -> String? = { name in
        items.first(where: { $0.name == name })?.value
      }
      return BrowserLoginCallback(
        code: field("code"),
        state: field("state") ?? "",
        error: field("error"),
        manual: true
      )
    }
    guard !trimmed.contains(where: { $0.isWhitespace }) else {
      throw AuthError.invalidManualCallback
    }
    return BrowserLoginCallback(code: trimmed, state: "", error: nil, manual: true)
  }

  private func exchangeAuthorizationCode(
    code: String,
    verifier: String,
    redirectURI: String
  ) async throws -> AuthResponse {
    let body = try JSONSerialization.data(withJSONObject: [
      "code": code,
      "code_verifier": verifier,
      "client_id": "tray-mac",
      "redirect_uri": redirectURI,
    ])

    let data = try await gateway.request(
      method: "POST",
      path: "/auth/oauth/token",
      body: body,
      authenticated: false
    )
    return try JSONDecoder().decode(AuthResponse.self, from: data)
  }

  private func saveTokens(from response: AuthResponse, httpResponse: HTTPURLResponse?) throws {
    let cookies = httpResponse.map(cookiesByName(from:)) ?? [:]
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

  private func randomBase64URL(byteCount: Int) throws -> String {
    var bytes = [UInt8](repeating: 0, count: byteCount)
    let status = SecRandomCopyBytes(kSecRandomDefault, byteCount, &bytes)
    guard status == errSecSuccess else {
      throw AuthError.randomGenerationFailed(status)
    }
    return base64URLEncode(Data(bytes))
  }

  private func base64URLEncode(_ data: Data) -> String {
    data.base64EncodedString()
      .replacingOccurrences(of: "+", with: "-")
      .replacingOccurrences(of: "/", with: "_")
      .replacingOccurrences(of: "=", with: "")
  }

  private func constantTimeEqual(_ lhs: String, _ rhs: String) -> Bool {
    let left = Array(lhs.utf8)
    let right = Array(rhs.utf8)
    let maxCount = max(left.count, right.count)
    var diff = left.count ^ right.count
    for index in 0..<maxCount {
      let a = index < left.count ? Int(left[index]) : 0
      let b = index < right.count ? Int(right[index]) : 0
      diff |= a ^ b
    }
    return diff == 0
  }
}

// MARK: - Response Types

struct AuthResponse: Decodable {
  let token: String?
  let refreshToken: String?
  let user: AuthUser?
  let expiresAt: String?

  enum CodingKeys: String, CodingKey {
    case token
    case accessToken = "access_token"
    case refreshToken = "refresh_token"
    case user
    case expiresAt = "expires_at"
  }

  init(from decoder: Decoder) throws {
    let container = try decoder.container(keyedBy: CodingKeys.self)
    token = try container.decodeIfPresent(String.self, forKey: .token)
      ?? container.decodeIfPresent(String.self, forKey: .accessToken)
    refreshToken = try container.decodeIfPresent(String.self, forKey: .refreshToken)
    user = try container.decodeIfPresent(AuthUser.self, forKey: .user)
    expiresAt = try container.decodeIfPresent(String.self, forKey: .expiresAt)
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

private struct WebappURLResponse: Decodable {
  let webappURL: String

  enum CodingKeys: String, CodingKey {
    case webappURL = "webapp_url"
  }
}

enum AuthError: LocalizedError {
  case noRefreshToken
  case missingAccessToken
  case missingUser
  case invalidAuthorizationURL
  case browserOpenFailed
  case stateMismatch
  case authorizationDenied(String)
  case missingCallbackCode
  case missingWebappURL
  case browserLoginTimedOut
  case noPendingBrowserLogin
  case invalidManualCallback
  case randomGenerationFailed(OSStatus)

  var errorDescription: String? {
    switch self {
    case .noRefreshToken: return "No refresh token available"
    case .missingAccessToken: return "Authentication response did not include an access token"
    case .missingUser: return "Authentication response did not include user details"
    case .invalidAuthorizationURL: return "Could not build browser authorization URL"
    case .browserOpenFailed: return "Could not open the browser for sign in"
    case .stateMismatch: return "Browser sign in returned an invalid state"
    case .authorizationDenied(let message): return "Browser sign in was denied: \(message)"
    case .missingCallbackCode: return "Browser sign in did not return an authorization code"
    case .missingWebappURL: return "Bridge did not return a webapp URL for browser sign in"
    case .browserLoginTimedOut: return "Browser sign in timed out"
    case .noPendingBrowserLogin: return "Start browser sign in before submitting a code"
    case .invalidManualCallback: return "Paste the full callback URL or authorization code"
    case .randomGenerationFailed(let status): return "Could not generate secure login nonce: \(status)"
    }
  }
}

private struct BrowserLoginCallback: Sendable {
  let code: String?
  let state: String
  let error: String?
  let manual: Bool
}

private final class BrowserLoginCallbackServer: @unchecked Sendable {
  private let queue = DispatchQueue(label: "com.livepeer.frameworks.auth-callback", qos: .userInitiated)
  private var listener: NWListener?
  private var startContinuation: CheckedContinuation<UInt16, Error>?
  private var callbackContinuation: CheckedContinuation<BrowserLoginCallback, Error>?
  private var pendingCallback: BrowserLoginCallback?
  private var completed = false

  func start() async throws -> UInt16 {
    let params = NWParameters.tcp
    params.allowLocalEndpointReuse = false
    if let loopback = IPv4Address("127.0.0.1") {
      params.requiredLocalEndpoint = .hostPort(host: .ipv4(loopback), port: .any)
    }

    let listener = try NWListener(using: params)
    self.listener = listener

    return try await withCheckedThrowingContinuation { continuation in
      queue.async {
        self.startContinuation = continuation
        listener.stateUpdateHandler = { [weak self] state in
          self?.handleListenerState(state)
        }
        listener.newConnectionHandler = { [weak self] connection in
          self?.handleConnection(connection)
        }
        listener.start(queue: self.queue)
      }
    }
  }

  func waitForCallback() async throws -> BrowserLoginCallback {
    try await withCheckedThrowingContinuation { continuation in
      queue.async {
        if let callback = self.pendingCallback {
          self.pendingCallback = nil
          continuation.resume(returning: callback)
          return
        }
        self.callbackContinuation = continuation
      }
    }
  }

  func submitManualCallback(_ callback: BrowserLoginCallback) {
    queue.async {
      guard !self.completed else { return }
      self.completed = true
      if let continuation = self.callbackContinuation {
        self.callbackContinuation = nil
        continuation.resume(returning: callback)
      } else {
        self.pendingCallback = callback
      }
    }
  }

  func stop() {
    queue.async {
      self.listener?.cancel()
      self.listener = nil
      self.resumeStart(with: AuthCallbackError.cancelled)
      self.resumeCallback(with: AuthCallbackError.cancelled)
    }
  }

  private func handleListenerState(_ state: NWListener.State) {
    switch state {
    case .ready:
      guard let port = listener?.port?.rawValue else {
        resumeStart(with: AuthCallbackError.missingPort)
        return
      }
      resumeStart(returning: port)
    case .failed(let error):
      resumeStart(with: error)
      resumeCallback(with: error)
    case .cancelled:
      resumeStart(with: AuthCallbackError.cancelled)
      resumeCallback(with: AuthCallbackError.cancelled)
    default:
      break
    }
  }

  private func handleConnection(_ connection: NWConnection) {
    guard !completed else {
      connection.cancel()
      return
    }
    connection.start(queue: queue)
    connection.receive(minimumIncompleteLength: 1, maximumLength: 8192) { [weak self] data, _, _, _ in
      guard let self else {
        connection.cancel()
        return
      }
      guard let data, let callback = self.parseCallback(from: data) else {
        self.sendHTTPResponse(connection, status: 400, body: "FrameWorks sign in failed. You can close this window.")
        return
      }
      self.completed = true
      self.sendHTTPResponse(connection, status: 200, body: "FrameWorks sign in complete. You can close this window.")
      if let continuation = self.callbackContinuation {
        self.callbackContinuation = nil
        continuation.resume(returning: callback)
      } else {
        self.pendingCallback = callback
      }
    }
  }

  private func parseCallback(from data: Data) -> BrowserLoginCallback? {
    guard let raw = String(data: data, encoding: .utf8),
          let requestLine = raw.components(separatedBy: "\r\n").first else {
      return nil
    }
    let parts = requestLine.split(separator: " ")
    guard parts.count >= 2, parts[0] == "GET" else { return nil }
    guard let components = URLComponents(string: "http://127.0.0.1\(parts[1])"),
          components.path == "/callback" else {
      return nil
    }
    let items = components.queryItems ?? []
    let value: (String) -> String? = { name in
      items.first(where: { $0.name == name })?.value
    }
    return BrowserLoginCallback(
      code: value("code"),
      state: value("state") ?? "",
      error: value("error"),
      manual: false
    )
  }

  private func sendHTTPResponse(_ connection: NWConnection, status: Int, body: String) {
    let statusText = status == 200 ? "OK" : "Bad Request"
    let html = """
    <!doctype html><html><head><meta charset="utf-8"><title>FrameWorks</title></head><body>\(body)</body></html>
    """
    let bodyData = Data(html.utf8)
    let headers = [
      "HTTP/1.1 \(status) \(statusText)",
      "Content-Type: text/html; charset=utf-8",
      "Content-Length: \(bodyData.count)",
      "Connection: close",
      "",
      "",
    ].joined(separator: "\r\n")
    var response = Data(headers.utf8)
    response.append(bodyData)
    connection.send(content: response, completion: .contentProcessed { _ in
      connection.cancel()
    })
  }

  private func resumeStart(returning port: UInt16) {
    guard let continuation = startContinuation else { return }
    startContinuation = nil
    continuation.resume(returning: port)
  }

  private func resumeStart(with error: Error) {
    guard let continuation = startContinuation else { return }
    startContinuation = nil
    continuation.resume(throwing: error)
  }

  private func resumeCallback(with error: Error) {
    guard let continuation = callbackContinuation else { return }
    callbackContinuation = nil
    continuation.resume(throwing: error)
  }
}

private enum AuthCallbackError: LocalizedError {
  case cancelled
  case missingPort

  var errorDescription: String? {
    switch self {
    case .cancelled: return "Browser sign in was cancelled"
    case .missingPort: return "Browser sign in callback listener did not expose a port"
    }
  }
}
