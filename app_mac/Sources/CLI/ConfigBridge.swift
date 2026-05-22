import Foundation

struct CLIContext: Decodable {
  let name: String
  let clusterID: String?
  let persona: String?
  let accessMode: String?
  let endpoints: CLIEndpoints

  enum CodingKeys: String, CodingKey {
    case name
    case clusterID = "cluster_id"
    case persona
    case accessMode = "access_mode"
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

struct CLIMenuCatalog: Decodable {
  let persona: String?
  let sections: [CLIMenuSection]
}

struct CLIMenuSection: Decodable, Identifiable {
  var id: String { key }
  let key: String
  let label: String
  let recommended: Bool
  let actions: [CLIMenuAction]
}

struct CLIMenuAction: Decodable, Identifiable, Hashable {
  var id: String { key }
  let key: String
  let label: String
  let description: String?
  let args: [String]
  let longRunning: Bool
  let risk: String?
  let interactive: Bool

  enum CodingKeys: String, CodingKey {
    case key
    case label
    case description
    case args
    case longRunning = "long_running"
    case risk
    case interactive
  }

  var commandText: String {
    (["frameworks"] + args).map(shellQuote).joined(separator: " ")
  }

  private func shellQuote(_ value: String) -> String {
    guard !value.isEmpty else { return "''" }
    let safe = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_+-./:=,@%")
    if value.unicodeScalars.allSatisfy({ safe.contains($0) }) {
      return value
    }
    return "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
  }
}

struct CLICommandCatalog: Decodable {
  let commands: [CLICommandEntry]
}

struct CLICommandEntry: Decodable, Identifiable, Hashable {
  var id: String { command }
  let path: [String]
  let command: String
  let use: String
  let short: String?
  let long: String?
  let runnable: Bool
  let interactive: Bool?
  let hidden: Bool?
  let deprecated: String?
  let risk: String?
  let arguments: [CLICommandArgument]?
  let flags: [CLICommandFlag]?
}

struct CLICommandArgument: Decodable, Identifiable, Hashable {
  var id: String { name }
  let name: String
  let raw: String
  let required: Bool?
  let variadic: Bool?
}

struct CLICommandFlag: Decodable, Identifiable, Hashable {
  var id: String { name }
  let name: String
  let shorthand: String?
  let usage: String?
  let `default`: String?
  let type: String?
  let scope: String
  let source: String?
  let required: Bool?
  let sensitive: Bool?
  let confirmation: Bool?
  let hidden: Bool?
  let deprecated: String?
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

  func loadMenuCatalog() async -> CLIMenuCatalog? {
    await rearmIfDeferred()
    return try? await CLIRunner.shared.runJSON(
      ["menu"], as: CLIMenuCatalog.self
    )
  }

  func loadCommandCatalog() async -> CLICommandCatalog? {
    await rearmIfDeferred()
    return try? await CLIRunner.shared.runJSON(
      ["commands"], as: CLICommandCatalog.self
    )
  }

  func ensureUserContext(bridgeURL: String) async -> Bool {
    let target = AppState.hostedContextName
    let entries = await loadContexts()
    let exists = entries.contains { $0.name == target }

    if !exists {
      guard let created = try? await CLIRunner.shared.run(["context", "create", target]),
            created.exitCode == 0 else {
        return false
      }
    }

    guard let persona = try? await CLIRunner.shared.run(
      ["context", "set-persona", "user", "--context", target]),
      persona.exitCode == 0
    else { return false }

    guard let url = try? await CLIRunner.shared.run(
      ["context", "set-url", "bridge", bridgeURL, "--context", target]),
      url.exitCode == 0
    else { return false }

    return await switchContext(target)
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
