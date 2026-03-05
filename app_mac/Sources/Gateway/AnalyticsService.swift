import Foundation

class AnalyticsService {
  static let shared = AnalyticsService()
  private let gateway = GatewayClient.shared

  private init() {}

  func fetchOverview() async throws -> AnalyticsOverview {
    let query = """
      query {
        analytics {
          overview {
            totalStreams
            activeStreams
            totalViewers
            bandwidthUsed
          }
        }
      }
      """

    let response = try await gateway.graphql(
      query: query,
      responseType: AnalyticsQueryResponse.self)
    return response.analytics.overview
  }
}

struct AnalyticsQueryResponse: Codable {
  let analytics: AnalyticsData
}

struct AnalyticsData: Codable {
  let overview: AnalyticsOverview
}

struct AnalyticsOverview: Codable {
  let totalStreams: Int?
  let activeStreams: Int?
  let totalViewers: Int?
  let bandwidthUsed: Int?
}
