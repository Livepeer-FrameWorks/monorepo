import Foundation

class SkipperService {
  static let shared = SkipperService()
  private let gateway = GatewayClient.shared

  private init() {}

  // Simple query mode — sends a question, gets a full response.
  // TODO: streaming via GraphQL subscription (skipperChat) for token-by-token rendering
  func ask(question: String, mode: String = "full") async throws -> String {
    let query = """
      mutation($input: SkipperChatInput!) {
        skipperChat(input: $input) {
          ... on SkipperDone {
            content
            conversationId
          }
        }
      }
      """

    let variables: [String: Any] = [
      "input": [
        "message": question,
        "mode": mode,
      ]
    ]

    let response = try await gateway.graphql(
      query: query,
      variables: variables,
      responseType: SkipperChatResponse.self)

    return response.skipperChat.content ?? "No response"
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

struct SkipperChatResponse: Codable {
  let skipperChat: SkipperChatResult
}

struct SkipperChatResult: Codable {
  let content: String?
  let conversationId: String?
}

struct SkipperConversationsResponse: Codable {
  let skipperConversations: [SkipperConversation]
}

struct SkipperConversation: Codable, Identifiable {
  let id: String
  let title: String?
  let updatedAt: String?
}
