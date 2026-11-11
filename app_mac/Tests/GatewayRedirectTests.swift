import Foundation
import Network
import XCTest

final class GatewayRedirectTests: XCTestCase {
  func testAccessTokenIsBoundToLoginBridge() {
    let stored = ["user_session": "private-session", "user_session_bridge": "https://login.example.test"]
    let client = GatewayClient(configuration: .ephemeral, credentialLoader: { stored[$0] })
    client.baseURL = "https://other.example.test"
    XCTAssertNil(client.accessToken, "context switch must not forward a previous Bridge credential")
    client.baseURL = "https://login.example.test/"
    XCTAssertEqual(client.accessToken, "private-session")
  }

  func testLegacyCredentialWithoutBridgeBindingRequiresSignIn() {
    let client = GatewayClient(configuration: .ephemeral, credentialLoader: { $0 == "user_session" ? "legacy-session" : nil })
    client.baseURL = "https://unknown.example.test"
    XCTAssertNil(client.accessToken, "legacy tokens have no issuer claim from which to recover a safe origin")
  }

  func testBridgeBindingPreservesEscapedPathSeparators() {
    let stored = ["user_session": "private-session", "user_session_bridge": "https://login.example.test/a"]
    let client = GatewayClient(configuration: .ephemeral, credentialLoader: { stored[$0] })
    client.baseURL = "https://login.example.test/a%2F"
    XCTAssertNil(client.accessToken, "encoded path boundaries must not be collapsed into another Bridge path")
  }

  func testRefreshRequestDoesNotFollowRedirect() async throws {
    let destination = try RedirectFixture(response: "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}")
    let destinationURL = try await destination.start()
    defer { destination.stop() }
    let bridge = try RedirectFixture(response: "HTTP/1.1 307 Temporary Redirect\r\nLocation: \(destinationURL)/stolen\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
    let bridgeURL = try await bridge.start()
    defer { bridge.stop() }
    let client = GatewayClient(configuration: .ephemeral)
    client.baseURL = bridgeURL
    do {
      _ = try await client.requestWithResponse(
        method: "POST", path: "/auth/oauth/token",
        body: Data(#"{"grant_type":"refresh_token","refresh_token":"private-refresh"}"#.utf8),
        authenticated: false)
      XCTFail("refresh request followed redirect")
    } catch GatewayError.httpError(let code, _) {
      XCTAssertEqual(code, 307)
    }
    XCTAssertEqual(destination.hits, 0)
  }
}

private final class RedirectFixture {
  private let listener: NWListener
  private let queue = DispatchQueue(label: "gateway-redirect-fixture")
  private let count = RequestCount()
  var hits: Int { count.value }

  init(response: String) throws {
    listener = try NWListener(using: .tcp, on: .any)
    let count = self.count
    listener.newConnectionHandler = { connection in
      connection.start(queue: .global())
      connection.receive(minimumIncompleteLength: 1, maximumLength: 65536) { _, _, _, _ in
        count.increment()
        connection.send(content: Data(response.utf8), completion: .contentProcessed { _ in connection.cancel() })
      }
    }
  }

  func start() async throws -> String {
    try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<Void, Error>) in
      listener.stateUpdateHandler = { [listener] state in
        switch state {
        case .ready:
          listener.stateUpdateHandler = nil
          continuation.resume()
        case .failed(let error):
          listener.stateUpdateHandler = nil
          continuation.resume(throwing: error)
        default: break
        }
      }
      listener.start(queue: queue)
    }
    return "http://127.0.0.1:\(listener.port!.rawValue)"
  }

  func stop() { listener.cancel() }
}

private final class RequestCount {
  private let lock = NSLock()
  private var hits = 0
  var value: Int {
    lock.lock()
    defer { lock.unlock() }
    return hits
  }

  func increment() {
    lock.lock()
    defer { lock.unlock() }
    hits += 1
  }
}
