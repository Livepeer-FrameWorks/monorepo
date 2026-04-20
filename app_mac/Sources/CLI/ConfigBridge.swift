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

struct CLIPath: Decodable {
  let kind: String
  let path: String
}

actor ConfigBridge {
  static let shared = ConfigBridge()

  private var fileWatcher: DispatchSourceFileSystemObject?
  private var cachedConfigPath: String?
  private var pendingOnChange: (@Sendable () -> Void)?

  func loadContexts() async -> [CLIContextEntry] {
    await rearmIfDeferred()
    guard let entries: [CLIContextEntry] = try? await CLIRunner.shared.runJSON(
      ["context", "list"], as: [CLIContextEntry].self
    ) else { return [] }
    return entries
  }

  func loadCurrentContext() async -> CLIContext? {
    await rearmIfDeferred()
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

  // rearmIfDeferred picks up the config file once `frameworks setup`
  // has created it on a fresh install, without requiring a tray
  // restart. armWatcher returns early when the file doesn't exist yet
  // and records the handler in pendingOnChange; every refresh path
  // retries until the watcher is live.
  private func rearmIfDeferred() async {
    guard fileWatcher == nil, let handler = pendingOnChange else { return }
    await armWatcher(onChange: handler)
  }

  func resolveConfigPath() async -> String? {
    if let cached = cachedConfigPath { return cached }
    guard
      let out: CLIPath = try? await CLIRunner.shared.runJSON(
        ["config", "path", "--kind", "config"], as: CLIPath.self)
    else { return nil }
    cachedConfigPath = out.path
    return out.path
  }

  func startWatching(onChange: @escaping @Sendable () -> Void) {
    Task { await self.armWatcher(onChange: onChange) }
  }

  private func armWatcher(onChange: @escaping @Sendable () -> Void) async {
    fileWatcher?.cancel()
    fileWatcher = nil
    pendingOnChange = onChange

    guard let configPath = await resolveConfigPath() else { return }
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
    pendingOnChange = nil
  }

  func stopWatching() {
    fileWatcher?.cancel()
    fileWatcher = nil
    pendingOnChange = nil
  }
}
