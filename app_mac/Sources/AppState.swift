import Foundation
import Combine

class AppState: ObservableObject {
  // Auth
  @Published var isAuthenticated = false
  @Published var userEmail: String?
  @Published var userId: String?
  @Published var tenantId: String?

  // Edge
  @Published var edgeDetected = false
  @Published var edgeHealthy = false
  @Published var edgeNodeId: String?
  @Published var edgeOperationalMode: String?
  @Published var edgeServiceDomain: ServiceDomain = .none
  @Published var activeStreamCount = 0
  @Published var totalViewers = 0
  @Published var bandwidthUp: UInt64 = 0
  @Published var bandwidthDown: UInt64 = 0

  // Platform
  @Published var streams: [StreamSummary] = []

  // Skipper
  @Published var skipperMessages: [SkipperMessage] = []
  @Published var skipperConversationId: String?

  // Gateway
  var gatewayBaseURL = "https://bridge.frameworks.network"
  var edgeBaseURL = "http://localhost:18007"
}

struct StreamSummary: Identifiable, Codable {
  let id: String
  let name: String
  let isActive: Bool
  let viewerCount: Int
}

struct SkipperMessage: Identifiable {
  let id = UUID()
  let role: String
  let content: String
  let timestamp = Date()
}
