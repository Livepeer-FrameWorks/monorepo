import Foundation
import Combine

class AppState: ObservableObject {
  static let hostedBridgeURL = "https://bridge.frameworks.network"
  static let hostedContextName = "my-account"

  // Auth
  @Published var isAuthenticated = false
  @Published var userEmail: String?
  @Published var userId: String?
  @Published var tenantId: String?
  @Published var loginBridgeURLDraft = AppState.hostedBridgeURL

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
  @Published var streamLoadError: String?

  // Skipper
  @Published var skipperMessages: [SkipperMessage] = []
  @Published var skipperConversationId: String?

  // Gateway
  @Published var gatewayBaseURL: String = ""

  // CLI Integration
  @Published var cliAvailable = false
  @Published var cliVersion: String?
  @Published var currentContext: String = ""
  @Published var currentPersona: String = ""
  @Published var currentAccessMode: String = ""
  @Published var currentClusterID: String?
  @Published var availableContexts: [String] = []
  @Published var diagnosticOutput: String = ""
  @Published var isDiagnosticRunning = false
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
