import SwiftUI

struct DiagnosticsView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var catalog: CLIMenuCatalog?
  @State private var selectedActionKey = ""
  @State private var loadingCatalog = false
  @State private var confirmingActionKey: String?

  private var sections: [CLIMenuSection] {
    catalog?.sections ?? fallbackCatalog.sections
  }

  private var selectedAction: CLIMenuAction? {
    for section in sections {
      if let action = section.actions.first(where: { $0.key == selectedActionKey }) {
        return action
      }
    }
    return nil
  }

  private var fallbackCatalog: CLIMenuCatalog {
    let actions = [
      CLIMenuAction(
        key: "context-check",
        label: "Context Check",
        description: "Check reachability and persona/auth invariants.",
        args: ["context", "check"],
        longRunning: false,
        risk: nil,
        interactive: false),
      CLIMenuAction(
        key: "cli-update-check",
        label: "Check CLI Update",
        description: "Check whether a CLI update is available.",
        args: ["update", "--check"],
        longRunning: true,
        risk: nil,
        interactive: false),
    ]
    return CLIMenuCatalog(
      persona: appState.currentPersona,
      sections: [
        CLIMenuSection(
          key: "settings",
          label: "Settings & Contexts",
          recommended: true,
          actions: actions)
      ])
  }

  var body: some View {
    VStack(spacing: 0) {
      header
      Divider()
      controls
      Divider()
      outputView
      footer
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear(perform: loadCatalog)
    .onChange(of: appState.currentContext) { _, _ in loadCatalog() }
    .onChange(of: appState.currentPersona) { _, _ in loadCatalog() }
  }

  private var header: some View {
    HStack {
      Image(systemName: "terminal").foregroundStyle(Color.tnAccent)
      Text("Command Center").font(.title2.bold())
      Spacer()
      Button(action: closePanel) {
        Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
      }
      .buttonStyle(.plain)
    }
    .padding()
  }

  private var controls: some View {
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Picker("Action", selection: $selectedActionKey) {
          ForEach(sections) { section in
            Section(section.label) {
              ForEach(section.actions) { action in
                Text(action.label).tag(action.key)
              }
            }
          }
        }
        .pickerStyle(.menu)
        .disabled(loadingCatalog || appState.isDiagnosticRunning)

        if loadingCatalog {
          ProgressView().controlSize(.small)
        } else if appState.isDiagnosticRunning {
          ProgressView().controlSize(.small)
        } else {
          Button(action: runSelectedAction) {
            Image(systemName: confirmingActionKey == selectedActionKey ? "exclamationmark.triangle.fill" : "play.fill")
          }
          .buttonStyle(.bordered)
          .controlSize(.small)
          .disabled(selectedAction == nil || selectedAction?.interactive == true)
        }
      }

      if let action = selectedAction {
        VStack(alignment: .leading, spacing: 6) {
          Text(action.commandText)
            .font(.system(.caption, design: .monospaced))
            .textSelection(.enabled)
          if let description = action.description, !description.isEmpty {
            Text(description)
              .font(.caption2)
              .foregroundStyle(.secondary)
          }
          HStack(spacing: 6) {
            if action.longRunning {
              tag("long-running", color: .tnCyan)
            }
            if let risk = action.risk, !risk.isEmpty {
              tag(risk, color: .tnOrange)
            }
            if action.interactive {
              tag("interactive CLI", color: .tnPurple)
            }
          }
        }
      }
    }
    .padding(.horizontal)
    .padding(.vertical, 10)
  }

  private var outputView: some View {
    ScrollViewReader { proxy in
      ScrollView {
        Text(appState.diagnosticOutput.isEmpty ? "Pick a CLI action and press run." : appState.diagnosticOutput)
          .font(.system(.caption, design: .monospaced))
          .frame(maxWidth: .infinity, alignment: .leading)
          .padding()
          .id("output")
      }
      .onChange(of: appState.diagnosticOutput) { _, _ in
        proxy.scrollTo("output", anchor: .bottom)
      }
    }
  }

  @ViewBuilder
  private var footer: some View {
    if !appState.diagnosticOutput.isEmpty {
      Divider()
      HStack {
        Button("Copy") {
          NSPasteboard.general.clearContents()
          NSPasteboard.general.setString(appState.diagnosticOutput, forType: .string)
        }
        .buttonStyle(.bordered)
        .controlSize(.small)

        Button("Clear") {
          appState.diagnosticOutput = ""
          confirmingActionKey = nil
        }
        .buttonStyle(.bordered)
        .controlSize(.small)

        Spacer()
      }
      .padding(8)
    }
  }

  private func tag(_ label: String, color: Color) -> some View {
    Text(label)
      .font(.caption2.bold())
      .padding(.horizontal, 6)
      .padding(.vertical, 2)
      .background(color.opacity(0.15))
      .foregroundStyle(color)
      .clipShape(Capsule())
  }

  private func loadCatalog() {
    guard appState.cliAvailable else {
      catalog = nil
      ensureSelectedActionAvailable()
      return
    }

    loadingCatalog = true
    Task {
      let loaded = await ConfigBridge.shared.loadMenuCatalog()
      await MainActor.run {
        catalog = loaded
        loadingCatalog = false
        ensureSelectedActionAvailable()
      }
    }
  }

  private func ensureSelectedActionAvailable() {
    let allActions = sections.flatMap(\.actions)
    if !allActions.contains(where: { $0.key == selectedActionKey }) {
      selectedActionKey = allActions.first?.key ?? ""
      confirmingActionKey = nil
    }
  }

  private func runSelectedAction() {
    guard !appState.isDiagnosticRunning, let action = selectedAction else { return }
    guard !action.interactive else {
      appState.diagnosticOutput = "[interactive CLI action: run this in Terminal]\n\(action.commandText)\n"
      return
    }

    if let risk = action.risk, !risk.isEmpty, confirmingActionKey != action.key {
      confirmingActionKey = action.key
      appState.diagnosticOutput = "[confirm required]\nPress run again to execute:\n\(action.commandText)\n"
      return
    }

    confirmingActionKey = nil
    appState.diagnosticOutput = "$ \(action.commandText)\n"
    appState.isDiagnosticRunning = true

    Task {
      do {
        let exitCode = try await CLIRunner.shared.runStreaming(action.args) { line in
          Task { @MainActor in
            appState.diagnosticOutput += line + "\n"
          }
        }
        await MainActor.run {
          if exitCode != 0 {
            appState.diagnosticOutput += "\n[exited with code \(exitCode)]\n"
          }
          appState.isDiagnosticRunning = false
        }
      } catch {
        await MainActor.run {
          appState.diagnosticOutput += "\n[error: \(error.localizedDescription)]\n"
          appState.isDiagnosticRunning = false
        }
      }
    }
  }
}
