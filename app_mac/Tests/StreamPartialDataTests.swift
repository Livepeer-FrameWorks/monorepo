import Foundation
import XCTest

final class StreamPartialDataTests: XCTestCase {
  private func response(errorPath: String) throws -> GraphQLResponse<StreamsQueryResponse> {
    let json = """
      {"data":{"streamsConnection":{"edges":[
        {"node":{"id":"healthy","name":"Healthy","ingest":{"state":"LIVE","isLive":true},"metrics":null}},
        {"node":{"id":"unavailable","name":"Unavailable","ingest":null,"metrics":null}}
      ],"totalCount":2}},"errors":[{"message":"cell unavailable","path":\(errorPath)}]}
      """
    return try JSONDecoder().decode(GraphQLResponse<StreamsQueryResponse>.self, from: Data(json.utf8))
  }

  func testUnavailableCellPreservesHealthyStreams() throws {
    let wrapper = try response(errorPath: #"["streamsConnection","edges",1,"node","ingest"]"#)
    let streams = try wrapper.resolved(partialData: .streamList)
    XCTAssertEqual(streams.streamsConnection.edges.count, 2)
    XCTAssertTrue(streams.streamsConnection.edges[0].node.isActive)
    XCTAssertNil(streams.streamsConnection.edges[1].node.ingest)
    XCTAssertEqual(streams.streamsConnection.edges[1].node.summary.liveStateLabel, "unavailable")
    XCTAssertNil(streams.streamsConnection.edges[0].node.summary.viewerCount)
  }

  func testStrictRequestsStillRejectFieldErrors() throws {
    let wrapper = try response(errorPath: #"["streamsConnection","edges",1,"node","ingest"]"#)
    XCTAssertThrowsError(try wrapper.resolved())
  }

  func testStreamListDoesNotIgnoreRootAuthorizationErrors() throws {
    let wrapper = try response(errorPath: #"["streamsConnection"]"#)
    XCTAssertThrowsError(try wrapper.resolved(partialData: .streamList))
  }
}

extension StreamPartialDataTests {
  func testMissingAnalyticsPreservesLiveState() throws {
    let wrapper = try response(errorPath: #"["streamsConnection","edges",0,"node","metrics"]"#)
    let streams = try wrapper.resolved(partialData: .streamList)
    XCTAssertTrue(streams.streamsConnection.edges[0].node.isActive)
    XCTAssertNil(streams.streamsConnection.edges[0].node.summary.viewerCount)
  }

  func testUnrelatedStreamFieldErrorRemainsFatal() throws {
    let wrapper = try response(errorPath: #"["streamsConnection","edges",0,"node","name"]"#)
    XCTAssertThrowsError(try wrapper.resolved(partialData: .streamList))
  }
}
