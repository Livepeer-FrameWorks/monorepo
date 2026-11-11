import XCTest

final class AuthTokenRefresherTests: XCTestCase {
  private func response(_ path: String) -> HTTPURLResponse {
    HTTPURLResponse(
      url: URL(string: "https://selfhost.example\(path)")!, statusCode: 200,
      httpVersion: nil, headerFields: ["Set-Cookie": "access_token=legacy-access; HttpOnly"]
    )!
  }

  func testOldBridgeValidationResponseUsesLegacyRefreshCookie() async throws {
    var paths: [String] = []
    let refresher = AuthTokenRefresher { path, body, headers in
      paths.append(path)
      if path == "/auth/oauth/token" {
        XCTAssertTrue(headers.isEmpty)
        let payload = try JSONSerialization.jsonObject(with: body) as? [String: String]
        XCTAssertEqual(payload?["grant_type"], "refresh_token")
        XCTAssertEqual(payload?["refresh_token"], "native-secret")
        throw GatewayError.httpError(
          400, Data(#"{"error":"invalid request: Key: 'Code' Error:Field validation for 'Code' failed on the 'required' tag\nKey: 'CodeVerifier' Error:Field validation for 'CodeVerifier' failed on the 'required' tag"}"#.utf8))
      }
      XCTAssertEqual(headers, ["Cookie": "refresh_token=native-secret"])
      return (Data(#"{"user":{"id":"user-1"}}"#.utf8), self.response(path))
    }
    let (_, httpResponse) = try await refresher.refresh("native-secret")
    XCTAssertEqual(paths, ["/auth/oauth/token", "/auth/refresh"])
    XCTAssertNotNil(httpResponse.value(forHTTPHeaderField: "Set-Cookie"))
  }

  func testModernBridgeUsesJSONWithoutCookies() async throws {
    var paths: [String] = []
    let refresher = AuthTokenRefresher { path, _, headers in
      paths.append(path)
      XCTAssertTrue(headers.isEmpty)
      return (Data(#"{"access_token":"access","refresh_token":"refresh"}"#.utf8), self.response(path))
    }
    let (data, _) = try await refresher.refresh("native-secret")
    XCTAssertTrue(String(decoding: data, as: UTF8.self).contains("access_token"))
    XCTAssertEqual(paths, ["/auth/oauth/token"])
  }

  func testRejectionAndOutageNeverFallBack() async {
    for (status, message) in [
      (400, "invalid_grant"), (400, "unsupported_grant_type"),
      (400, "invalid_request"), (401, "invalid request"), (503, "temporarily_unavailable"),
    ] {
      var calls = 0
      let refresher = AuthTokenRefresher { _, _, _ in
        calls += 1
        throw GatewayError.httpError(
          status, try JSONSerialization.data(withJSONObject: ["error": message]))
      }
      do {
        _ = try await refresher.refresh("native-secret")
        XCTFail("Expected refresh failure for \(message)")
      } catch {}
      XCTAssertEqual(calls, 1, "Unexpected legacy retry for \(message)")
    }
  }
}
