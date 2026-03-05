import Foundation

// MARK: - Edge API Responses

struct EdgeStatusResponse: Codable {
  let nodeId: String
  let operationalMode: String
  let tenantId: String
  let uptimeSeconds: Int
  let version: String
  let gitCommit: String

  enum CodingKeys: String, CodingKey {
    case nodeId = "node_id"
    case operationalMode = "operational_mode"
    case tenantId = "tenant_id"
    case uptimeSeconds = "uptime_seconds"
    case version, gitCommit = "git_commit"
  }
}

struct EdgeHealthResponse: Codable {
  let healthy: Bool
  let lastSeen: String?
  let nodeId: String
  let mistReachable: Bool

  enum CodingKeys: String, CodingKey {
    case healthy
    case lastSeen = "last_seen"
    case nodeId = "node_id"
    case mistReachable = "mist_reachable"
  }
}

struct EdgeStreamsResponse: Codable {
  let nodeId: String
  let count: Int
  let streams: [EdgeStream]

  enum CodingKeys: String, CodingKey {
    case nodeId = "node_id"
    case count, streams
  }
}

struct EdgeStream: Codable, Identifiable {
  var id: String { name }
  let name: String
  let viewers: Int
  let clients: Int
  let upBytes: UInt64
  let downBytes: UInt64

  enum CodingKeys: String, CodingKey {
    case name, viewers, clients
    case upBytes = "up_bytes"
    case downBytes = "down_bytes"
  }
}

struct EdgeMetricsResponse: Codable {
  let nodeId: String
  let bandwidthUp: UInt64
  let bandwidthDown: UInt64
  let cpuPercent: Double
  let memBytes: UInt64
  let totalViewers: Int

  enum CodingKeys: String, CodingKey {
    case nodeId = "node_id"
    case bandwidthUp = "bandwidth_up"
    case bandwidthDown = "bandwidth_down"
    case cpuPercent = "cpu_percent"
    case memBytes = "mem_bytes"
    case totalViewers = "total_viewers"
  }
}

struct EdgeStreamDetailResponse: Codable {
  // MistServer stream info — flexible shape
  let meta: [String: AnyCodable]?

  init(from decoder: Decoder) throws {
    let container = try decoder.singleValueContainer()
    if let dict = try? container.decode([String: AnyCodable].self) {
      meta = dict
    } else {
      meta = nil
    }
  }

  func encode(to encoder: Encoder) throws {
    var container = encoder.singleValueContainer()
    try container.encode(meta)
  }
}

struct EdgeClientsResponse: Codable {
  let nodeId: String
  let clients: [EdgeClientInfo]

  enum CodingKeys: String, CodingKey {
    case nodeId = "node_id"
    case clients
  }
}

struct EdgeClientInfo: Codable {
  let stream: String?
  let ip: String?
  let protocol_: String?

  enum CodingKeys: String, CodingKey {
    case stream, ip
    case protocol_ = "protocol"
  }
}

// Simple wrapper for arbitrary JSON values
struct AnyCodable: Codable {
  let value: Any

  init(_ value: Any) { self.value = value }

  init(from decoder: Decoder) throws {
    let container = try decoder.singleValueContainer()
    if let v = try? container.decode(String.self) { value = v }
    else if let v = try? container.decode(Int.self) { value = v }
    else if let v = try? container.decode(Double.self) { value = v }
    else if let v = try? container.decode(Bool.self) { value = v }
    else if let v = try? container.decode([AnyCodable].self) { value = v.map(\.value) }
    else if let v = try? container.decode([String: AnyCodable].self) {
      value = v.mapValues(\.value)
    } else { value = NSNull() }
  }

  func encode(to encoder: Encoder) throws {
    var container = encoder.singleValueContainer()
    switch value {
    case let v as String: try container.encode(v)
    case let v as Int: try container.encode(v)
    case let v as Double: try container.encode(v)
    case let v as Bool: try container.encode(v)
    default: try container.encodeNil()
    }
  }
}

// MARK: - GraphQL Responses

struct StreamsQueryResponse: Codable {
  let streamsConnection: StreamsConnection
}

struct StreamsConnection: Codable {
  let edges: [StreamEdge]
  let totalCount: Int?
}

struct StreamEdge: Codable {
  let node: StreamNode
}

struct StreamNode: Codable {
  let id: String
  let name: String
  let isActive: Bool
  let viewerCount: Int?

  enum CodingKeys: String, CodingKey {
    case id, name
    case isActive = "is_active"
    case viewerCount = "viewer_count"
  }
}
