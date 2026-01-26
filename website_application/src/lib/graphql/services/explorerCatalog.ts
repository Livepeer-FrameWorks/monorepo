import type { Template } from "./explorer";

export type ExplorerExample = {
  id: string;
  title: string;
  description: string;
  operationType: "query" | "mutation" | "subscription";
  templatePath?: string;
  query?: string;
  variables?: Record<string, unknown>;
  tags?: string[];
  expectedPayload?: string;
};

export type ExplorerSection = {
  id: string;
  title: string;
  description: string;
  examples: ExplorerExample[];
};

export type ResolvedExplorerExample = ExplorerExample & {
  template?: Template;
};

export type ResolvedExplorerSection = {
  id: string;
  title: string;
  description: string;
  examples: ResolvedExplorerExample[];
};

export const EXPLORER_CATALOG: ExplorerSection[] = [
  {
    id: "analytics-overview",
    title: "Analytics Overview",
    description: "Top-level rollups and usage surfaces.",
    examples: [
      {
        id: "platform-overview",
        title: "Platform Overview",
        description: "High-level totals + daily stats.",
        operationType: "query",
        templatePath: "operations/queries/GetPlatformOverview.gql",
        tags: ["core", "analytics"],
      },
      {
        id: "usage-records",
        title: "Usage Records",
        description: "Billing usage line items by time range.",
        operationType: "query",
        templatePath: "operations/queries/GetUsageRecords.gql",
        tags: ["core", "analytics", "billing"],
      },
      {
        id: "viewer-hours-hourly",
        title: "Viewer Hours (Hourly)",
        description: "Hourly viewer hours across streams.",
        operationType: "query",
        templatePath: "operations/queries/GetViewerHoursHourly.gql",
        tags: ["advanced", "analytics"],
      },
      {
        id: "storage-usage",
        title: "Storage Usage Snapshots",
        description: "Hot/cold storage usage by artifact class.",
        operationType: "query",
        templatePath: "operations/queries/GetStorageUsage.gql",
        tags: ["advanced", "analytics"],
      },
      {
        id: "processing-usage",
        title: "Processing Usage",
        description: "Transcode/processing usage with summaries.",
        operationType: "query",
        templatePath: "operations/queries/GetProcessingUsage.gql",
        tags: ["advanced", "analytics"],
      },
    ],
  },
  {
    id: "analytics-health",
    title: "Health & QoE",
    description: "Stream health, QoE metrics, and rebuffering.",
    examples: [
      {
        id: "stream-health",
        title: "Stream Health",
        description: "Detailed health + client QoE for a stream.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamHealth.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "health"],
      },
      {
        id: "rebuffering-events",
        title: "Rebuffering Events",
        description: "Rebuffering transitions for a stream.",
        operationType: "query",
        templatePath: "operations/queries/GetRebufferingEventsConnection.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "health"],
      },
      {
        id: "stream-overview",
        title: "Stream Overview Analytics",
        description: "Stream metrics + hourly connections.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamOverview.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "analytics"],
      },
    ],
  },
  {
    id: "analytics-lifecycle",
    title: "Lifecycle & Events",
    description: "Stream, viewer, and artifact lifecycle events.",
    examples: [
      {
        id: "stream-events",
        title: "Stream Events",
        description: "Lifecycle events for a stream.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamEvents.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "lifecycle"],
      },
      {
        id: "connection-events",
        title: "Viewer Connection Events",
        description: "Connect/disconnect events with geo.",
        operationType: "query",
        templatePath: "operations/queries/GetConnectionEvents.gql",
        tags: ["core", "lifecycle"],
      },
      {
        id: "stream-sessions",
        title: "Viewer Sessions",
        description: "Session details by stream.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamSessions.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "lifecycle"],
      },
      {
        id: "artifact-states",
        title: "Artifact State",
        description: "Latest lifecycle state for artifacts.",
        operationType: "query",
        templatePath: "operations/queries/GetArtifactStatesConnection.gql",
        tags: ["advanced", "lifecycle"],
      },
    ],
  },
  {
    id: "analytics-infra",
    title: "Infrastructure",
    description: "Nodes, routing, and service health.",
    examples: [
      {
        id: "infra-overview",
        title: "Infrastructure Overview",
        description: "Clusters, nodes, and node metrics rollups.",
        operationType: "query",
        templatePath: "operations/queries/GetInfrastructureOverview.gql",
        tags: ["core", "infra"],
      },
      {
        id: "node-performance",
        title: "Node Performance (5m)",
        description: "Short-term node performance rollups.",
        operationType: "query",
        templatePath: "operations/queries/GetNodePerformance5m.gql",
        tags: ["advanced", "infra"],
      },
      {
        id: "routing-events",
        title: "Routing Events",
        description: "Load-balancer routing decisions.",
        operationType: "query",
        templatePath: "operations/queries/GetRoutingEvents.gql",
        tags: ["core", "infra"],
      },
      {
        id: "service-instances",
        title: "Service Instances",
        description: "Per-service availability and health.",
        operationType: "query",
        templatePath: "operations/queries/GetServiceInstancesConnection.gql",
        tags: ["advanced", "infra"],
      },
    ],
  },
  {
    id: "developer-tools",
    title: "Developer Tools",
    description: "Playback debugging and integration helpers.",
    examples: [
      {
        id: "resolve-viewer-endpoint",
        title: "Resolve Viewer Endpoint",
        description: "Resolve playback endpoints for a given content ID.",
        operationType: "query",
        templatePath: "operations/queries/ResolveViewerEndpoint.gql",
        variables: {
          contentId: "content_id",
        },
        tags: ["core", "developer"],
      },
    ],
  },
  {
    id: "live-subscriptions",
    title: "Live Subscriptions",
    description:
      "Real-time events (Signalman). Combine with historical analytics for full context.",
    examples: [
      {
        id: "live-stream-events",
        title: "Stream Events (Live)",
        description: "Stream lifecycle events as they happen.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/StreamEvents.gql",
        expectedPayload:
          "Live stream status transitions, buffer updates, and lifecycle events.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "subscription"],
      },
      {
        id: "live-viewer-metrics",
        title: "Viewer Metrics (Live)",
        description: "Per-stream viewer connect/disconnect pulses.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/ViewerMetricsStream.gql",
        expectedPayload:
          "Connect/disconnect events with protocol and node metadata.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "subscription"],
      },
      {
        id: "live-tracklist",
        title: "Track List Updates",
        description: "Live track additions/removals for a stream.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/TrackListUpdates.gql",
        expectedPayload: "Track count + track metadata for audio/video tracks.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "subscription"],
      },
      {
        id: "live-clip-lifecycle",
        title: "Clip Lifecycle",
        description: "Clip creation workflow events.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/ClipLifecycle.gql",
        expectedPayload: "Clip stage transitions with asset metadata.",
        tags: ["advanced", "subscription"],
      },
      {
        id: "live-dvr-lifecycle",
        title: "DVR Lifecycle",
        description: "DVR recording start/stop events.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/DvrLifecycle.gql",
        expectedPayload: "DVR status changes and segment counts.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "subscription"],
      },
      {
        id: "live-vod-lifecycle",
        title: "VOD Lifecycle",
        description: "VOD ingest/transcode lifecycle events.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/VodLifecycle.gql",
        expectedPayload: "VOD processing state transitions.",
        tags: ["advanced", "subscription"],
      },
      {
        id: "live-system-health",
        title: "System Health",
        description: "Cluster health pings and outages.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/SystemHealth.gql",
        expectedPayload: "Node health snapshots (cpu/mem/disk/latency).",
        tags: ["advanced", "subscription"],
      },
    ],
  },
  {
    id: "mutations",
    title: "Mutations",
    description: "Create streams, clips, DVR, and assets.",
    examples: [
      {
        id: "start-dvr",
        title: "Start DVR Recording",
        description: "Kick off DVR recording for a stream.",
        operationType: "mutation",
        templatePath: "operations/mutations/StartDVR.gql",
        tags: ["core", "mutation"],
      },
      {
        id: "create-clip",
        title: "Create Clip",
        description: "Create a clip from a stream.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateClip.gql",
        tags: ["core", "mutation"],
      },
      {
        id: "create-vod-upload",
        title: "Create VOD Upload",
        description: "Begin a VOD upload request.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateVodUpload.gql",
        tags: ["core", "mutation"],
      },
      {
        id: "complete-vod-upload",
        title: "Complete VOD Upload",
        description: "Finalize a VOD upload and trigger ingest.",
        operationType: "mutation",
        templatePath: "operations/mutations/CompleteVodUpload.gql",
        tags: ["advanced", "mutation"],
      },
    ],
  },
];
