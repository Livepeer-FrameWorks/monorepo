import Foundation

struct AuthSessionLogout {
  var revoke: () async throws -> Void
  var refresh: () async throws -> Void
  var clearCredentials: () async -> Void

  func logout(named: Bool, canRefresh: Bool) async throws {
    if named {
      do {
        try await revoke()
      } catch {
        guard case GatewayError.httpError(401, _) = error, canRefresh else { throw error }
        do {
          try await refresh()
        } catch {
          guard AuthSessionRecovery.refreshRejected(error) else { throw error }
          await clearCredentials()
          return
        }
        try await revoke()
      }
    }
    await clearCredentials()
  }
}
