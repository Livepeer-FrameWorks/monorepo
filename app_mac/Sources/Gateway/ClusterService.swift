import Foundation

class ClusterService {
  static let shared = ClusterService()
  private let gateway = GatewayClient.shared

  private init() {}

  func listNodes() async throws -> [ClusterNode] {
    let query = """
      query {
        nodes {
          id
          name
          status
          region
        }
      }
      """

    let response = try await gateway.graphql(
      query: query,
      responseType: NodesQueryResponse.self)
    return response.nodes
  }
}

struct NodesQueryResponse: Codable {
  let nodes: [ClusterNode]
}

struct ClusterNode: Codable, Identifiable {
  let id: String
  let name: String?
  let status: String?
  let region: String?
}
