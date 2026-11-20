import SwiftUI

struct ProvisionView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var enrollmentToken = ""
  @State private var bandwidthMbps = ""
  @State private var output = ""
  @State private var isRunning = false

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "shippingbox").foregroundStyle(Color.tnAccent)
        Text("Provision Edge").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      HStack {
        TextField("Enrollment token", text: $enrollmentToken)
          .textFieldStyle(.roundedBorder)
          .font(.system(.caption, design: .monospaced))
          .disabled(isRunning)

        if isRunning {
          ProgressView()
            .controlSize(.small)
            .padding(.leading, 4)
        } else {
          Button("Provision") { runProvision() }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .disabled(enrollmentToken.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
      }
      .padding(.horizontal)
      .padding(.vertical, 8)

      VStack(alignment: .leading, spacing: 4) {
        TextField("Bandwidth (Mbit/s, optional)", text: $bandwidthMbps)
          .textFieldStyle(.roundedBorder).disabled(isRunning)
        Text("Requires sign-in to set through the API. Leave blank to keep the budget; new nodes default to 1 Gbit/s.")
          .font(.caption2).foregroundStyle(.secondary)
      }
      .padding(.horizontal)
      .padding(.bottom, 8)

      Divider()

      ScrollViewReader { proxy in
        ScrollView {
          Text(output.isEmpty ? "Enter an enrollment token and press Provision." : output)
            .font(.system(.caption, design: .monospaced))
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding()
            .id("output")
        }
        .onChange(of: output) { _, _ in
          proxy.scrollTo("output", anchor: .bottom)
        }
      }

      if !output.isEmpty {
        Divider()
        HStack {
          Button("Copy") {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(output, forType: .string)
          }
          .buttonStyle(.bordered)
          .controlSize(.small)

          Button("Clear") { output = "" }
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
  }

  private func runProvision() {
    let token = enrollmentToken.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !token.isEmpty, !isRunning else { return }
    output = ""
    let requestedBandwidth = bandwidthMbps.trimmingCharacters(in: .whitespacesAndNewlines)
    if !requestedBandwidth.isEmpty {
      do { _ = try NodeCapacityService.overrideBytes(mbps: requestedBandwidth) }
      catch { output = error.localizedDescription; return }
      guard GatewayClient.shared.accessToken != nil else {
        output = "Sign in to set node bandwidth through the API, or leave the field blank and set it later with frameworks edge capacity set."
        return
      }
    }
    isRunning = true

    Task {
      do {
        let exitCode = try await CLIRunner.shared.runStreaming(
          ["edge", "provision", "--local", "--enrollment-token", token]
        ) { line in
          Task { @MainActor in
            output += line + "\n"
          }
        }

        if exitCode == 0 && !requestedBandwidth.isEmpty {
          do {
            let health = try await EdgeClient.shared.fetchHealth()
            let service = NodeCapacityService()
            let current = try await service.fetch(nodeId: health.nodeId)
            _ = try await service.set(nodeId: health.nodeId, revision: current.revision,
              egressMbps: requestedBandwidth, ingressMbps: requestedBandwidth)
            await MainActor.run { output += "\n[capacity saved: \(requestedBandwidth) Mbit/s per direction]\n" }
          } catch {
            await MainActor.run {
              output += "\n[edge provisioned; capacity was not saved: \(error.localizedDescription)]\n"
              output += "Set it later with frameworks edge capacity set.\n"
            }
          }
        }
        await MainActor.run {
          if exitCode != 0 {
            output += "\n[exited with code \(exitCode)]\n"
          }
          isRunning = false
        }
      } catch {
        await MainActor.run {
          output += "\n[error: \(error.localizedDescription)]\n"
          isRunning = false
        }
      }
    }
  }
}
