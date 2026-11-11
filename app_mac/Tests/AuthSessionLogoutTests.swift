import XCTest

final class AuthSessionLogoutTests: XCTestCase {
  func testOutageRetainsCredentialsForRetry() async {
    var clears = 0
    let logout = AuthSessionLogout(
      revoke: { throw GatewayError.httpError(503, Data()) },
      refresh: { XCTFail("refreshed on outage") },
      clearCredentials: { clears += 1 })
    do {
      try await logout.logout(named: true, canRefresh: true)
      XCTFail("outage was hidden")
    } catch {}
    XCTAssertEqual(clears, 0)
  }

  func testExpiredAccessRenewsBeforeRevoking() async throws {
    var requests = 0
    var refreshes = 0
    var clears = 0
    let logout = AuthSessionLogout(
      revoke: {
        requests += 1
        if requests == 1 { throw GatewayError.httpError(401, Data()) }
      }, refresh: { refreshes += 1 }, clearCredentials: { clears += 1 })
    try await logout.logout(named: true, canRefresh: true)
    XCTAssertEqual(requests, 2)
    XCTAssertEqual(refreshes, 1)
    XCTAssertEqual(clears, 1)
  }

  func testRejectedRefreshClearsAlreadyInvalidSession() async throws {
    var clears = 0
    let logout = AuthSessionLogout(
      revoke: { throw GatewayError.httpError(401, Data()) },
      refresh: { throw GatewayError.httpError(400, Data(#"{"error":"invalid_grant"}"#.utf8)) },
      clearCredentials: { clears += 1 })
    try await logout.logout(named: true, canRefresh: true)
    XCTAssertEqual(clears, 1)
  }

  func testRefreshOutageRetainsCredentials() async {
    var clears = 0
    let logout = AuthSessionLogout(
      revoke: { throw GatewayError.httpError(401, Data()) },
      refresh: { throw GatewayError.httpError(503, Data()) },
      clearCredentials: { clears += 1 })
    do {
      try await logout.logout(named: true, canRefresh: true)
      XCTFail("refresh outage was hidden")
    } catch {}
    XCTAssertEqual(clears, 0)
  }

  func testLegacyLogoutNeverCallsUserWideEndpoint() async throws {
    var clears = 0
    let logout = AuthSessionLogout(
      revoke: { XCTFail("called old user-wide logout") },
      refresh: { XCTFail("refreshed legacy logout") }, clearCredentials: { clears += 1 })
    try await logout.logout(named: false, canRefresh: true)
    XCTAssertEqual(clears, 1)
  }
}
