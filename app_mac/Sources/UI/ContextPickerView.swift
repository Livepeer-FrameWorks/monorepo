import AppKit
import SwiftUI

struct ContextPickerView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var contexts: [CLIContextEntry] = []
  @State private var loading = true
  @State private var switching = false

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "arrow.triangle.branch").foregroundStyle(Color.tnAccent)
        Text("Contexts").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      if loading {
        Spacer()
        ProgressView("Loading contexts...")
        Spacer()
      } else if contexts.isEmpty {
        Spacer()
        VStack(spacing: 8) {
          Image(systemName: "exclamationmark.triangle")
            .font(.largeTitle).foregroundStyle(.secondary)
          Text("No contexts found")
            .font(.headline).foregroundStyle(.secondary)
          Text("Run 'frameworks setup' to create one.")
            .font(.caption).foregroundStyle(.tertiary)
        }
        Spacer()
      } else {
        ScrollView {
          VStack(spacing: 4) {
            ForEach(contexts, id: \.name) { ctx in
              contextRow(ctx)
            }
          }
          .padding()
        }
      }

      if !appState.cliAvailable {
        Divider()
        HStack {
          Image(systemName: "exclamationmark.triangle")
            .foregroundStyle(Color.tnOrange)
          Text("CLI not found")
            .font(.caption).foregroundStyle(.secondary)
          Spacer()
        }
        .padding(8)
      }
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear { loadContexts() }
  }

  private func contextRow(_ ctx: CLIContextEntry) -> some View {
    HStack {
      Circle()
        .fill(ctx.current ? Color.tnGreen : Color.secondary.opacity(0.3))
        .frame(width: 8, height: 8)
      Text(ctx.name).font(.body)
      if ctx.current {
        Text("active")
          .font(.caption2.bold())
          .padding(.horizontal, 6)
          .padding(.vertical, 2)
          .background(Color.tnGreen.opacity(0.2))
          .clipShape(Capsule())
      }
      Spacer()
      if !ctx.current {
        Button("Switch") { switchTo(ctx.name) }
          .buttonStyle(.bordered)
          .controlSize(.small)
          .disabled(switching)
      }
    }
    .padding(.vertical, 6)
    .padding(.horizontal, 12)
    .background(ctx.current ? Color.tnAccent.opacity(0.05) : .clear)
    .clipShape(RoundedRectangle(cornerRadius: 6))
  }

  private func loadContexts() {
    loading = true
    Task {
      let entries = await ConfigBridge.shared.loadContexts()
      await MainActor.run {
        contexts = entries
        loading = false
      }
    }
  }

  private func switchTo(_ name: String) {
    switching = true
    Task {
      let ok = await ConfigBridge.shared.switchContext(name)
      if ok {
        let ctx = await ConfigBridge.shared.loadCurrentContext()
        let entries = await ConfigBridge.shared.loadContexts()
        if let appDelegate = await MainActor.run(body: { NSApp.delegate as? AppDelegate }) {
          await appDelegate.applyHydratedContext(ctx, contextNames: entries.map(\.name))
        }
      }
      await MainActor.run {
        switching = false
        loadContexts()
      }
    }
  }
}
