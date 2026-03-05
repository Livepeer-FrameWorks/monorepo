import Foundation

class StreamService {
  static let shared = StreamService()
  private let gateway = GatewayClient.shared

  private init() {}

  func listStreams() async throws -> [StreamSummary] {
    let query = """
      query {
        streamsConnection {
          edges {
            node {
              id
              name
              is_active
              viewer_count
            }
          }
          totalCount
        }
      }
      """

    let response = try await gateway.graphql(
      query: query,
      responseType: StreamsQueryResponse.self)

    return response.streamsConnection.edges.map { edge in
      StreamSummary(
        id: edge.node.id,
        name: edge.node.name,
        isActive: edge.node.isActive,
        viewerCount: edge.node.viewerCount ?? 0)
    }
  }
}
