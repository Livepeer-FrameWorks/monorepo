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
    id: "streams-playback",
    title: "Streams & Playback",
    description: "Core stream objects, playback routing, credentials, and restream targets.",
    examples: [
      {
        id: "streams-list",
        title: "List Streams",
        description: "Paginated streams with live status metrics.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamsConnection.gql",
        tags: ["core", "stream"],
      },
      {
        id: "stream-detail",
        title: "Stream Detail",
        description: "One stream with metrics and current analytics summary.",
        operationType: "query",
        templatePath: "operations/queries/GetStream.gql",
        variables: {
          id: "stream_global_id",
          streamId: "stream_global_id",
        },
        tags: ["core", "stream"],
      },
      {
        id: "resolve-viewer-endpoint",
        title: "Resolve Viewer Endpoint",
        description: "Resolve playback endpoints for a given content ID.",
        operationType: "query",
        templatePath: "operations/queries/ResolveViewerEndpoint.gql",
        variables: {
          contentId: "playback_or_asset_id",
        },
        tags: ["core", "developer", "playback"],
      },
      {
        id: "stream-keys",
        title: "Stream Keys",
        description: "List ingest credentials for a stream.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamKeys.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "stream", "auth"],
      },
      {
        id: "push-targets",
        title: "Push Targets",
        description: "Restreaming destinations configured on a stream.",
        operationType: "query",
        templatePath: "operations/queries/GetPushTargets.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "stream", "multistream"],
      },
      {
        id: "streaming-config",
        title: "Streaming Config",
        description: "Tenant ingest/playback configuration.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamingConfig.gql",
        tags: ["advanced", "stream"],
      },
    ],
  },
  {
    id: "analytics-overview",
    title: "Analytics Overview",
    description: "Top-level rollups, stream summaries, and billing usage surfaces.",
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
        id: "stream-analytics-summary",
        title: "Stream Analytics Summary",
        description: "Per-stream range summary with hourly and daily breakdowns.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamAnalyticsSummary.gql",
        variables: {
          id: "stream_global_id",
          streamId: "stream_global_id",
        },
        tags: ["core", "analytics", "stream"],
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
        id: "geographic-distribution",
        title: "Geographic Distribution",
        description: "Top countries and cities for recent viewers.",
        operationType: "query",
        templatePath: "operations/queries/GetGeographicDistribution.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "analytics", "audience"],
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
        id: "stream-health-summary",
        title: "Stream Health Summary",
        description: "Pre-aggregated health counters for dashboards.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamHealthSummary.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "health"],
      },
      {
        id: "client-qoe-summary",
        title: "Client QoE Summary",
        description: "Pre-aggregated packet loss, bandwidth, and connection timing.",
        operationType: "query",
        templatePath: "operations/queries/GetClientQoeSummary.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "health", "qoe"],
      },
      {
        id: "stream-health-core",
        title: "Stream Health Detail",
        description: "Current stream metrics plus raw health and 5-minute samples.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamHealthCore.gql",
        variables: {
          id: "stream_global_id",
          streamId: "stream_global_id",
          metricsFirst: 25,
        },
        tags: ["advanced", "health"],
      },
      {
        id: "stream-health-timeseries",
        title: "Health 5m Time Series",
        description: "5-minute bitrate, FPS, issue, and rebuffering samples.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamHealth5mTimeSeries.gql",
        variables: {
          streamId: "stream_global_id",
          first: 48,
        },
        tags: ["advanced", "health"],
      },
      {
        id: "stream-health-lifecycle",
        title: "Buffer & Track Lifecycle",
        description: "Track-list snapshots and buffer transition events.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamHealthLifecycle.gql",
        variables: {
          streamId: "stream_global_id",
          bufferFirst: 25,
        },
        tags: ["advanced", "health", "lifecycle"],
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
        id: "stream-overview-core",
        title: "Stream Overview Core",
        description: "Core stream fields, live metrics, summary, and viewer time-series.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamOverviewCore.gql",
        variables: {
          id: "stream_global_id",
          streamId: "stream_global_id",
          viewerFirst: 48,
        },
        tags: ["advanced", "analytics", "health"],
      },
      {
        id: "stream-overview-charts",
        title: "Stream Overview Charts",
        description: "Small health snapshot for overview charts.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamOverviewCharts.gql",
        variables: {
          streamId: "stream_global_id",
          healthFirst: 24,
        },
        tags: ["advanced", "analytics", "health"],
      },
    ],
  },
  {
    id: "analytics-lifecycle",
    title: "Lifecycle & Events",
    description: "Stream, viewer, and artifact lifecycle events.",
    examples: [
      {
        id: "connection-events",
        title: "Viewer Connection Events",
        description: "Connect/disconnect events with geo and session metadata.",
        operationType: "query",
        templatePath: "operations/queries/GetConnectionEvents.gql",
        variables: {
          streamId: "stream_global_id",
          first: 25,
        },
        tags: ["core", "lifecycle"],
      },
      {
        id: "viewer-sessions",
        title: "Viewer Sessions",
        description: "Paginated viewer sessions for a stream or tenant.",
        operationType: "query",
        templatePath: "operations/queries/GetViewerSessionsConnection.gql",
        variables: {
          streamId: "stream_global_id",
          first: 25,
        },
        tags: ["advanced", "lifecycle"],
      },
      {
        id: "stream-events",
        title: "Stream Lifecycle Events",
        description: "Raw historical stream lifecycle events from Periscope.",
        operationType: "query",
        templatePath: "operations/queries/GetStreamEvents.gql",
        variables: {
          streamId: "stream_global_id",
          first: 25,
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
      {
        id: "artifact-events",
        title: "Artifact Events",
        description: "Historical clip, DVR, and VOD lifecycle events.",
        operationType: "query",
        templatePath: "operations/queries/GetArtifactEventsConnection.gql",
        variables: {
          first: 25,
        },
        tags: ["advanced", "lifecycle", "media"],
      },
      {
        id: "storage-events",
        title: "Storage Events",
        description: "Freeze, defrost, and retention lifecycle events.",
        operationType: "query",
        templatePath: "operations/queries/GetStorageEventsConnection.gql",
        variables: {
          first: 25,
        },
        tags: ["advanced", "lifecycle", "storage"],
      },
    ],
  },
  {
    id: "analytics-infra",
    title: "Infrastructure & Network",
    description: "Nodes, services, routing, federation, and public network status.",
    examples: [
      {
        id: "network-status",
        title: "Public Network Status",
        description: "Topology and service status without tenant-specific analytics.",
        operationType: "query",
        templatePath: "operations/queries/GetNetworkStatus.gql",
        tags: ["core", "infra", "network"],
      },
      {
        id: "infra-overview",
        title: "Infrastructure Overview",
        description: "Clusters, nodes, and node metrics rollups.",
        operationType: "query",
        templatePath: "operations/queries/GetInfrastructureOverview.gql",
        tags: ["core", "infra"],
      },
      {
        id: "nodes",
        title: "Nodes",
        description: "Paginated nodes with cluster and status filters.",
        operationType: "query",
        templatePath: "operations/queries/GetNodesConnection.gql",
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
        id: "routing-efficiency",
        title: "Routing Efficiency",
        description: "Pre-aggregated routing success and latency metrics.",
        operationType: "query",
        templatePath: "operations/queries/GetRoutingEfficiency.gql",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "infra", "routing"],
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
      {
        id: "network-overview",
        title: "Network Overview",
        description: "Tenant-scoped network topology with traffic summaries.",
        operationType: "query",
        templatePath: "operations/queries/GetNetworkOverview.gql",
        tags: ["advanced", "network"],
      },
      {
        id: "cluster-traffic-matrix",
        title: "Cluster Traffic Matrix",
        description: "Cross-cluster routing traffic rollups.",
        operationType: "query",
        templatePath: "operations/queries/GetClusterTrafficMatrix.gql",
        tags: ["advanced", "network", "routing"],
      },
      {
        id: "federation-summary",
        title: "Federation Summary",
        description: "Origin pull, peer connection, and federation failure rollups.",
        operationType: "query",
        templatePath: "operations/queries/GetFederationSummary.gql",
        tags: ["advanced", "network", "federation"],
      },
      {
        id: "federation-events",
        title: "Federation Events",
        description: "Historical federation event stream.",
        operationType: "query",
        templatePath: "operations/queries/GetFederationEvents.gql",
        tags: ["advanced", "network", "federation"],
      },
    ],
  },
  {
    id: "media-storage",
    title: "Media & Storage",
    description: "VOD, clips, DVR, retained artifacts, and storage policy.",
    examples: [
      {
        id: "vod-assets",
        title: "VOD Assets",
        description: "Paginated uploaded VOD assets.",
        operationType: "query",
        templatePath: "operations/queries/GetVodAssetsConnection.gql",
        tags: ["core", "media"],
      },
      {
        id: "clips",
        title: "Clips",
        description: "Paginated clips with metadata and lifecycle state.",
        operationType: "query",
        templatePath: "operations/queries/GetClipsConnection.gql",
        tags: ["core", "media"],
      },
      {
        id: "dvr-recordings",
        title: "DVR Recordings",
        description: "Paginated DVR recordings and recording metadata.",
        operationType: "query",
        templatePath: "operations/queries/GetDVRRequests.gql",
        tags: ["core", "media"],
      },
      {
        id: "storage-artifacts",
        title: "Storage Artifacts",
        description: "Artifact storage inventory with retention filters.",
        operationType: "query",
        templatePath: "operations/queries/GetStorageArtifactsConnection.gql",
        tags: ["advanced", "media", "storage"],
      },
      {
        id: "media-retention-policy",
        title: "Media Retention Policy",
        description: "Tenant retention defaults and effective storage horizons.",
        operationType: "query",
        templatePath: "operations/queries/MediaRetentionPolicy.gql",
        tags: ["advanced", "media", "storage"],
      },
    ],
  },
  {
    id: "billing-usage",
    title: "Billing & Usage",
    description: "Plan status, invoices, prepaid balance, and aggregate usage.",
    examples: [
      {
        id: "billing-status",
        title: "Billing Status",
        description: "Current tier, subscription state, and usage summary.",
        operationType: "query",
        templatePath: "operations/queries/GetBillingStatus.gql",
        tags: ["core", "billing"],
      },
      {
        id: "billing-tiers",
        title: "Billing Tiers",
        description: "Available billing tiers and entitlements.",
        operationType: "query",
        templatePath: "operations/queries/GetBillingTiers.gql",
        tags: ["advanced", "billing"],
      },
      {
        id: "invoices",
        title: "Invoices",
        description: "Paginated invoices with payment status.",
        operationType: "query",
        templatePath: "operations/queries/GetInvoices.gql",
        tags: ["advanced", "billing"],
      },
      {
        id: "prepaid-balance",
        title: "Prepaid Balance",
        description: "Current prepaid balance by currency.",
        operationType: "query",
        templatePath: "operations/queries/GetPrepaidBalance.gql",
        tags: ["advanced", "billing"],
      },
      {
        id: "balance-transactions",
        title: "Balance Transactions",
        description: "Prepaid balance ledger entries.",
        operationType: "query",
        templatePath: "operations/queries/GetBalanceTransactions.gql",
        tags: ["advanced", "billing"],
      },
      {
        id: "usage-aggregates",
        title: "Usage Aggregates",
        description: "Aggregated billing usage by time range and granularity.",
        operationType: "query",
        templatePath: "operations/queries/GetUsageAggregates.gql",
        tags: ["advanced", "billing", "usage"],
      },
    ],
  },
  {
    id: "developer-tools",
    title: "Developer Tools",
    description: "API tokens, signing keys, playback auth, and integration helpers.",
    examples: [
      {
        id: "api-tokens",
        title: "API Tokens",
        description: "List developer API tokens and status.",
        operationType: "query",
        templatePath: "operations/queries/GetAPITokensConnection.gql",
        tags: ["core", "developer", "auth"],
      },
      {
        id: "signing-keys",
        title: "Signing Keys",
        description: "List playback signing keys for JWT access.",
        operationType: "query",
        templatePath: "operations/queries/GetSigningKeysConnection.gql",
        tags: ["core", "developer", "auth"],
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
        expectedPayload: "Live stream status transitions, buffer updates, and lifecycle events.",
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
        expectedPayload: "Connect/disconnect events with protocol and node metadata.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["core", "subscription"],
      },
      {
        id: "live-connection-events",
        title: "Connection Events (Live)",
        description: "Viewer connect/disconnect events as they happen.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/ConnectionEventsLive.gql",
        expectedPayload: "Connection events with geo, node, and session metadata.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "subscription"],
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
        id: "live-processing-events",
        title: "Processing Events",
        description: "Live transcode and processing usage events.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/ProcessingEventsLive.gql",
        expectedPayload: "Processing job events and usage records.",
        variables: {
          streamId: "stream_global_id",
        },
        tags: ["advanced", "subscription"],
      },
      {
        id: "live-storage-events",
        title: "Storage Events",
        description: "Live storage lifecycle events.",
        operationType: "subscription",
        templatePath: "operations/subscriptions/StorageEventsLive.gql",
        expectedPayload: "Storage freeze, defrost, upload, and retention events.",
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
        id: "create-stream",
        title: "Create Push Stream",
        description: "Create a standard encoder-pushed live stream.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateStream.gql",
        variables: {
          input: {
            name: "Main Stage",
            record: true,
            ingestMode: "PUSH",
          },
        },
        tags: ["core", "mutation", "stream"],
      },
      {
        id: "create-pull-stream",
        title: "Create Pull Stream",
        description: "Create a live stream that pulls from an upstream source.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateStream.gql",
        variables: {
          input: {
            name: "Venue Camera",
            ingestMode: "PULL",
            pullSource: {
              sourceUri: "rtsp://camera.example.net/live",
              enabled: true,
            },
          },
        },
        tags: ["advanced", "mutation", "stream"],
      },
      {
        id: "create-api-token",
        title: "Create API Token",
        description: "Create a scoped developer API token.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateAPIToken.gql",
        variables: {
          input: {
            name: "ci-token",
            permissions: "read",
            expiresIn: null,
          },
        },
        tags: ["core", "mutation", "developer"],
      },
      {
        id: "create-edge-cluster",
        title: "Create Edge Cluster",
        description: "Create a self-hosted edge cluster and enrollment token.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateEdgeCluster.gql",
        variables: {
          input: {
            clusterName: "Amsterdam Edge",
            shortDescription: "Self-hosted edge cluster",
          },
        },
        tags: ["advanced", "mutation", "infra"],
      },
      {
        id: "create-enrollment-token",
        title: "Create Enrollment Token",
        description: "Issue a token for adding another edge to a cluster.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateEnrollmentToken.gql",
        variables: {
          clusterId: "cluster_id",
          name: "Amsterdam edge 02",
          ttl: "30d",
        },
        tags: ["advanced", "mutation", "infra"],
      },
      {
        id: "create-stream-key",
        title: "Create Stream Key",
        description: "Issue a new ingest key for a stream.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateStreamKey.gql",
        variables: {
          streamId: "stream_global_id",
          input: {
            name: "primary-key",
          },
        },
        tags: ["advanced", "mutation", "stream"],
      },
      {
        id: "create-push-target",
        title: "Create Push Target",
        description: "Add a multistream RTMP destination.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreatePushTarget.gql",
        variables: {
          streamId: "stream_global_id",
          input: {
            platform: "custom",
            name: "Backup CDN",
            targetUri: "rtmp://rtmp.example.net/live/stream-key",
          },
        },
        tags: ["advanced", "mutation", "multistream"],
      },
      {
        id: "create-signing-key",
        title: "Create Signing Key",
        description: "Create an ES256 key for JWT playback policies.",
        operationType: "mutation",
        templatePath: "operations/mutations/CreateSigningKey.gql",
        variables: {
          input: {
            name: "production-player",
          },
        },
        tags: ["core", "mutation", "auth"],
      },
      {
        id: "test-playback-access",
        title: "Test Playback Access",
        description: "Dry-run playback policy evaluation without starting playback.",
        operationType: "mutation",
        templatePath: "operations/mutations/TestPlaybackAccess.gql",
        variables: {
          input: {
            playbackId: "playback_id",
            viewerToken: "",
            viewerIp: "203.0.113.10",
            requestUrl: "https://viewer.example.net/watch",
            connector: "hls",
            sessionId: "session_id",
            fireWebhook: false,
          },
        },
        tags: ["advanced", "mutation", "auth"],
      },
      {
        id: "set-playback-policy",
        title: "Set Playback Policy",
        description: "Require a signed viewer JWT for playback.",
        operationType: "mutation",
        templatePath: "operations/mutations/SetPlaybackPolicy.gql",
        variables: {
          input: {
            streamId: "stream_global_id",
            policy: {
              type: "JWT",
              jwt: {
                allowedKids: ["kid_example"],
                requiredAudience: ["web-player"],
                requiredClaimsJson: [],
              },
            },
          },
        },
        tags: ["core", "mutation", "auth"],
      },
      {
        id: "set-media-retention-policy",
        title: "Set Media Retention Policy",
        description: "Set tenant retention defaults for VOD, DVR, or clips.",
        operationType: "mutation",
        templatePath: "operations/mutations/SetMediaRetentionPolicy.gql",
        variables: {
          input: {
            targetType: "DVR",
            days: 30,
            clear: false,
          },
        },
        tags: ["advanced", "mutation", "media"],
      },
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
