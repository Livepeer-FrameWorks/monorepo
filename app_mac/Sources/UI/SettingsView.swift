import SwiftUI

struct SettingsView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "gearshape").foregroundStyle(Color.tnAccent)
        Text("Settings").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      Form {
        Section("Connection") {
          TextField("Gateway URL", text: $appState.gatewayBaseURL)
          TextField("Edge URL", text: $appState.edgeBaseURL)
        }

        if appState.isAuthenticated {
          Section("Account") {
            LabeledContent("Email", value: appState.userEmail ?? "—")
            LabeledContent("Tenant", value: appState.tenantId ?? "—")

            Button("Log Out", role: .destructive) {
              Task {
                await AuthService.shared.logout(appState: appState)
                closePanel()
              }
            }
          }
        }

        Section("About") {
          LabeledContent("Version", value: Bundle.main.object(
            forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "dev")
          LabeledContent("Build", value: Bundle.main.object(
            forInfoDictionaryKey: "CFBundleVersion") as? String ?? "0")
        }
      }
      .formStyle(.grouped)
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
  }
}
