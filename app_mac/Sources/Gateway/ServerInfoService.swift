import Foundation

/// The release and shipped product features a Bridge reports through the
/// public `serverInfo` query.
struct ServerInfo: Decodable, Equatable {
  let version: String
  let features: [String]
}

struct ServerInfoQueryResponse: Decodable {
  let serverInfo: ServerInfo
}

/// A platform release number, `vMAJOR.MINOR.PATCH`.
struct ReleaseVersion: Comparable, CustomStringConvertible {
  let major: Int
  let minor: Int
  let patch: Int

  init(major: Int, minor: Int, patch: Int) {
    self.major = major
    self.minor = minor
    self.patch = patch
  }

  /// Parses a tagged release with or without a leading "v", including a
  /// pre-release suffix such as "-rc1". Returns nil for "dev" and for
  /// git-describe builds such as "v0.3.4-7-gabc123", whose code is newer than
  /// the tag they name, so their number says nothing about what they serve.
  init?(_ raw: String) {
    var text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    if text.hasPrefix("v") { text.removeFirst() }
    let core: Substring
    if let dash = text.firstIndex(of: "-") {
      let suffix = text[text.index(after: dash)...]
      if ReleaseVersion.isGitDescribeSuffix(suffix) { return nil }
      core = text[..<dash]
    } else {
      core = Substring(text)
    }
    let parts = core.split(separator: ".", omittingEmptySubsequences: false)
    guard parts.count == 3,
      let major = Int(parts[0]), let minor = Int(parts[1]), let patch = Int(parts[2]),
      major >= 0, minor >= 0, patch >= 0
    else { return nil }
    self.init(major: major, minor: minor, patch: patch)
  }

  static func < (lhs: ReleaseVersion, rhs: ReleaseVersion) -> Bool {
    (lhs.major, lhs.minor, lhs.patch) < (rhs.major, rhs.minor, rhs.patch)
  }

  var description: String { "v\(major).\(minor).\(patch)" }

  // git describe appends "-<commits>-g<hash>" (and optionally "-dirty").
  private static func isGitDescribeSuffix(_ suffix: Substring) -> Bool {
    let fields = suffix.split(separator: "-")
    guard fields.count >= 2, Int(fields[0]) != nil else { return false }
    return fields[1].hasPrefix("g") && fields[1].dropFirst().allSatisfy(\.isHexDigit)
  }
}

/// Whether the connected Bridge serves what this app needs.
enum ServerCompatibility: Equatable {
  /// Not checked yet, or the Bridge could not be asked.
  case unknown
  case supported(ServerInfo)
  /// The Bridge predates serverInfo (reported is nil) or reports an older
  /// release.
  case tooOld(reported: String?)

  /// The message shown while the Bridge is too old; nil otherwise.
  var warning: String? {
    guard case .tooOld(let reported) = self else { return nil }
    let minimum = ServerInfoService.minimumVersion
    if let reported {
      return "Server too old (\(reported)). This app needs \(minimum) or later."
    }
    return "Server too old. This app needs \(minimum) or later."
  }
}

/// Asks the connected Bridge for its release when the app connects and decides
/// whether it is new enough. The minimum is the first release that serves
/// serverInfo, so a Bridge that rejects the query as an unknown field is too old.
final class ServerInfoService {
  static let minimumVersion = ReleaseVersion(major: 0, minor: 3, patch: 11)
  static let shared = ServerInfoService()

  typealias Transport = (_ query: String) async throws -> ServerInfoQueryResponse

  private let transport: Transport

  init(
    transport: @escaping Transport = { query in
      try await GatewayClient.shared.graphql(
        query: query, responseType: ServerInfoQueryResponse.self)
    }
  ) {
    self.transport = transport
  }

  func check() async -> ServerCompatibility {
    do {
      let response = try await transport(GQL.GetServerInfo)
      return Self.compatibility(for: response.serverInfo)
    } catch let error as GatewayError {
      return Self.compatibility(forFailure: error)
    } catch {
      return .unknown
    }
  }

  /// Runs the check and publishes the answer on the app state.
  func refresh(appState: AppState) async {
    let result = await check()
    await MainActor.run { appState.serverCompatibility = result }
  }

  static func compatibility(for info: ServerInfo) -> ServerCompatibility {
    if let reported = ReleaseVersion(info.version), reported < minimumVersion {
      return .tooOld(reported: info.version)
    }
    return .supported(info)
  }

  /// A Bridge older than serverInfo rejects the query with "Cannot query field
  /// serverInfo", either in a 200 body or as HTTP 422 with the same body. Any
  /// other failure leaves the answer unknown rather than blaming the server.
  static func compatibility(forFailure error: GatewayError) -> ServerCompatibility {
    let messages: [String]
    switch error {
    case .graphqlErrors(let reported):
      messages = reported
    case .httpError(_, let data):
      guard
        let decoded = try? JSONDecoder().decode(
          GraphQLResponse<ServerInfoQueryResponse>.self, from: data),
        let errors = decoded.errors
      else { return .unknown }
      messages = errors.map(\.message)
    default:
      return .unknown
    }
    return messages.contains(where: isUnknownServerInfoField) ? .tooOld(reported: nil) : .unknown
  }

  private static func isUnknownServerInfoField(_ message: String) -> Bool {
    message.lowercased().contains("cannot query field") && message.contains("serverInfo")
  }
}
