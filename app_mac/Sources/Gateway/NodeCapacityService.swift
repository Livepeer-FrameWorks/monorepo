import Foundation

struct EdgeNodeCapacity: Decodable, Equatable {
  let egressBudgetBytesPerSec: Double
  let ingressBudgetBytesPerSec: Double
  let egressSource: String
  let ingressSource: String
  let revision: Int
  let appliedRevision: Int?

  var applicationStatus: String {
    guard let appliedRevision else { return "Applied capacity unknown" }
    return appliedRevision == revision ? "Applied" : "Capacity not applied yet"
  }
}

struct NodeCapacityQueryResponse: Decodable {
  struct Node: Decodable {
    let capacity: EdgeNodeCapacity?
  }
  let node: Node?
}

private struct SetNodeCapacityResponse: Decodable {
  struct Result: Decodable {
    let __typename: String
    let capacity: EdgeNodeCapacity?
    let message: String?
  }
  let setNodeCapacity: Result
}

enum NodeCapacityError: LocalizedError {
  case unavailable
  case invalidBudget
  case rejected(String)

  var errorDescription: String? {
    switch self {
    case .unavailable: return "Node capacity is unavailable. Check the Bridge connection and infrastructure permissions."
    case .invalidBudget: return "Enter positive Mbit/s that converts to whole bytes per second, or leave blank for default."
    case .rejected(let message): return message
    }
  }
}

struct NodeCapacityService {
  var gateway: GatewayClient = .shared

  static func overrideBytes(mbps: String) throws -> Double? {
    let text = mbps.trimmingCharacters(in: .whitespacesAndNewlines)
    if text.isEmpty { return nil }
    guard let value = Double(text), value.isFinite else { throw NodeCapacityError.invalidBudget }
    let bytes = value * 125_000
    guard bytes > 0, bytes <= 9_007_199_254_740_991,
      bytes.rounded(.towardZero) == bytes
    else { throw NodeCapacityError.invalidBudget }
    return bytes
  }

  func fetch(nodeId: String) async throws -> EdgeNodeCapacity {
    let id = Data("InfrastructureNode:\(nodeId)".utf8).base64EncodedString()
    let response = try await gateway.graphql(
      query: GQL.GetNodeCapacity, variables: ["id": id], responseType: NodeCapacityQueryResponse.self)
    guard let capacity = response.node?.capacity else { throw NodeCapacityError.unavailable }
    return capacity
  }

  func set(nodeId: String, revision: Int, egressMbps: String, ingressMbps: String) async throws -> EdgeNodeCapacity {
    let egress = try Self.overrideBytes(mbps: egressMbps)
    let ingress = try Self.overrideBytes(mbps: ingressMbps)
    let input: [String: Any] = [
      "nodeId": nodeId, "expectedRevision": revision,
      "egressBudgetBytesPerSec": egress.map { $0 as Any } ?? NSNull(),
      "ingressBudgetBytesPerSec": ingress.map { $0 as Any } ?? NSNull(),
    ]
    let response = try await gateway.graphql(
      query: GQL.SetNodeCapacity, variables: ["input": input], responseType: SetNodeCapacityResponse.self)
    guard response.setNodeCapacity.__typename == "InfrastructureNode",
      let capacity = response.setNodeCapacity.capacity
    else { throw NodeCapacityError.rejected(response.setNodeCapacity.message ?? "Capacity could not be saved") }
    return capacity
  }
}
