import Foundation

struct AuthSessionRecovery {
  var fetchUser: () async throws -> Data
  var refresh: () async throws -> Void
  var clearCredentials: () async -> Void

  func restore(hasAccessToken: Bool, hasRefreshToken: Bool) async throws -> Data? {
    if hasAccessToken {
      do {
        return try await fetchUser()
      } catch {
        guard Self.accessRejected(error) else { throw error }
      }
    }
    guard hasRefreshToken else {
      await clearCredentials()
      return nil
    }
    do {
      try await refresh()
    } catch {
      guard Self.refreshRejected(error) else { throw error }
      await clearCredentials()
      return nil
    }
    do {
      return try await fetchUser()
    } catch {
      guard Self.accessRejected(error) else { throw error }
      await clearCredentials()
      return nil
    }
  }

  private static func accessRejected(_ error: Error) -> Bool {
    if case GatewayError.httpError(401, _) = error { return true }
    return false
  }

  static func refreshRejected(_ error: Error) -> Bool {
    if case GatewayError.httpError(401, _) = error { return true }
    guard case GatewayError.httpError(400, let data) = error,
      let body = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
    else { return false }
    return body["error"] as? String == "invalid_grant"
  }

  static func refreshDelay(accessToken: String?, now: Date = Date()) -> TimeInterval {
    guard let accessToken else { return 12 * 60 }
    let fields = accessToken.split(separator: ".")
    guard fields.count == 3 else { return 12 * 60 }
    var payload = String(fields[1]).replacingOccurrences(of: "-", with: "+")
      .replacingOccurrences(of: "_", with: "/")
    payload += String(repeating: "=", count: (4 - payload.count % 4) % 4)
    guard let data = Data(base64Encoded: payload),
      let claims = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
      let expiry = claims["exp"] as? Double
    else { return 12 * 60 }
    let remaining = expiry - now.timeIntervalSince1970
    return max(1, min(12 * 60, remaining > 60 ? remaining - 60 : remaining * 0.8))
  }

  static func hasSessionID(accessToken: String?) -> Bool {
    guard let accessToken else { return false }
    let fields = accessToken.split(separator: ".")
    guard fields.count == 3 else { return false }
    var payload = String(fields[1]).replacingOccurrences(of: "-", with: "+")
      .replacingOccurrences(of: "_", with: "/")
    payload += String(repeating: "=", count: (4 - payload.count % 4) % 4)
    guard let data = Data(base64Encoded: payload),
      let claims = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
      let sessionID = claims["sid"] as? String
    else { return false }
    return UUID(uuidString: sessionID) != nil
  }
}
