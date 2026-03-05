import Foundation

class ClusterService {
  static let shared = ClusterService()
  private let gateway = GatewayClient.shared

  private init() {}

  func listNodes(clusterId: String? = nil) async throws -> [ClusterNode] {
    // Use the nodes shorthand on NodesConnection (avoids Relay edge/cursor overhead).
    // Fragment is from pkg/graphql/operations/fragments/NodeListFields.gql via codegen.
    let query = GQL.NodeListFields + "\n" + """
      query($clusterId: String) {
        nodesConnection(clusterId: $clusterId, page: { first: 100 }) {
          nodes { ...NodeListFields }
        }
      }
      """

    var variables: [String: Any] = [:]
    if let clusterId { variables["clusterId"] = clusterId }

    let response = try await gateway.graphql(
      query: query,
      variables: variables,
      responseType: NodesQueryResponse.self)
    return response.nodesConnection.nodes
  }
}

// MARK: - Response Types

struct NodesQueryResponse: Codable {
  let nodesConnection: NodesConnectionData
}

struct NodesConnectionData: Codable {
  let nodes: [ClusterNode]
}

struct ClusterNode: Codable, Identifiable {
  let id: String
  let nodeId: String
  let clusterId: String
  let nodeName: String
  let nodeType: String
  let region: String?
  let externalIp: String?
  let internalIp: String?
  let lastHeartbeat: String?
  let liveState: NodeLiveState?

  var helmsmanURL: String? {
    // Prefer external IP (tray connects over network), internal as fallback
    if let ip = externalIp { return "http://\(ip):18007" }
    if let ip = internalIp { return "http://\(ip):18007" }
    return nil
  }
}

struct NodeLiveState: Codable {
  let cpuPercent: Double?
  let ramUsedBytes: UInt64?
  let ramTotalBytes: UInt64?
  let isHealthy: Bool?
  let activeStreams: Int?
  let updatedAt: String?
  let diskUsedBytes: UInt64?
  let diskTotalBytes: UInt64?
  let location: String?
}
