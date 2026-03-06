import SwiftUI

struct ProvisionView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var enrollmentToken = ""
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
            .disabled(enrollmentToken.trimmingCharacters(in: .whitespaces).isEmpty)
        }
      }
      .padding(.horizontal)
      .padding(.vertical, 8)

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
    let token = enrollmentToken.trimmingCharacters(in: .whitespaces)
    guard !token.isEmpty, !isRunning else { return }
    output = ""
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
