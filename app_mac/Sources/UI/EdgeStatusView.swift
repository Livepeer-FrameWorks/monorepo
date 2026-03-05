import SwiftUI

struct EdgeStatusView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var streams: [EdgeStream] = []

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "server.rack")
          .foregroundStyle(appState.edgeHealthy ? Color.tnGreen : Color.tnOrange)
        Text("Edge Node").font(.title2.bold())
        if appState.edgeServiceDomain != .none {
          Text(appState.edgeServiceDomain == .user ? "user" : "system")
            .font(.caption2.bold())
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(appState.edgeServiceDomain == .user ? Color.tnGreen.opacity(0.2) : Color.tnOrange.opacity(0.2))
            .clipShape(Capsule())
        }
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      ScrollView {
        VStack(spacing: 16) {
          // Service controls
          VStack(alignment: .leading, spacing: 8) {
            Text("Services").font(.subheadline.bold())
            ForEach(EdgeService.allCases, id: \.rawValue) { service in
              if ServiceManager.isInstalled(service) {
                serviceRow(service)
              }
            }
          }
          .padding()
          .background(Color.tnAccent.opacity(0.05))
          .clipShape(RoundedRectangle(cornerRadius: 8))

          // Metrics
          if appState.edgeDetected {
            metricsSection
          }

          // Active streams
          if !streams.isEmpty {
            activeStreamsSection
          }
        }
        .padding()
      }
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear { loadEdgeStreams() }
  }

  private func serviceRow(_ service: EdgeService) -> some View {
    let running = ServiceManager.isRunning(service)
    return HStack {
      Circle()
        .fill(running ? Color.tnGreen : Color.tnRed)
        .frame(width: 8, height: 8)
      Text(service.displayName).font(.body)
      Spacer()
      Button(running ? "Stop" : "Start") {
        if running {
          _ = ServiceManager.stop(service)
        } else {
          _ = ServiceManager.start(service)
        }
      }
      .buttonStyle(.bordered)
      .controlSize(.small)

      Button("Restart") {
        _ = ServiceManager.restart(service)
      }
      .buttonStyle(.bordered)
      .controlSize(.small)
      .disabled(!running)
    }
  }

  private var metricsSection: some View {
    VStack(alignment: .leading, spacing: 8) {
      Text("Metrics").font(.subheadline.bold())
      HStack(spacing: 24) {
        metricItem(icon: "arrow.up", label: "Up", value: formatBytes(appState.bandwidthUp), color: .tnCyan)
        metricItem(icon: "arrow.down", label: "Down", value: formatBytes(appState.bandwidthDown), color: .tnAccent)
        metricItem(icon: "person.2", label: "Viewers", value: "\(appState.totalViewers)", color: .tnGreen)
        metricItem(icon: "play.circle", label: "Streams", value: "\(appState.activeStreamCount)", color: .tnPurple)
      }
    }
    .padding()
    .background(Color.tnAccent.opacity(0.05))
    .clipShape(RoundedRectangle(cornerRadius: 8))
  }

  private func metricItem(icon: String, label: String, value: String, color: Color) -> some View {
    VStack(spacing: 4) {
      Image(systemName: icon).foregroundStyle(color)
      Text(value).font(.system(.caption, design: .monospaced).bold())
      Text(label).font(.caption2).foregroundStyle(.secondary)
    }
  }

  private var activeStreamsSection: some View {
    VStack(alignment: .leading, spacing: 8) {
      Text("Active Streams").font(.subheadline.bold())
      ForEach(streams) { stream in
        HStack {
          Text(stream.name).font(.body)
          Spacer()
          HStack(spacing: 12) {
            Label("\(stream.viewers)", systemImage: "person.2")
            Label(formatBytes(stream.downBytes), systemImage: "arrow.down")
          }
          .font(.caption).foregroundStyle(.secondary)
        }
        .padding(.vertical, 2)
      }
    }
    .padding()
    .background(Color.tnGreen.opacity(0.05))
    .clipShape(RoundedRectangle(cornerRadius: 8))
  }

  private func loadEdgeStreams() {
    Task {
      if let response = try? await EdgeClient.shared.fetchStreams() {
        await MainActor.run { streams = response.streams }
      }
    }
  }

  private func formatBytes(_ bytes: UInt64) -> String {
    let formatter = ByteCountFormatter()
    formatter.countStyle = .binary
    formatter.allowedUnits = [.useKB, .useMB, .useGB]
    return formatter.string(fromByteCount: Int64(bytes)) + "/s"
  }
}
