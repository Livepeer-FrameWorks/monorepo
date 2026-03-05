import Foundation

enum ServiceDomain: String {
  case user    // ~/Library/LaunchAgents — no admin needed
  case system  // /Library/LaunchDaemons — requires admin
  case none    // not installed
}

enum EdgeService: String, CaseIterable {
  case helmsman
  case mistserver
  case caddy

  var label: String { "com.livepeer.frameworks.\(rawValue)" }

  var displayName: String {
    switch self {
    case .helmsman: return "Helmsman"
    case .mistserver: return "MistServer"
    case .caddy: return "Caddy"
    }
  }

  var domain: ServiceDomain {
    if FileManager.default.fileExists(atPath: userPlistPath) { return .user }
    if FileManager.default.fileExists(atPath: systemPlistPath) { return .system }
    return .none
  }

  var plistPath: String? {
    switch domain {
    case .user: return userPlistPath
    case .system: return systemPlistPath
    case .none: return nil
    }
  }

  var userPlistPath: String {
    NSHomeDirectory() + "/Library/LaunchAgents/\(label).plist"
  }

  var systemPlistPath: String {
    "/Library/LaunchDaemons/\(label).plist"
  }
}

enum ServiceManager {
  static func isRunning(_ service: EdgeService) -> Bool {
    let domain = service.domain
    switch domain {
    case .user:
      let result = shell("launchctl print gui/\(getuid())/\(service.label)")
      return result.exitCode == 0
    case .system:
      let result = shell("launchctl print system/\(service.label)")
      return result.exitCode == 0
    case .none:
      return false
    }
  }

  static func start(_ service: EdgeService) -> Bool {
    guard let plist = service.plistPath else { return false }
    let domain = service.domain
    switch domain {
    case .user:
      let result = shell("launchctl bootstrap gui/\(getuid()) \(plist)")
      return result.exitCode == 0
    case .system:
      let result = shellWithAdmin("launchctl bootstrap system \(plist)")
      return result.exitCode == 0
    case .none:
      return false
    }
  }

  static func stop(_ service: EdgeService) -> Bool {
    let domain = service.domain
    switch domain {
    case .user:
      let result = shell("launchctl bootout gui/\(getuid())/\(service.label)")
      return result.exitCode == 0
    case .system:
      let result = shellWithAdmin("launchctl bootout system/\(service.label)")
      return result.exitCode == 0
    case .none:
      return false
    }
  }

  static func restart(_ service: EdgeService) -> Bool {
    _ = stop(service)
    usleep(500_000)
    return start(service)
  }

  static func isInstalled(_ service: EdgeService) -> Bool {
    service.domain != .none
  }

  static func detectedDomain() -> ServiceDomain {
    for service in EdgeService.allCases {
      let d = service.domain
      if d != .none { return d }
    }
    return .none
  }

  private static func shell(_ command: String) -> (output: String, exitCode: Int32) {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/bin/sh")
    process.arguments = ["-c", command]

    let pipe = Pipe()
    process.standardOutput = pipe
    process.standardError = pipe

    do {
      try process.run()
      process.waitUntilExit()
    } catch {
      return ("", 1)
    }

    let data = pipe.fileHandleForReading.readDataToEndOfFile()
    let output = String(data: data, encoding: .utf8) ?? ""
    return (output, process.terminationStatus)
  }

  private static func shellWithAdmin(_ command: String) -> (output: String, exitCode: Int32) {
    let escaped = command.replacingOccurrences(of: "\\", with: "\\\\")
      .replacingOccurrences(of: "\"", with: "\\\"")
    let script = "do shell script \"\(escaped)\" with administrator privileges"
    let result = shell("osascript -e '\(script)'")
    return result
  }
}
