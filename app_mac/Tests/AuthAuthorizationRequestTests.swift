import XCTest

final class AuthAuthorizationRequestTests: XCTestCase {
  private func response() -> HTTPURLResponse {
    HTTPURLResponse(url: URL(string: "https://selfhost.example/auth/authorize/start")!, statusCode: 200, httpVersion: nil, headerFields: nil)!
  }

  func testStartBindsPKCEAndSendsNativeMetadataWithoutCredentials() async throws {
    var calls = 0
    let request = AuthAuthorizationRequest { path, body, headers in
      calls += 1
      XCTAssertEqual(path, "/auth/authorize/start")
      XCTAssertNotNil(headers["X-Device-Label"])
      XCTAssertTrue(headers["X-Client-Version"]?.contains("macOS") == true)
      XCTAssertNil(headers["Authorization"])
      XCTAssertNil(headers["Cookie"])
      let payload = try JSONSerialization.jsonObject(with: body) as? [String: String]
      XCTAssertEqual(payload?["client_id"], "tray-mac")
      XCTAssertEqual(payload?["redirect_uri"], "http://127.0.0.1:54321/callback")
      XCTAssertEqual(payload?["code_challenge"], "challenge")
      XCTAssertEqual(payload?["code_challenge_method"], "S256")
      XCTAssertEqual(payload?["state"], "native-state")
      XCTAssertEqual(payload?["scope"], "account")
      return (Data(#"{"request_context":"context.signature"}"#.utf8), self.response())
    }
    let context = try await request.start(redirectURI: "http://127.0.0.1:54321/callback", codeChallenge: "challenge", state: "native-state")
    XCTAssertEqual(context, "context.signature")
    XCTAssertEqual(calls, 1)
  }

  func testOldBridgeMissingEndpointPreservesLegacyFlow() async throws {
    for status in [404, 405, 501] {
      var calls = 0
      let request = AuthAuthorizationRequest { _, _, _ in
        calls += 1
        throw GatewayError.httpError(status, Data("old endpoint response".utf8))
      }
      let context = try await request.start(redirectURI: "http://127.0.0.1:54321/callback", codeChallenge: "challenge", state: "state")
      XCTAssertNil(context)
      XCTAssertEqual(calls, 1)
    }
  }

  func testRejectionOutageAndRedirectNeverSelectLegacyFlow() async {
    for status in [400, 401, 403, 429, 500, 503, 302, 307, 308] {
      var calls = 0
      let request = AuthAuthorizationRequest { _, _, _ in
        calls += 1
        throw GatewayError.httpError(status, Data(#"{"error":"request failed"}"#.utf8))
      }
      do {
        _ = try await request.start(redirectURI: "http://127.0.0.1:54321/callback", codeChallenge: "challenge", state: "state")
        XCTFail("Expected refusal for HTTP \(status)")
      } catch {}
      XCTAssertEqual(calls, 1)
    }
  }

  func testMalformedModernResponseDoesNotFallBack() async {
    for body in ["not json", "{}", #"{"request_context":""}"#] {
      let request = AuthAuthorizationRequest { _, _, _ in
        (Data(body.utf8), self.response())
      }
      do {
        _ = try await request.start(redirectURI: "http://127.0.0.1:54321/callback", codeChallenge: "challenge", state: "state")
        XCTFail("Expected malformed context refusal")
      } catch {}
    }
  }

  func testBrowserURLPreservesStatePKCEAndLegacyCompatibility() throws {
    for context in [nil, "context.signature"] {
      let url = try XCTUnwrap(AuthAuthorizationRequest.authorizationURL(
        webappBaseURL: "https://console.example/prefix/", redirectURI: "http://127.0.0.1:54321/callback", codeChallenge: "challenge", state: "opaque&state", requestContext: context))
      let components = try XCTUnwrap(URLComponents(url: url, resolvingAgainstBaseURL: false))
      let fields = Dictionary(uniqueKeysWithValues: (components.queryItems ?? []).map { ($0.name, $0.value ?? "") })
      XCTAssertEqual(components.path, "/prefix/authorize")
      XCTAssertEqual(fields["client_id"], "tray-mac")
      XCTAssertEqual(fields["redirect_uri"], "http://127.0.0.1:54321/callback")
      XCTAssertEqual(fields["code_challenge"], "challenge")
      XCTAssertEqual(fields["code_challenge_method"], "S256")
      XCTAssertEqual(fields["state"], "opaque&state")
      XCTAssertEqual(fields["request_context"], context)
    }
  }

  func testDeviceHeadersAreBoundedAndContainNoControlCharacters() {
    let headers = AuthAuthorizationRequest.clientHeaders(
      deviceName: String(repeating: "名", count: 220) + "\r\nInjected: yes", appVersion: "v0.3.15\r\n", osVersion: "15.0", architecture: "arm64")
    XCTAssertEqual(headers["X-Device-Label"]?.unicodeScalars.count, 200)
    XCTAssertLessThanOrEqual(headers["X-Client-Version"]?.unicodeScalars.count ?? 1000, 100)
    XCTAssertTrue(headers["X-Client-Version"]?.contains("macOS") == true)
    for value in headers.values {
      XCTAssertFalse(value.unicodeScalars.contains { CharacterSet.controlCharacters.contains($0) })
    }
    XCTAssertEqual(AuthAuthorizationRequest.clientHeaders(deviceName: "", appVersion: "dev", osVersion: "15", architecture: "arm64")["X-Device-Label"], "FrameWorks for Mac")
  }
}
