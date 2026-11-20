import Foundation

class StreamService {
  static let shared = StreamService()
  private let gateway = GatewayClient.shared

  private init() {}

  func listStreams() async throws -> [StreamSummary] {
    let query = """
      query {
        streamsConnection(page: { first: 50 }) {
          edges {
            node {
              id
              name
              ingest { state isLive }
              metrics {
                currentViewers
              }
            }
          }
          totalCount
        }
      }
      """

    let response = try await gateway.graphql(
      query: query,
      responseType: StreamsQueryResponse.self,
      partialData: .streamList)

    return response.streamsConnection.edges.map { $0.node.summary }
  }
}
