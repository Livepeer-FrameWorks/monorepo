import XCTest

final class AuthSessionRecoveryTests: XCTestCase {
  func testOutageDoesNotRefreshOrClearCredentials() async {
    var refreshes = 0
    var clears = 0
    let recovery = AuthSessionRecovery(
      fetchUser: { throw GatewayError.httpError(503, Data()) },
      refresh: { refreshes += 1 },
      clearCredentials: { clears += 1 })
    do {
      _ = try await recovery.restore(hasAccessToken: true, hasRefreshToken: true)
      XCTFail("outage was accepted")
    } catch {}
    XCTAssertEqual(refreshes, 0)
    XCTAssertEqual(clears, 0)
  }

  func testRefreshOutageKeepsCredentialForRetry() async {
    var clears = 0
    let recovery = AuthSessionRecovery(
      fetchUser: { throw GatewayError.httpError(401, Data()) },
      refresh: { throw URLError(.notConnectedToInternet) },
      clearCredentials: { clears += 1 })
    do {
      _ = try await recovery.restore(hasAccessToken: true, hasRefreshToken: true)
      XCTFail("outage was accepted")
    } catch {}
    XCTAssertEqual(clears, 0)
  }

  func testRejectedRefreshClearsOnlyLocalCredentials() async throws {
    var clears = 0
    let recovery = AuthSessionRecovery(
      fetchUser: { throw GatewayError.httpError(401, Data()) },
      refresh: { throw GatewayError.httpError(400, Data(#"{"error":"invalid_grant"}"#.utf8)) },
      clearCredentials: { clears += 1 })
    let data = try await recovery.restore(hasAccessToken: true, hasRefreshToken: true)
    XCTAssertNil(data)
    XCTAssertEqual(clears, 1)
  }

  func testExpiredAccessRefreshesAndFetchesOnlyOnceMore() async throws {
    var requests = 0
    var refreshes = 0
    let recovery = AuthSessionRecovery(
      fetchUser: {
        requests += 1
        if requests == 1 { throw GatewayError.httpError(401, Data()) }
        return Data("user".utf8)
      }, refresh: { refreshes += 1 }, clearCredentials: { XCTFail("cleared valid session") })
    let data = try await recovery.restore(hasAccessToken: true, hasRefreshToken: true)
    XCTAssertEqual(data, Data("user".utf8))
    XCTAssertEqual(requests, 2)
    XCTAssertEqual(refreshes, 1)
  }

  func testConcurrentRefreshesShareOneRotation() async throws {
    let coordinator = AuthRefreshCoordinator()
    let count = RefreshCount()
    try await withThrowingTaskGroup(of: Void.self) { tasks in
      for _ in 0..<20 {
        tasks.addTask {
          try await coordinator.run {
            await count.increment()
            try await Task.sleep(nanoseconds: 100_000_000)
          }
        }
      }
      try await tasks.waitForAll()
    }
    let rotations = await count.value
    XCTAssertEqual(rotations, 1)
  }

  func testFailedRotationCanRetry() async throws {
    let coordinator = AuthRefreshCoordinator()
    do {
      try await coordinator.run { throw URLError(.timedOut) }
      XCTFail("failed refresh accepted")
    } catch {}
    let count = RefreshCount()
    try await coordinator.run { await count.increment() }
    let rotations = await count.value
    XCTAssertEqual(rotations, 1)
  }

  func testManualLogoutWaitsForTheWinningRefresh() async throws {
    let coordinator = AuthRefreshCoordinator()
    let started = expectation(description: "refresh started")
    let count = RefreshCount()
    let refresh = Task {
      try await coordinator.run {
        started.fulfill()
        try await Task.sleep(nanoseconds: 50_000_000)
        await count.increment()
      }
    }
    await fulfillment(of: [started], timeout: 1)
    await coordinator.finishPending()
    let saved = await count.value
    XCTAssertEqual(saved, 1)
    try await refresh.value
  }

  func testLogoutCancelsPendingSaveAndBlocksFurtherRenewals() async throws {
    let coordinator = AuthRefreshCoordinator()
    let started = expectation(description: "refresh started")
    let count = RefreshCount()
    let refresh = Task {
      try await coordinator.run {
        started.fulfill()
        try await Task.sleep(nanoseconds: 10_000_000_000)
        await count.increment()
      }
    }
    await fulfillment(of: [started], timeout: 1)
    await coordinator.close()
    _ = try? await refresh.value
    do {
      try await coordinator.run { XCTFail("rotation started after logout") }
      XCTFail("closed coordinator accepted rotation")
    } catch {}
    let saves = await count.value
    XCTAssertEqual(saves, 0)
    await coordinator.open()
    try await coordinator.run { await count.increment() }
    let newLoginSaves = await count.value
    XCTAssertEqual(newLoginSaves, 1)
  }

  func testRestoredSessionRefreshIsScheduledFromActualExpiry() throws {
    let now = Date(timeIntervalSince1970: 1_800_000_000)
    let claims = try JSONSerialization.data(withJSONObject: ["exp": now.timeIntervalSince1970 + 300])
    let token = "header.\(claims.base64EncodedString()).signature"
    XCTAssertEqual(AuthSessionRecovery.refreshDelay(accessToken: token, now: now), 240)
    XCTAssertEqual(AuthSessionRecovery.refreshDelay(accessToken: token, now: now.addingTimeInterval(301)), 1)
  }
}

private actor RefreshCount {
  private(set) var value = 0
  func increment() { value += 1 }
}
