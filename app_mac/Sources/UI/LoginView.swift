import SwiftUI

struct LoginView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var isLoading = false
  @State private var errorMessage: String?

  var body: some View {
    VStack(spacing: 16) {
      HStack {
        Text("Log In").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill")
            .foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }

      VStack(spacing: 12) {
        if appState.currentContext.isEmpty {
          TextField("Bridge URL", text: $appState.loginBridgeURLDraft)
            .textFieldStyle(.roundedBorder)
        }

        if let error = errorMessage {
          Text(error)
            .font(.caption)
            .foregroundStyle(Color.tnRed)
        }

        Button(action: login) {
          HStack {
            if isLoading {
              ProgressView().controlSize(.small)
            }
            Text("Sign In with Browser")
          }
          .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .tint(Color.tnAccent)
        .disabled(!canSubmit)
        .keyboardShortcut(.defaultAction)
      }
      Spacer()
    }
    .padding()
    .frame(width: 380, height: 420)
    .background(.regularMaterial)
  }

  private var canSubmit: Bool {
    let hasBridgeURL = !appState.gatewayBaseURL.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
      || !appState.loginBridgeURLDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    return hasBridgeURL && !isLoading
  }

  private func login() {
    let contextURL = appState.gatewayBaseURL.trimmingCharacters(in: .whitespacesAndNewlines)
    let draftURL = appState.loginBridgeURLDraft.trimmingCharacters(in: .whitespacesAndNewlines)
    let trimmed = contextURL.isEmpty ? draftURL : contextURL
    guard !trimmed.isEmpty else {
      errorMessage = "Bridge URL is required."
      return
    }

    isLoading = true
    errorMessage = nil

    GatewayClient.shared.baseURL = trimmed
    appState.gatewayBaseURL = trimmed

    Task {
      do {
        try await AuthService.shared.loginWithBrowser(appState: appState)
        await ensureCLIContextIfNeeded(bridgeURL: trimmed)
        await MainActor.run {
          closePanel()
        }
      } catch {
        await MainActor.run {
          errorMessage = error.localizedDescription
          isLoading = false
        }
      }
    }
  }

  private func ensureCLIContextIfNeeded(bridgeURL: String) async {
    let hasContext = await MainActor.run {
      !appState.currentContext.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }
    guard !hasContext else { return }
    guard await MainActor.run(body: { appState.cliAvailable }) else { return }
    guard await ConfigBridge.shared.ensureUserContext(bridgeURL: bridgeURL) else { return }

    let contexts = await ConfigBridge.shared.loadContexts()
    let current = await ConfigBridge.shared.loadCurrentContext()

    await MainActor.run {
      appState.availableContexts = contexts.map(\.name)
      if let current {
        appState.currentContext = current.name
        appState.currentPersona = current.persona ?? ""
        appState.currentAccessMode = current.accessMode ?? "local"
        appState.currentClusterID = current.clusterID
        appState.gatewayBaseURL = current.endpoints.bridgeURL
        GatewayClient.shared.baseURL = current.endpoints.bridgeURL
      }
    }
  }
}
