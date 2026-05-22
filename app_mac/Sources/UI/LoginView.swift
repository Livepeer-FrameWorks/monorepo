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
        TextField("Email", text: $appState.loginEmailDraft)
          .textFieldStyle(.roundedBorder)
          .textContentType(.emailAddress)

        SecureField("Password", text: $appState.loginPasswordDraft)
          .textFieldStyle(.roundedBorder)
          .textContentType(.password)

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
            Text("Log In")
          }
          .frame(maxWidth: .infinity)
        }
        .buttonStyle(.borderedProminent)
        .tint(Color.tnAccent)
        .disabled(appState.loginEmailDraft.isEmpty || appState.loginPasswordDraft.isEmpty || isLoading)
        .keyboardShortcut(.defaultAction)
      }

      Divider()

      Text("Or connect with a wallet")
        .font(.caption)
        .foregroundStyle(.secondary)

      Button(action: {}) {
        HStack {
          Image(systemName: "wallet.pass")
          Text("Wallet Login")
        }
        .frame(maxWidth: .infinity)
      }
      .buttonStyle(.bordered)
      .disabled(true)

      Spacer()
    }
    .padding()
    .frame(width: 380, height: 420)
    .background(.regularMaterial)
  }

  private func login() {
    let email = appState.loginEmailDraft.trimmingCharacters(in: .whitespacesAndNewlines)
    let password = appState.loginPasswordDraft
    let contextURL = appState.gatewayBaseURL.trimmingCharacters(in: .whitespacesAndNewlines)
    let draftURL = appState.loginBridgeURLDraft.trimmingCharacters(in: .whitespacesAndNewlines)
    let trimmed = contextURL.isEmpty ? draftURL : contextURL
    guard !email.isEmpty && !password.isEmpty else {
      errorMessage = "Email and password are required."
      return
    }
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
        try await AuthService.shared.login(
          email: email,
          password: password,
          appState: appState
        )
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
        appState.gatewayBaseURL = current.endpoints.bridgeURL
        GatewayClient.shared.baseURL = current.endpoints.bridgeURL
      }
    }
  }
}
