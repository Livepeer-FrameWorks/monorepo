import Foundation

struct CLIContext: Decodable {
  let name: String
  let clusterID: String?
  let endpoints: CLIEndpoints

  enum CodingKeys: String, CodingKey {
    case name
    case clusterID = "cluster_id"
    case endpoints
  }
}

struct CLIEndpoints: Decodable {
  let bridgeURL: String

  enum CodingKeys: String, CodingKey {
    case bridgeURL = "bridge_url"
  }
}

struct CLIContextEntry: Decodable {
  let name: String
  let current: Bool
}

actor ConfigBridge {
  static let shared = ConfigBridge()

  private var fileWatcher: DispatchSourceFileSystemObject?

  func loadContexts() async -> [CLIContextEntry] {
    guard let entries: [CLIContextEntry] = try? await CLIRunner.shared.runJSON(
      ["context", "list"], as: [CLIContextEntry].self
    ) else { return [] }
    return entries
  }

  func loadCurrentContext() async -> CLIContext? {
    return try? await CLIRunner.shared.runJSON(
      ["context", "show"], as: CLIContext.self
    )
  }

  func switchContext(_ name: String) async -> Bool {
    guard let result = try? await CLIRunner.shared.run(["context", "use", name]) else {
      return false
    }
    return result.exitCode == 0
  }

  func startWatching(onChange: @escaping @Sendable () -> Void) {
    armWatcher(onChange: onChange)
  }

  private func armWatcher(onChange: @escaping @Sendable () -> Void) {
    fileWatcher?.cancel()
    fileWatcher = nil

    let configPath = NSHomeDirectory() + "/.frameworks/config.yaml"
    guard FileManager.default.fileExists(atPath: configPath) else { return }

    let fd = open(configPath, O_EVTONLY)
    guard fd >= 0 else { return }

    let source = DispatchSource.makeFileSystemObjectSource(
      fileDescriptor: fd,
      eventMask: [.write, .rename],
      queue: .global()
    )
    source.setEventHandler { [weak self] in
      onChange()
      let flags = source.data
      if flags.contains(.rename) {
        Task { await self?.armWatcher(onChange: onChange) }
      }
    }
    source.setCancelHandler { close(fd) }
    source.resume()
    fileWatcher = source
  }

  func stopWatching() {
    fileWatcher?.cancel()
    fileWatcher = nil
  }
}
