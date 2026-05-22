import Foundation

class SkipperService {
  static let shared = SkipperService()
  private let gateway = GatewayClient.shared

  private init() {}

  // Skipper chat is subscription-only in the current GraphQL schema.
  func ask(question _: String, mode _: String = "full") async throws -> String {
    throw SkipperServiceError.subscriptionTransportRequired
  }

  func listConversations() async throws -> [SkipperConversation] {
    let query = """
      query {
        skipperConversations {
          id
          title
          updatedAt
        }
      }
      """

    let response = try await gateway.graphql(
      query: query,
      responseType: SkipperConversationsResponse.self)
    return response.skipperConversations
  }
}

struct SkipperConversationsResponse: Codable {
  let skipperConversations: [SkipperConversation]
}

struct SkipperConversation: Codable, Identifiable {
  let id: String
  let title: String?
  let updatedAt: String?
}

enum SkipperServiceError: LocalizedError {
  case subscriptionTransportRequired

  var errorDescription: String? {
    switch self {
    case .subscriptionTransportRequired:
      return "Skipper chat now requires GraphQL subscription transport; the tray client is not wired for it yet."
    }
  }
}
