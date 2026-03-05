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
