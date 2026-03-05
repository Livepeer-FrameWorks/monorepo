import Foundation

class EdgePoller {
  private let appState: AppState
  private var timer: Timer?
  private let interval: TimeInterval = 10

  init(appState: AppState) {
    self.appState = appState
  }

  func start() {
    poll()
    timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
      self?.poll()
    }
  }

  func stop() {
    timer?.invalidate()
    timer = nil
  }

  private func poll() {
    Task {
      do {
        async let health = EdgeClient.shared.fetchHealth()
        async let streams = EdgeClient.shared.fetchStreams()
        async let metrics = EdgeClient.shared.fetchMetrics()

        let h = try await health
        let s = try await streams
        let m = try await metrics

        await MainActor.run {
          appState.edgeDetected = true
          appState.edgeHealthy = h.healthy
          appState.edgeNodeId = h.nodeId
          appState.activeStreamCount = s.count
          appState.totalViewers = m.totalViewers
          appState.bandwidthUp = m.bandwidthUp
          appState.bandwidthDown = m.bandwidthDown
        }
      } catch {
        await MainActor.run {
          appState.edgeDetected = false
          appState.edgeHealthy = false
        }
      }
    }
  }
}
