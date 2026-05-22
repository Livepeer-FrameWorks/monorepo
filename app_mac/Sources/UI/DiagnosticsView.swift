import SwiftUI

struct DiagnosticsView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var selectedCommand = DiagnosticCommand.contextCheck

  enum DiagnosticCommand: String {
    case edgeStatus = "Edge Status"
    case edgeDoctor = "Edge Doctor"
    case contextCheck = "Context"
    case dnsDoctor = "DNS"
    case meshStatus = "Mesh"
    case servicesHealth = "Services"
    case edgeLogs = "Logs"
    case edgeUpdate = "Update"
    case cliUpdate = "CLI Update"

    var args: [String] {
      switch self {
      case .edgeStatus: return ["edge", "status"]
      case .edgeDoctor: return ["edge", "doctor"]
      case .contextCheck: return ["context", "check"]
      case .dnsDoctor: return ["dns", "doctor"]
      case .meshStatus: return ["mesh", "status"]
      case .servicesHealth: return ["services", "health"]
      case .edgeLogs: return ["edge", "logs", "--tail", "100"]
      case .edgeUpdate: return ["edge", "update"]
      case .cliUpdate: return ["update"]
      }
    }
  }

  private var availableCommands: [DiagnosticCommand] {
    var commands: [DiagnosticCommand] = [.contextCheck]
    let persona = appState.currentPersona
    let hasLocalEdge = appState.edgeDetected || appState.edgeServiceDomain != .none

    if hasLocalEdge || persona == "selfhosted" || persona == "platform" {
      commands.append(contentsOf: [.edgeStatus, .edgeDoctor, .edgeLogs, .edgeUpdate])
    }

    if persona == "platform" {
      commands.append(contentsOf: [.dnsDoctor, .meshStatus, .servicesHealth])
    }

    commands.append(.cliUpdate)
    return commands
  }

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "stethoscope").foregroundStyle(Color.tnAccent)
        Text("Diagnostics").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      HStack {
        Picker("Command", selection: $selectedCommand) {
          ForEach(availableCommands, id: \.self) { cmd in
            Text(cmd.rawValue).tag(cmd)
          }
        }
        .pickerStyle(.menu)

        if appState.isDiagnosticRunning {
          ProgressView()
            .controlSize(.small)
            .padding(.leading, 4)
        } else {
          Button(action: runDiagnostic) {
            Image(systemName: "play.fill")
          }
          .buttonStyle(.bordered)
          .controlSize(.small)
        }
      }
      .padding(.horizontal)
      .padding(.vertical, 8)

      Divider()

      ScrollViewReader { proxy in
        ScrollView {
          Text(appState.diagnosticOutput.isEmpty ? "Press play to run diagnostics." : appState.diagnosticOutput)
            .font(.system(.caption, design: .monospaced))
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding()
            .id("output")
        }
        .onChange(of: appState.diagnosticOutput) { _, _ in
          proxy.scrollTo("output", anchor: .bottom)
        }
      }

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
          }
          .buttonStyle(.bordered)
          .controlSize(.small)

          Spacer()
        }
        .padding(8)
      }
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear(perform: ensureSelectedCommandAvailable)
    .onChange(of: appState.currentPersona) { _, _ in ensureSelectedCommandAvailable() }
    .onChange(of: appState.edgeDetected) { _, _ in ensureSelectedCommandAvailable() }
    .onChange(of: appState.edgeServiceDomain) { _, _ in ensureSelectedCommandAvailable() }
  }

  private func runDiagnostic() {
    guard !appState.isDiagnosticRunning else { return }
    ensureSelectedCommandAvailable()
    appState.diagnosticOutput = ""
    appState.isDiagnosticRunning = true
    let args = selectedCommand.args

    Task {
      do {
        let exitCode = try await CLIRunner.shared.runStreaming(args) { line in
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

  private func ensureSelectedCommandAvailable() {
    let commands = availableCommands
    if !commands.contains(selectedCommand), let first = commands.first {
      selectedCommand = first
    }
  }
}
