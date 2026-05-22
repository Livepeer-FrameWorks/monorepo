import SwiftUI

struct LoginView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var isLoading = false
  @State private var errorMessage: String?
  @State private var statusMessage: String?
  @State private var manualCallback = ""
  @State private var loginTask: Task<Void, Never>?

  var body: some View {
    VStack(spacing: 16) {
      HStack {
        Text("Log In").font(.title2.bold())
        Spacer()
        Button(action: cancelAndClose) {
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
        if let status = statusMessage {
          Text(status)
            .font(.caption)
            .foregroundStyle(.secondary)
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

        Divider()

        HStack(spacing: 8) {
          TextField("Callback URL or code", text: $manualCallback)
            .textFieldStyle(.roundedBorder)
          Button("Submit Code", action: submitManualCode)
            .disabled(manualCallback.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
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
    statusMessage = nil

    GatewayClient.shared.baseURL = trimmed
    appState.gatewayBaseURL = trimmed

    loginTask?.cancel()
    loginTask = Task {
      do {
        try await AuthService.shared.loginWithBrowser(appState: appState)
        guard !Task.isCancelled else { return }
        await ensureCLIContextIfNeeded(bridgeURL: trimmed)
        await MainActor.run {
          isLoading = false
          loginTask = nil
          closePanel()
        }
      } catch {
        guard !Task.isCancelled else { return }
        await MainActor.run {
          errorMessage = error.localizedDescription
          isLoading = false
          loginTask = nil
        }
      }
    }
  }

  private func cancelAndClose() {
    cancelLogin()
    closePanel()
  }

  private func cancelLogin() {
    loginTask?.cancel()
    loginTask = nil
    AuthService.shared.cancelBrowserLogin()
    isLoading = false
    statusMessage = nil
  }

  private func submitManualCode() {
    let trimmed = manualCallback.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return }
    do {
      try AuthService.shared.submitBrowserLoginCallback(trimmed)
      manualCallback = ""
      errorMessage = nil
      statusMessage = "Code submitted. Finishing sign in..."
    } catch {
      statusMessage = nil
      errorMessage = error.localizedDescription
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
