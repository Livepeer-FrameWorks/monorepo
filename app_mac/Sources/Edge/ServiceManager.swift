import Foundation

enum EdgeService: String, CaseIterable {
  case helmsman
  case mistserver
  case caddy

  var label: String { "com.livepeer.frameworks.\(rawValue)" }
  var plistPath: String { "/Library/LaunchDaemons/\(label).plist" }

  var displayName: String {
    switch self {
    case .helmsman: return "Helmsman"
    case .mistserver: return "MistServer"
    case .caddy: return "Caddy"
    }
  }
}

enum ServiceManager {
  static func isRunning(_ service: EdgeService) -> Bool {
    let result = shell("launchctl print system/\(service.label)")
    return result.exitCode == 0
  }

  static func start(_ service: EdgeService) -> Bool {
    guard FileManager.default.fileExists(atPath: service.plistPath) else { return false }
    let result = shell("launchctl bootstrap system \(service.plistPath)")
    return result.exitCode == 0
  }

  static func stop(_ service: EdgeService) -> Bool {
    let result = shell("launchctl bootout system/\(service.label)")
    return result.exitCode == 0
  }

  static func restart(_ service: EdgeService) -> Bool {
    _ = stop(service)
    usleep(500_000)
    return start(service)
  }

  static func isInstalled(_ service: EdgeService) -> Bool {
    FileManager.default.fileExists(atPath: service.plistPath)
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
}
