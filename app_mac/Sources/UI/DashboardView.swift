import SwiftUI

struct DashboardView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  var body: some View {
    VStack(spacing: 0) {
      // Header
      HStack {
        Circle()
          .fill(appState.edgeHealthy ? Color.tnGreen : Color.tnAccent)
          .frame(width: 8, height: 8)
        Text("FrameWorks").font(.headline)
        if appState.cliAvailable {
          Text(appState.currentContext)
            .font(.caption2.bold())
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Color.tnAccent.opacity(0.15))
            .clipShape(Capsule())
        }
        Spacer()
        if let email = appState.userEmail {
          Text(email).font(.caption).foregroundStyle(.secondary)
        }
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      ScrollView {
        VStack(spacing: 16) {
          if appState.edgeDetected {
            edgeSection
          }
          streamsSection
        }
        .padding()
      }
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear { loadStreams() }
  }

  // MARK: - Edge Section

  private var edgeSection: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Image(systemName: "server.rack")
          .foregroundStyle(appState.edgeHealthy ? Color.tnGreen : Color.tnOrange)
        Text("Edge Node").font(.subheadline.bold())
        Spacer()
        Text(appState.edgeOperationalMode ?? "unknown")
          .font(.caption).foregroundStyle(.secondary)
      }

      HStack(spacing: 16) {
        statBadge(
          icon: "play.circle",
          value: "\(appState.activeStreamCount)",
          label: "streams",
          color: .tnAccent)
        statBadge(
          icon: "person.2",
          value: "\(appState.totalViewers)",
          label: "viewers",
          color: .tnGreen)
        statBadge(
          icon: "arrow.up.arrow.down",
          value: formatBytes(appState.bandwidthUp + appState.bandwidthDown),
          label: "bandwidth",
          color: .tnCyan)
      }

      HStack(spacing: 8) {
        ForEach(EdgeService.allCases, id: \.rawValue) { service in
          if ServiceManager.isInstalled(service) {
            serviceChip(service)
          }
        }
      }
    }
    .padding()
    .background(Color.tnAccent.opacity(0.05))
    .clipShape(RoundedRectangle(cornerRadius: 8))
  }

  private func serviceChip(_ service: EdgeService) -> some View {
    let running = ServiceManager.isRunning(service)
    return HStack(spacing: 4) {
      Circle()
        .fill(running ? Color.tnGreen : Color.tnRed)
        .frame(width: 6, height: 6)
      Text(service.displayName)
        .font(.caption2)
    }
    .padding(.horizontal, 8)
    .padding(.vertical, 4)
    .background(Color.secondary.opacity(0.1))
    .clipShape(Capsule())
  }

  private func statBadge(icon: String, value: String, label: String, color: Color) -> some View {
    VStack(spacing: 2) {
      HStack(spacing: 4) {
        Image(systemName: icon).font(.caption2).foregroundStyle(color)
        Text(value).font(.system(.caption, design: .monospaced).bold())
      }
      Text(label).font(.caption2).foregroundStyle(.secondary)
    }
  }

  // MARK: - Streams Section

  private var streamsSection: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Image(systemName: "video").foregroundStyle(Color.tnAccent)
        Text("Streams").font(.subheadline.bold())
        Spacer()
        Text("\(appState.streams.count) total")
          .font(.caption).foregroundStyle(.secondary)
      }

      if appState.streams.isEmpty {
        Text("No streams yet")
          .font(.caption).foregroundStyle(.tertiary)
          .frame(maxWidth: .infinity, alignment: .center)
          .padding()
      } else {
        ForEach(appState.streams) { stream in
          HStack {
            Circle()
              .fill(stream.isActive ? Color.tnGreen : Color.secondary.opacity(0.3))
              .frame(width: 8, height: 8)
            Text(stream.name).font(.body)
            Spacer()
            if stream.isActive {
              HStack(spacing: 4) {
                Image(systemName: "person.2").font(.caption2)
                Text("\(stream.viewerCount)")
              }
              .font(.caption).foregroundStyle(Color.tnGreen)
            } else {
              Text("offline").font(.caption).foregroundStyle(.tertiary)
            }
          }
          .padding(.vertical, 2)
        }
      }
    }
    .padding()
    .background(Color.tnAccent.opacity(0.05))
    .clipShape(RoundedRectangle(cornerRadius: 8))
  }

  // MARK: - Data Loading

  private func loadStreams() {
    Task {
      do {
        let streams = try await StreamService.shared.listStreams()
        await MainActor.run {
          appState.streams = streams
        }
      } catch {
        // Silently fail — streams section shows empty state
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
