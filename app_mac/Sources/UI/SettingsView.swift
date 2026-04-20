import ServiceManagement
import SwiftUI

struct SettingsView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void
  @State private var launchAtLogin = false

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
        Section("General") {
          if #available(macOS 13.0, *) {
            Toggle("Launch at login", isOn: $launchAtLogin)
              .onChange(of: launchAtLogin) { _, newValue in
                toggleLoginItem(newValue)
              }
          }
        }

        Section("Connection") {
          if appState.cliAvailable {
            LabeledContent(
              "Context",
              value: appState.currentContext.isEmpty ? "(no context — run 'frameworks setup')" : appState.currentContext
            )
          }
          LabeledContent(
            "Bridge URL",
            value: appState.gatewayBaseURL.isEmpty ? "(no context)" : appState.gatewayBaseURL
          )
        }

        if appState.cliAvailable {
          Section("CLI") {
            LabeledContent("Version", value: appState.cliVersion ?? "unknown")
            LabeledContent("Status", value: "Connected")
          }
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
    .onAppear { loadLoginItemStatus() }
  }

  private func loadLoginItemStatus() {
    if #available(macOS 13.0, *) {
      launchAtLogin = SMAppService.mainApp.status == .enabled
    }
  }

  private func toggleLoginItem(_ enable: Bool) {
    if #available(macOS 13.0, *) {
      do {
        if enable {
          try SMAppService.mainApp.register()
        } else {
          try SMAppService.mainApp.unregister()
        }
      } catch {
        launchAtLogin = SMAppService.mainApp.status == .enabled
      }
    }
  }
}
