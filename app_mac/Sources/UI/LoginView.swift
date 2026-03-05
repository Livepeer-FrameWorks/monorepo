import SwiftUI

struct LoginView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var email = ""
  @State private var password = ""
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
        TextField("Email", text: $email)
          .textFieldStyle(.roundedBorder)
          .textContentType(.emailAddress)

        SecureField("Password", text: $password)
          .textFieldStyle(.roundedBorder)
          .textContentType(.password)

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
        .disabled(email.isEmpty || password.isEmpty || isLoading)
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

      HStack {
        Text("Gateway:")
          .font(.caption).foregroundStyle(.secondary)
        TextField("URL", text: $appState.gatewayBaseURL)
          .font(.caption)
          .textFieldStyle(.roundedBorder)
      }
    }
    .padding()
    .frame(width: 380, height: 420)
    .background(.regularMaterial)
  }

  private func login() {
    isLoading = true
    errorMessage = nil

    GatewayClient.shared.baseURL = appState.gatewayBaseURL

    Task {
      do {
        try await AuthService.shared.login(email: email, password: password, appState: appState)
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
}
