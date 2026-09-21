import XCTest

final class ServerInfoServiceTests: XCTestCase {
  private let unknownField = "Cannot query field \"serverInfo\" on type \"Query\"."

  private func service(returning version: String) -> ServerInfoService {
    ServerInfoService { _ in
      ServerInfoQueryResponse(
        serverInfo: ServerInfo(version: version, features: ["viewer-protocol-selection"]))
    }
  }

  private func service(throwing error: Error) -> ServerInfoService {
    ServerInfoService { _ in throw error }
  }

  func testSendsTheGeneratedServerInfoOperation() async {
    var sent: String?
    let probe = ServerInfoService { query in
      sent = query
      return ServerInfoQueryResponse(serverInfo: ServerInfo(version: "v0.3.11", features: []))
    }
    _ = await probe.check()
    XCTAssertEqual(sent, GQL.GetServerInfo)
  }

  func testBridgeBelowTheMinimumIsTooOld() async {
    let result = await service(returning: "v0.3.10").check()
    XCTAssertEqual(result, .tooOld(reported: "v0.3.10"))
    XCTAssertEqual(result.warning, "Server too old (v0.3.10). This app needs v0.3.11 or later.")
  }

  func testMinimumAndNewerReleasesAreSupported() async {
    for version in ["v0.3.11", "0.3.11", "v0.3.12", "v0.4.0", "v1.0.0", "v0.3.11-rc1"] {
      let result = await service(returning: version).check()
      guard case .supported(let info) = result else {
        return XCTFail("\(version) reported \(result), want supported")
      }
      XCTAssertEqual(info.version, version)
      XCTAssertNil(result.warning)
    }
  }

  // A dev or git-describe build that answers serverInfo already serves what the
  // app needs; its number names an older tag, not what it runs.
  func testUnreleasedBuildsThatServeServerInfoAreSupported() async {
    for version in ["dev", "v0.3.4-7-g3023b6190-dirty", "v0.3.5-12-gabc1234"] {
      let result = await service(returning: version).check()
      guard case .supported = result else {
        return XCTFail("\(version) reported \(result), want supported")
      }
    }
  }

  func testBridgeWithoutServerInfoIsTooOld() async {
    let inBody = await service(throwing: GatewayError.graphqlErrors([unknownField])).check()
    XCTAssertEqual(inBody, .tooOld(reported: nil))
    XCTAssertEqual(inBody.warning, "Server too old. This app needs v0.3.11 or later.")

    let body = Data("{\"errors\":[{\"message\":\"\(unknownField.replacingOccurrences(of: "\"", with: "\\\""))\"}],\"data\":null}".utf8)
    let asUnprocessable = await service(throwing: GatewayError.httpError(422, body)).check()
    XCTAssertEqual(asUnprocessable, .tooOld(reported: nil))
  }

  // Failing to reach the Bridge, or any error other than the unknown field,
  // says nothing about its release.
  func testUnreachableOrOtherwiseFailingBridgeStaysUnknown() async {
    let failures: [Error] = [
      URLError(.cannotConnectToHost),
      GatewayError.notConfigured,
      GatewayError.httpError(503, Data()),
      GatewayError.httpError(401, Data("{\"error\":\"unauthorized\"}".utf8)),
      GatewayError.graphqlErrors(["rate limit exceeded"]),
    ]
    for failure in failures {
      let result = await service(throwing: failure).check()
      XCTAssertEqual(result, .unknown, "\(failure)")
      XCTAssertNil(result.warning)
    }
  }

  func testRefreshPublishesTheAnswerOnAppState() async {
    let appState = AppState()
    await service(returning: "v0.3.5").refresh(appState: appState)
    let published = await MainActor.run { appState.serverCompatibility }
    XCTAssertEqual(published, .tooOld(reported: "v0.3.5"))
  }

  func testReleaseVersionParsing() {
    XCTAssertEqual(ReleaseVersion("v0.3.11"), ReleaseVersion(major: 0, minor: 3, patch: 11))
    XCTAssertEqual(ReleaseVersion("1.2.3"), ReleaseVersion(major: 1, minor: 2, patch: 3))
    XCTAssertEqual(ReleaseVersion("v0.3.11-rc1"), ReleaseVersion(major: 0, minor: 3, patch: 11))
    XCTAssertNil(ReleaseVersion("dev"))
    XCTAssertNil(ReleaseVersion("v0.3"))
    XCTAssertNil(ReleaseVersion("v0.3.4-7-g3023b6190-dirty"))
    XCTAssertTrue(ReleaseVersion(major: 0, minor: 3, patch: 10) < ServerInfoService.minimumVersion)
    XCTAssertFalse(ReleaseVersion(major: 0, minor: 10, patch: 0) < ServerInfoService.minimumVersion)
  }
}
