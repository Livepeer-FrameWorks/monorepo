import Foundation
import XCTest

final class NodeCapacityServiceTests: XCTestCase {
  func testDecimalMegabitsAndDefaultRestoration() throws {
    XCTAssertNil(try NodeCapacityService.overrideBytes(mbps: "  "))
    XCTAssertEqual(try NodeCapacityService.overrideBytes(mbps: "1000"), 125_000_000)
    XCTAssertEqual(try NodeCapacityService.overrideBytes(mbps: "750"), 93_750_000)
    XCTAssertEqual(try NodeCapacityService.overrideBytes(mbps: "0.1"), 12_500)
    for invalid in ["0", "-1", "nan", "inf", "0.000001", "100000000000000000000"] {
      XCTAssertThrowsError(try NodeCapacityService.overrideBytes(mbps: invalid), invalid)
    }
  }

  func testBudgetAndApplicationAreIndependent() throws {
    let data = Data("""
      {"node":{"capacity":{"egressBudgetBytesPerSec":125000000,"ingressBudgetBytesPerSec":250000000,
       "egressSource":"DEFAULT","ingressSource":"OPERATOR","revision":7,"appliedRevision":6}}}
      """.utf8)
    let response = try JSONDecoder().decode(NodeCapacityQueryResponse.self, from: data)
    let capacity = try XCTUnwrap(response.node?.capacity)
    XCTAssertEqual(capacity.egressSource, "DEFAULT")
    XCTAssertEqual(capacity.ingressBudgetBytesPerSec, 250_000_000)
    XCTAssertEqual(capacity.applicationStatus, "Capacity not applied yet")
  }
}
