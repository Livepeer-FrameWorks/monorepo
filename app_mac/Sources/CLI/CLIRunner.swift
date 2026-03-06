import Foundation

struct CLIResult {
  let stdout: String
  let stderr: String
  let exitCode: Int32
}

actor CLIRunner {
  static let shared = CLIRunner()

  private var binaryPath: String?

  func isAvailable() async -> Bool {
    return await findBinary() != nil
  }

  func version() async -> String? {
    guard let result = try? await run(["version"]),
          result.exitCode == 0 else { return nil }
    return result.stdout.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  func run(_ args: [String], jsonOutput: Bool = false, environment: [String: String]? = nil) async throws -> CLIResult {
    guard let binary = await findBinary() else {
      throw CLIError.notFound
    }

    var fullArgs = args
    if jsonOutput {
      fullArgs.append(contentsOf: ["--output", "json"])
    }

    let process = Process()
    process.executableURL = URL(fileURLWithPath: binary)
    process.arguments = fullArgs

    var env = ProcessInfo.processInfo.environment
    if let token = KeychainHelper.load(key: "access_token") {
      env["FW_JWT"] = token
    }
    if let extra = environment {
      env.merge(extra) { _, new in new }
    }
    process.environment = env

    let stdoutPipe = Pipe()
    let stderrPipe = Pipe()
    process.standardOutput = stdoutPipe
    process.standardError = stderrPipe

    try process.run()

    let stdoutData = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
    let stderrData = stderrPipe.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()

    return CLIResult(
      stdout: String(data: stdoutData, encoding: .utf8) ?? "",
      stderr: String(data: stderrData, encoding: .utf8) ?? "",
      exitCode: process.terminationStatus
    )
  }

  func runStreaming(_ args: [String], onLine: @escaping @Sendable (String) -> Void) async throws -> Int32 {
    guard let binary = await findBinary() else {
      throw CLIError.notFound
    }

    let process = Process()
    process.executableURL = URL(fileURLWithPath: binary)
    process.arguments = args

    var env = ProcessInfo.processInfo.environment
    if let token = KeychainHelper.load(key: "access_token") {
      env["FW_JWT"] = token
    }
    process.environment = env

    let stdoutPipe = Pipe()
    let stderrPipe = Pipe()
    process.standardOutput = stdoutPipe
    process.standardError = stderrPipe

    var buffer = Data()
    stdoutPipe.fileHandleForReading.readabilityHandler = { handle in
      let data = handle.availableData
      guard !data.isEmpty else { return }
      buffer.append(data)
      while let newlineRange = buffer.range(of: Data([0x0A])) {
        let lineData = buffer.subdata(in: buffer.startIndex..<newlineRange.lowerBound)
        buffer.removeSubrange(buffer.startIndex...newlineRange.lowerBound)
        if let line = String(data: lineData, encoding: .utf8) {
          onLine(line)
        }
      }
    }

    stderrPipe.fileHandleForReading.readabilityHandler = { handle in
      let data = handle.availableData
      guard !data.isEmpty else { return }
      if let line = String(data: data, encoding: .utf8) {
        onLine(line)
      }
    }

    try process.run()
    process.waitUntilExit()

    stdoutPipe.fileHandleForReading.readabilityHandler = nil
    stderrPipe.fileHandleForReading.readabilityHandler = nil

    // Flush remaining buffer
    if !buffer.isEmpty, let remaining = String(data: buffer, encoding: .utf8) {
      onLine(remaining)
    }

    return process.terminationStatus
  }

  func runJSON<T: Decodable>(_ args: [String], as type: T.Type) async throws -> T {
    let result = try await run(args, jsonOutput: true)
    guard result.exitCode == 0 else {
      throw CLIError.failed(result.exitCode, result.stderr)
    }
    guard let data = result.stdout.data(using: .utf8) else {
      throw CLIError.invalidOutput
    }
    return try JSONDecoder().decode(T.self, from: data)
  }

  private func findBinary() async -> String? {
    if let cached = binaryPath { return cached }

    let candidates = [
      NSHomeDirectory() + "/.frameworks/bin/frameworks",
      "/usr/local/bin/frameworks",
      "/opt/homebrew/bin/frameworks",
    ]

    for path in candidates {
      if FileManager.default.isExecutableFile(atPath: path) {
        binaryPath = path
        return path
      }
    }

    // Try PATH lookup
    let result = shell("which frameworks")
    if result.exitCode == 0 {
      let path = result.output.trimmingCharacters(in: .whitespacesAndNewlines)
      if !path.isEmpty {
        binaryPath = path
        return path
      }
    }

    return nil
  }

  private func shell(_ command: String) -> (output: String, exitCode: Int32) {
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

enum CLIError: LocalizedError {
  case notFound
  case failed(Int32, String)
  case invalidOutput

  var errorDescription: String? {
    switch self {
    case .notFound:
      return "CLI not found. Install with: brew install frameworks"
    case .failed(let code, let stderr):
      return "CLI exited with code \(code): \(stderr)"
    case .invalidOutput:
      return "Invalid CLI output"
    }
  }
}
