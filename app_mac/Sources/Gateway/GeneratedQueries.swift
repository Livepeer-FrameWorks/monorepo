// AUTO-GENERATED from pkg/graphql/operations/ — do not edit
// Re-generate: ./scripts/generate-swift-gql.sh
// swiftlint:disable all

enum GQL {

  // MARK: - fragments

  static let AllocationFields = """
  fragment AllocationFields on AllocationDetails {
    limit
    unitPrice
    unit
  }
  """

  static let AuthErrorFields = """
  fragment AuthErrorFields on AuthError {
    __typename
    message
    code
  }
  """

  static let BillingTierFields = """
  fragment BillingTierFields on BillingTier {
    id
    tierName
    displayName
    description
    basePrice
    currency
    billingPeriod
    supportLevel
    slaLevel
    meteringEnabled
    isEnterprise
    features {
      recording
      analytics
      customBranding
      apiAccess
      supportLevel
      sla
    }
    bandwidthAllocation {
      ...AllocationFields
    }
    storageAllocation {
      ...AllocationFields
    }
    computeAllocation {
      ...AllocationFields
    }
    overageRates {
      bandwidth {
        ...AllocationFields
      }
      storage {
        ...AllocationFields
      }
      compute {
        ...AllocationFields
      }
    }
  }
  """

  static let BootstrapTokenFields = """
  # All fields available on BootstrapToken type
  fragment BootstrapTokenFields on BootstrapToken {
    id
    name
    token
    kind
    clusterId
    expectedIp
    metadata
    usageLimit
    usageCount
    expiresAt
    usedAt
    createdBy
    createdAt
  }
  """

  static let ClipFields = """
  # All fields available on the Clip type
  fragment ClipFields on Clip {
    id
    clipHash
    playbackId
    streamId
    stream {
      streamId
    }
    title
    description
    startTime
    duration
    nodeId
    storagePath
    sizeBytes
    status
    createdAt
    updatedAt
    clipMode
    requestedParams
    storageLocation
    isFrozen
    expiresAt
  }
  """

  static let ClusterFields = """
  # All fields available on Cluster type (excludes nodesConnection edge)
  fragment ClusterFields on Cluster {
    id
    clusterId
    clusterName
    clusterType
    deploymentModel
    baseUrl
    databaseUrl
    periscopeUrl
    kafkaBrokers
    maxConcurrentStreams
    maxConcurrentViewers
    maxBandwidthMbps
    healthStatus
    isActive
    isDefaultCluster
    isSubscribed
    createdAt
    updatedAt
    ownerTenantId
    visibility
    pricingModel
    monthlyPriceCents
    requiresApproval
    shortDescription
  }
  """

  static let DeleteSuccessFields = """
  fragment DeleteSuccessFields on DeleteSuccess {
    __typename
    success
    deletedId
  }
  """

  static let DVRRequestFields = """
  # All fields available on DVRRequest type
  fragment DVRRequestFields on DVRRequest {
    id
    dvrHash
    playbackId
    streamId
    stream {
      streamId
    }
    title
    createdAt
    updatedAt
    expiresAt
    storageNodeId
    status
    startedAt
    endedAt
    durationSeconds
    sizeBytes
    manifestPath
    errorMessage
    storageLocation
    isFrozen
  }
  """

  static let LiveUsageSummaryFields = """
  fragment LiveUsageSummaryFields on LiveUsageSummary {
    periodStart
    periodEnd
    # Core metrics
    streamHours
    egressGb
    peakBandwidthMbps
    averageStorageGb
    # Per-codec breakdown: Livepeer
    livepeerH264Seconds
    livepeerVp9Seconds
    livepeerAv1Seconds
    livepeerHevcSeconds
    # Per-codec breakdown: Native AV
    nativeAvH264Seconds
    nativeAvVp9Seconds
    nativeAvAv1Seconds
    nativeAvHevcSeconds
    nativeAvAacSeconds
    nativeAvOpusSeconds
    # Viewer metrics
    totalStreams
    totalViewers
    viewerHours
    maxViewers
    uniqueUsers
    # Segment/stream counts
    livepeerSegmentCount
    livepeerUniqueStreams
    nativeAvSegmentCount
    nativeAvUniqueStreams
    # Geo enrichment
    uniqueCountries
    uniqueCities
    geoBreakdown {
      countryCode
      viewerCount
      viewerHours
      egressGb
    }
    # Storage lifecycle - artifact counts
    clipsCreated
    clipsDeleted
    dvrCreated
    dvrDeleted
    vodCreated
    vodDeleted
    # Storage - hot (bytes)
    clipBytes
    dvrBytes
    vodBytes
    # Storage - cold/frozen (bytes)
    frozenClipBytes
    frozenDvrBytes
    frozenVodBytes
    # Freeze/defrost operations
    freezeCount
    freezeBytes
    defrostCount
    defrostBytes
  }
  """

  static let NodeCoreFields = """
  fragment NodeCoreFields on InfrastructureNode {
    id
    nodeId
    nodeName
    clusterId
    nodeType
    region
    externalIp
    internalIp
    wireguardIp
    wireguardPublicKey
    availabilityZone
    cpuCores
    memoryGb
    diskGb
    lastHeartbeat
    createdAt
    updatedAt
    tags
    metadata
    # Real-time state from live_nodes (ClickHouse ReplacingMergeTree)
    liveState {
      nodeId
      cpuPercent
      ramUsedBytes
      ramTotalBytes
      diskUsedBytes
      diskTotalBytes
      upSpeed
      downSpeed
      activeStreams
      isHealthy
      location
      updatedAt
    }
  }
  """

  static let NodeListFields = """
  fragment NodeListFields on InfrastructureNode {
    id
    nodeId
    nodeName
    clusterId
    nodeType
    region
    externalIp
    internalIp
    latitude
    longitude
    cpuCores
    memoryGb
    diskGb
    lastHeartbeat
    tags
    liveState {
      cpuPercent
      ramUsedBytes
      ramTotalBytes
      isHealthy
      activeStreams
      updatedAt
      diskUsedBytes
      diskTotalBytes
      location
    }
  }
  """

  static let NotFoundErrorFields = """
  fragment NotFoundErrorFields on NotFoundError {
    __typename
    message
    code
    resourceType
    resourceId
  }
  """

  static let PageInfoFields = """
  fragment PageInfoFields on PageInfo {
    hasNextPage
    hasPreviousPage
    startCursor
    endCursor
  }
  """

  static let PlaybackMetadataFields = """
  # All fields available on PlaybackMetadata type
  fragment PlaybackMetadataFields on PlaybackMetadata {
    status
    isLive
    viewers
    bufferState
    tracks {
      type
      codec
      bitrateKbps
      width
      height
      channels
      sampleRate
    }
    protocolHints
    instances {
      nodeId
      viewers
      bufferState
      bytesUp
      bytesDown
      totalConnections
      inputs
      lastUpdate
    }
    dvrStatus
    dvrSourceUri
    contentId
    contentType
    title
    description
    durationSeconds
    recordingSizeBytes
    clipSource
    createdAt
    thumbnailSpriteVttUrl
  }
  """

  static let PushTargetFields = """
  fragment PushTargetFields on PushTarget {
    id
    streamId
    platform
    name
    targetUri
    isEnabled
    status
    lastError
    lastPushedAt
    createdAt
  }
  """

  static let RateLimitErrorFields = """
  fragment RateLimitErrorFields on RateLimitError {
    __typename
    message
    code
    retryAfter
  }
  """

  static let StreamCoreFields = """
  fragment StreamCoreFields on Stream {
    id
    streamId
    name
    description
    streamKey
    playbackId
    record
    createdAt
    updatedAt
  }
  """

  static let StreamHealthFields = """
  # All fields available on StreamHealthMetric type
  fragment StreamHealthFields on StreamHealthMetric {
    timestamp
    streamId
    nodeId
    issuesDescription
    hasIssues
    bitrate
    fps
    width
    height
    codec
    qualityTier
    gopSize
    bufferState
    bufferHealth
    bufferSize
  }
  """

  static let StreamMetricsFields = """
  fragment StreamMetricsFields on StreamMetrics {
    status
    isLive
    currentViewers
    startedAt
    updatedAt
    nodeId
    trackCount
    totalInputs
    uploadedBytes
    downloadedBytes
    viewerSeconds
    packetsSent
    packetsLost
    packetsRetransmitted
    bufferState
    qualityTier
    primaryWidth
    primaryHeight
    primaryFps
    primaryCodec
    primaryBitrate
    hasIssues
    issuesDescription
  }
  """

  static let StreamMetricsListFields = """
  fragment StreamMetricsListFields on StreamMetrics {
    status
    isLive
    currentViewers
    bufferState
    qualityTier
    startedAt
  }
  """

  static let UsageSummaryFields = """
  fragment UsageSummaryFields on UsageSummary {
    clusterId
    period
    periodStart
    periodEnd
    timestamp
    granularity
    # Flow metrics
    streamHours
    egressGb
    peakBandwidthMbps
    # Storage
    averageStorageGb
    # Per-codec breakdown: Livepeer (external gateway)
    livepeerH264Seconds
    livepeerVp9Seconds
    livepeerAv1Seconds
    livepeerHevcSeconds
    # Per-codec breakdown: Native AV (local processing)
    nativeAvH264Seconds
    nativeAvVp9Seconds
    nativeAvAv1Seconds
    nativeAvHevcSeconds
    nativeAvAacSeconds
    nativeAvOpusSeconds
    # Viewer metrics
    totalStreams
    totalViewers
    viewerHours
    maxViewers
    uniqueUsers
    # Segment/stream counts
    livepeerSegmentCount
    livepeerUniqueStreams
    nativeAvSegmentCount
    nativeAvUniqueStreams
    # Geo enrichment
    uniqueCountries
    uniqueCities
    geoBreakdown {
      countryCode
      viewerCount
      viewerHours
      egressGb
    }
    # Storage lifecycle - artifact counts
    clipsCreated
    clipsDeleted
    dvrCreated
    dvrDeleted
    vodCreated
    vodDeleted
    # Storage - hot (bytes)
    clipBytes
    dvrBytes
    vodBytes
    # Storage - cold/frozen (bytes)
    frozenClipBytes
    frozenDvrBytes
    frozenVodBytes
    # Freeze/defrost operations
    freezeCount
    freezeBytes
    defrostCount
    defrostBytes
  }
  """

  static let ValidationErrorFields = """
  fragment ValidationErrorFields on ValidationError {
    __typename
    message
    code
    field
    constraint
  }
  """

  static let ViewerEndpointFields = """
  # All fields available on ViewerEndpoint type
  fragment ViewerEndpointFields on ViewerEndpoint {
    nodeId
    baseUrl
    protocol
    url
    geoDistance
    loadScore
    outputs
  }
  """

  // MARK: - queries

  static let GetAPITokensConnection = """
  # Fetch paginated list of developer API tokens with permissions and usage timestamps
  query GetAPITokensConnection($first: Int = 50, $after: String) {
    developerTokensConnection(page: { first: $first, after: $after })  {
      edges {
        cursor
        node {
          id
          tokenName
          permissions
          status
          lastUsedAt
          expiresAt
          createdAt
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetAPIUsageConnection = """
  query GetAPIUsageConnection(
    $timeRange: TimeRangeInput
    $authType: String
    $operationType: String
    $first: Int = 50
  ) {
    analytics {
      usage {
        api {
          apiUsageConnection(
            page: { first: $first }
            authType: $authType
            operationType: $operationType
            timeRange: $timeRange
          ) {
            edges {
              cursor
              node {
                id
                timestamp
                authType
                operationType
                operationName
                requestCount
                errorCount
                totalDurationMs
                totalComplexity
                uniqueUsers
                uniqueTokens
              }
            }
            pageInfo {
              hasNextPage
              hasPreviousPage
              startCursor
              endCursor
            }
            totalCount
            summaries {
              date
              authType
              totalRequests
              totalErrors
              avgDurationMs
              totalComplexity
              uniqueUsers
              uniqueTokens
            }
            operationSummaries {
              operationType
              totalRequests
              totalErrors
              uniqueOperations
              avgDurationMs
              totalComplexity
            }
          }
        }
      }
    }
  }
  """

  static let GetArtifactEventsConnection = """
  # Fetch artifact lifecycle events (clip/dvr/vod)
  query GetArtifactEventsConnection(
    $streamId: ID
    $contentType: String
    $stage: String
    $timeRange: TimeRangeInput
    $first: Int = 50
    $after: String
  ) {
    analytics {
      lifecycle {
        artifactEventsConnection(
          streamId: $streamId
          contentType: $contentType
          stage: $stage
          timeRange: $timeRange
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              id
              timestamp
              contentType
              playbackId
              stage
              percent
              message
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetArtifactStatesConnection = """
  # Fetch paginated artifact lifecycle states for DVR/clip processing
  # Returns real-time status, progress, and storage paths for ongoing artifact operations
  query GetArtifactStatesConnection(
    $streamId: ID
    $contentType: String
    $stage: String
    $first: Int = 100
    $after: String
  ) {
    analytics {
      lifecycle {
        artifactStatesConnection(
          streamId: $streamId
          contentType: $contentType
          stage: $stage
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              streamId
              contentType
              playbackId
              stage
              progressPercent
              errorMessage
              requestedAt
              startedAt
              completedAt
              clipStartUnix
              clipStopUnix
              segmentCount
              manifestPath
              filePath
              s3Url
              sizeBytes
              processingNodeId
            }
          }
          pageInfo {
            hasNextPage
            hasPreviousPage
            startCursor
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetBalanceTransactions = """
  query GetBalanceTransactions($page: ConnectionInput, $transactionType: String) {
    balanceTransactionsConnection(page: $page, transactionType: $transactionType) {
      nodes {
        id
        amountCents
        balanceAfterCents
        transactionType
        description
        createdAt
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetBillingDetails = """
  query GetBillingDetails {
    billingDetails {
      email
      company
      vatNumber
      address {
        street
        city
        state
        postalCode
        country
      }
      isComplete
      updatedAt
    }
  }
  """

  static let GetBillingStatus = """
  # Fetch complete billing status including current tier, subscription, and usage summary
  query GetBillingStatus {
    billingStatus {
      billingStatus
      currency
      nextBillingDate
      trialEndsAt
      outstandingAmount
      currentTier {
        ...BillingTierFields
      }
      subscription {
        id
        tenantId
        tierId
        status
        billingEmail
        billingModel
        startedAt
        trialEndsAt
        nextBillingDate
        cancelledAt
        customPricing {
          basePrice
          discountRate
          overageRates {
            bandwidth {
              ...AllocationFields
            }
            storage {
              ...AllocationFields
            }
            compute {
              ...AllocationFields
            }
          }
        }
        customFeatures {
          recording
          analytics
          customBranding
          apiAccess
          supportLevel
          sla
        }
        customAllocations {
          ...AllocationFields
        }
        paymentMethod
        createdAt
        updatedAt
      }
      liveUsage {
        ...LiveUsageSummaryFields
      }
      invoicePreview {
        id
        status
        amount
        baseAmount
        meteredAmount
        currency
        dueDate
        periodStart
        periodEnd
        usageSummary {
          ...UsageSummaryFields
        }
        lineItems {
          description
          quantity
          unitPrice
          total
        }
      }
    }
  }
  """

  static let GetBillingTiers = """
  # Fetch all available billing tiers with pricing, features, and resource allocations
  query GetBillingTiers {
    billingTiers {
      id
      tierName
      displayName
      description
      basePrice
      currency
      billingPeriod
      features {
        recording
        analytics
        customBranding
        apiAccess
        supportLevel
        sla
      }
      bandwidthAllocation {
        limit
        unitPrice
        unit
      }
      storageAllocation {
        limit
        unitPrice
        unit
      }
      computeAllocation {
        limit
        unitPrice
        unit
      }
      overageRates {
        bandwidth {
          limit
          unitPrice
          unit
        }
        storage {
          limit
          unitPrice
          unit
        }
        compute {
          limit
          unitPrice
          unit
        }
      }
      supportLevel
      slaLevel
      meteringEnabled
      isEnterprise
    }
  }
  """

  static let GetClientQoeSummary = """
  # Pre-aggregated client QoE summary for dashboard views
  query GetClientQoeSummary($streamId: ID, $timeRange: TimeRangeInput) {
    analytics {
      health {
        clientQoeSummary(streamId: $streamId, timeRange: $timeRange) {
          avgPacketLossRate
          peakPacketLossRate
          avgBandwidthIn
          avgBandwidthOut
          avgConnectionTime
          totalActiveSessions
        }
      }
    }
  }
  """

  static let GetClipsConnection = """
  # Fetch paginated list of clips with metadata and lifecycle status
  # Returns clip metadata from Commodore (use artifactStatesConnection for live status)
  query GetClipsConnection($streamId: ID, $first: Int = 50, $after: String) {
    clipsConnection(streamId: $streamId, page: { first: $first, after: $after })  {
      edges {
        cursor
        node {
          # Business metadata (from Commodore)
          id
          clipHash
          playbackId
          streamId
          stream {
            streamId
          }
          title
          description
          startTime
          duration
          clipMode
          requestedParams
          createdAt
          updatedAt
          expiresAt
          # Lifecycle data (null from Commodore, use artifactStatesConnection for live status)
          nodeId
          storagePath
          sizeBytes
          status
          storageLocation
          isFrozen
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetClusterInvites = """
  # Fetch invites for a cluster you own
  query GetClusterInvites($clusterId: ID!) {
    clusterInvites(clusterId: $clusterId) {
      id
      clusterId
      clusterName
      invitedTenantId
      invitedTenantName
      inviteToken
      accessLevel
      status
      createdAt
      expiresAt
    }
  }
  """

  static let GetClustersAccess = """
  # Fetch cluster access permissions and resource limits for the current tenant
  query GetClustersAccess {
    clustersAccess {
      clusterId
      clusterName
      accessLevel
      resourceLimits
    }
  }
  """

  static let GetClustersAvailable = """
  # Fetch list of clusters available for tenant enrollment with tier requirements
  query GetClustersAvailable {
    clustersAvailable {
      clusterId
      clusterName
      tiers
      autoEnroll
    }
  }
  """

  static let GetClusterTrafficMatrix = """
  # Cross-cluster routing traffic matrix from hourly rollups
  query GetClusterTrafficMatrix($timeRange: TimeRangeInput, $noCache: Boolean = false) {
    analytics {
      infra {
        clusterTrafficMatrix(timeRange: $timeRange, noCache: $noCache) {
          clusterId
          remoteClusterId
          eventCount
          successCount
          avgLatencyMs
          avgDistanceKm
          successRate
          maxLatencyMs
        }
      }
    }
  }
  """

  static let GetConnectionEvents = """
  # Fetch paginated viewer connection events with geographic and session details
  # Returns connect/disconnect events for analytics and monitoring
  query GetConnectionEvents(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 50
    $after: String
    $noCache: Boolean = false
  ) {
    analytics {
      lifecycle {
        connectionEventsConnection(
          streamId: $streamId
          timeRange: $timeRange
          noCache: $noCache
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              eventId
              timestamp
              streamId
              stream {
                streamId
              }
              sessionId
              connectionAddr
              connector
              nodeId
              countryCode
              city
              latitude
              longitude
              clientBucket {
                h3Index
                resolution
              }
              nodeBucket {
                h3Index
                resolution
              }
              eventType
              sessionDurationSeconds
              bytesTransferred
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetConversation = """
  query GetConversation($id: ID!) {
    conversation(id: $id) {
      id
      subject
      status
      unreadCount
      createdAt
      updatedAt
    }
  }
  """

  static let GetConversationsConnection = """
  query GetConversationsConnection($first: Int, $after: String) {
    conversationsConnection(page: { first: $first, after: $after }) {
      edges {
        cursor
        node {
          id
          subject
          status
          unreadCount
          createdAt
          updatedAt
          lastMessage {
            id
            content
            sender
            createdAt
          }
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetDVRRequests = """
  # Fetch paginated list of DVR recordings with lifecycle status and storage paths
  # Returns DVR metadata from Commodore (use artifactStatesConnection for live status)
  query GetDVRRequests($streamId: ID, $first: Int = 50, $after: String) {
    dvrRecordingsConnection(streamId: $streamId, page: { first: $first, after: $after })  {
      edges {
        cursor
        node {
          # Business metadata (from Commodore)
          id
          dvrHash
          playbackId
          streamId
          stream {
            streamId
          }
          title
          createdAt
          updatedAt
          expiresAt
          # Lifecycle data (null from Commodore, use artifactStatesConnection for live status)
          storageNodeId
          status
          startedAt
          endedAt
          durationSeconds
          sizeBytes
          manifestPath
          errorMessage
          storageLocation
          isFrozen
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetFederationEvents = """
  # Federation events: origin pulls, peer connections, leader elections
  query GetFederationEvents($timeRange: TimeRangeInput!, $first: Int = 50, $eventType: String) {
    analytics {
      infra {
        federationEventsConnection(timeRange: $timeRange, first: $first, eventType: $eventType) {
          edges {
            cursor
            node {
              timestamp
              eventType
              localCluster
              remoteCluster
              streamName
              streamId
              sourceNode
              destNode
              dtscUrl
              latencyMs
              timeToLiveMs
              failureReason
              queriedClusters
              respondingClusters
              totalCandidates
              bestRemoteScore
              peerCluster
              role
              reason
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetFederationSummary = """
  # Aggregated federation summary: event counts by type, latency, failure rate
  query GetFederationSummary($timeRange: TimeRangeInput!, $noCache: Boolean = false) {
    analytics {
      infra {
        federationSummary(timeRange: $timeRange, noCache: $noCache) {
          eventCounts {
            eventType
            count
            failureCount
            avgLatencyMs
          }
          totalEvents
          overallAvgLatencyMs
          overallFailureRate
        }
      }
    }
  }
  """

  static let GetGeographicDistribution = """
  # Fetch geographic viewer distribution with top countries and cities
  # Returns aggregated viewer counts and percentages for geographic analytics
  query GetGeographicDistribution($streamId: ID, $timeRange: TimeRangeInput, $topN: Int = 10) {
    analytics {
      usage {
        streaming {
          geographicDistribution(streamId: $streamId, timeRange: $timeRange, topN: $topN) {
            timeRange {
              start
              end
            }
            streamId
            topCountries {
              countryCode
              viewerCount
              percentage
            }
            topCities {
              city
              countryCode
              viewerCount
              percentage
              latitude
              longitude
            }
            uniqueCountries
            uniqueCities
            totalViewers
            viewersByCountry {
              timestamp
              countryCode
              viewerCount
            }
          }
        }
      }
    }
  }
  """

  static let GetInfrastructureMetrics = """
  # Fetch infrastructure node metrics (aggregated + hourly time-series)
  # Split from GetInfrastructureOverview to stay within query complexity limits
  query GetInfrastructureMetrics(
    $timeRange: TimeRangeInput
    $first: Int = 100
    $noCache: Boolean = false
  ) {
    analytics {
      infra {
        nodeMetricsAggregated(timeRange: $timeRange, noCache: $noCache) {
          nodeId
          clusterId
          avgCpu
          avgMemory
          avgDisk
          avgShm
          totalBandwidthIn
          totalBandwidthOut
          sampleCount
        }
        nodeMetrics1hConnection(timeRange: $timeRange, noCache: $noCache, page: { first: $first }) {
          edges {
            cursor
            node {
              timestamp
              nodeId
              avgCpu
              peakCpu
              avgMemory
              peakMemory
              avgDisk
              peakDisk
              avgShm
              peakShm
              totalBandwidthIn
              totalBandwidthOut
              wasHealthy
            }
          }
          pageInfo {
            ...PageInfoFields
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetInfrastructureNode = """
  # Fetch a single infrastructure node by its global relay ID
  query GetInfrastructureNode($id: ID!) {
    node(id: $id) {
      ... on InfrastructureNode {
        id
        nodeId
        nodeName
        clusterId
        nodeType
        region
        externalIp
        latitude
        longitude
        cpuCores
        memoryGb
        diskGb
        lastHeartbeat
        tags
        liveState {
          cpuPercent
          ramUsedBytes
          ramTotalBytes
          isHealthy
          activeStreams
          updatedAt
          diskUsedBytes
          diskTotalBytes
          location
        }
      }
    }
  }
  """

  static let GetInfrastructureOverview = """
  # Fetch tenant info and clusters for infrastructure dashboard
  # Nodes: use GetNodesConnection. Metrics: use GetInfrastructureMetrics.
  query GetInfrastructureOverview {
    tenant {
      id
      name
      cluster
      createdAt
    }
    clustersConnection(page: { first: 50 }) {
      edges {
        cursor
        node {
          id
          clusterId
          clusterName
          clusterType
          deploymentModel
          baseUrl
          databaseUrl
          periscopeUrl
          kafkaBrokers
          maxConcurrentStreams
          maxConcurrentViewers
          maxBandwidthMbps
          healthStatus
          isActive
          isDefaultCluster
          isSubscribed
          createdAt
          updatedAt
          ownerTenantId
          visibility
          pricingModel
          monthlyPriceCents
          requiresApproval
          shortDescription
        }
      }
      pageInfo {
        ...PageInfoFields
      }
      totalCount
    }
  }
  """

  static let GetInvoices = """
  # Fetch paginated list of invoices with line items and payment status
  query GetInvoices($first: Int = 50, $after: String) {
    invoicesConnection(page: { first: $first, after: $after })  {
      edges {
        cursor
        node {
          id
          amount
          currency
          status
          dueDate
          createdAt
          lineItems {
            description
            quantity
            unitPrice
            total
          }
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetMarketplaceClusters = """
  # Fetch available marketplace clusters with pricing, eligibility, and subscription status
  # Returns publicly available clusters users can browse and subscribe to
  query GetMarketplaceClusters($first: Int = 50, $after: String) {
    marketplaceClusters(first: $first, after: $after) {
      clusterId
      clusterName
      shortDescription
      visibility
      pricingModel
      monthlyPriceCents
      ownerName
      isSubscribed
      subscriptionStatus
      requiresApproval
      isEligible
      denialReason
      currentUtilization
      maxConcurrentStreams
      maxConcurrentViewers
    }
  }
  """

  static let GetMessagesConnection = """
  query GetMessagesConnection($conversationId: ID!, $first: Int, $after: String) {
    messagesConnection(conversationId: $conversationId, page: { first: $first, after: $after }) {
      edges {
        cursor
        node {
          id
          conversationId
          content
          sender
          createdAt
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetMyClusterInvites = """
  # Fetch cluster invitations sent to the current tenant
  # Returns pending invites with access level and expiration details
  query GetMyClusterInvites {
    myClusterInvites {
      id
      clusterId
      clusterName
      invitedTenantId
      invitedTenantName
      inviteToken
      accessLevel
      status
      expiresAt
      createdAt
    }
  }
  """

  static let GetMySubscriptions = """
  # Fetch list of cluster subscriptions for the current tenant
  query GetMySubscriptions {
    mySubscriptions {
      id
      clusterId
      clusterName
      clusterType
      healthStatus
      createdAt
      isDefaultCluster
      isSubscribed
    }
  }
  """

  static let GetNetworkOverview = """
  # Combined network topology + federation traffic for the network dashboard page
  # Requires authentication (returns tenant-scoped cluster traffic)
  query GetNetworkOverview($timeRange: TimeRangeInput!) {
    networkStatus {
      clusters {
        clusterId
        name
        region
        latitude
        longitude
        nodeCount
        healthyNodeCount
        peerCount
        status
        clusterType
        shortDescription
        maxStreams
        currentStreams
        maxViewers
        currentViewers
        maxBandwidthMbps
        currentBandwidthMbps
      }
      peerConnections {
        sourceCluster
        targetCluster
        connected
      }
      nodes {
        nodeId
        name
        nodeType
        latitude
        longitude
        status
        clusterId
      }
      serviceInstances {
        instanceId
        serviceId
        clusterId
        nodeId
        status
        healthStatus
      }
      totalNodes
      healthyNodes
      updatedAt
    }
    analytics {
      infra {
        clusterTrafficMatrix(timeRange: $timeRange) {
          clusterId
          remoteClusterId
          eventCount
          successCount
          avgLatencyMs
          avgDistanceKm
          successRate
          localLatitude
          localLongitude
          remoteLatitude
          remoteLongitude
        }
        federationSummary(timeRange: $timeRange) {
          eventCounts {
            eventType
            count
            failureCount
            avgLatencyMs
          }
          totalEvents
          overallAvgLatencyMs
          overallFailureRate
        }
      }
    }
  }
  """

  static let GetNetworkStatus = """
  # Public network status for marketing site and webapp (no auth required)
  # Returns full infrastructure topology: clusters, nodes, services, peer connections
  query GetNetworkStatus {
    networkStatus {
      clusters {
        clusterId
        name
        region
        latitude
        longitude
        nodeCount
        healthyNodeCount
        peerCount
        status
        clusterType
        shortDescription
        maxStreams
        currentStreams
        maxViewers
        currentViewers
        maxBandwidthMbps
        currentBandwidthMbps
        services
      }
      peerConnections {
        sourceCluster
        targetCluster
        connected
        connectionType
      }
      nodes {
        nodeId
        name
        nodeType
        latitude
        longitude
        status
        clusterId
      }
      serviceInstances {
        instanceId
        serviceId
        clusterId
        nodeId
        status
        healthStatus
      }
      totalNodes
      healthyNodes
      updatedAt
    }
  }
  """

  static let GetNodeMetrics = """
  # Fetch paginated node metrics including CPU, memory, disk, network, and connection data
  # Used for individual node monitoring and resource tracking
  query GetNodeMetrics(
    $nodeId: String
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
    $noCache: Boolean = false
  ) {
    analytics {
      infra {
        nodeMetricsConnection(
          nodeId: $nodeId
          timeRange: $timeRange
          noCache: $noCache
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              timestamp
              nodeId
              cpuUsage
              memoryTotal
              memoryUsed
              diskTotal
              diskUsed
              shmTotal
              shmUsed
              networkRx
              networkTx
              upSpeed
              downSpeed
              connectionsCurrent
              status
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetNodePerformance5m = """
  # 5-minute node performance aggregates
  query GetNodePerformance5m(
    $nodeId: String
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
  ) {
    analytics {
      infra {
        nodePerformance5mConnection(
          nodeId: $nodeId
          timeRange: $timeRange
          page: { first: $first, after: $after }
        ) {
          edges {
            cursor
            node {
              id
              timestamp
              nodeId
              avgCpu
              maxCpu
              avgMemory
              maxMemory
              totalBandwidth
              avgStreams
              maxStreams
            }
          }
          pageInfo {
            ...PageInfoFields
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetNodesConnection = """
  # Fetch paginated list of nodes with filtering by cluster, status, and type
  query GetNodesConnection(
    $clusterId: String
    $status: NodeStatus
    $type: String
    $first: Int = 50
    $after: String
  ) {
    nodesConnection(
      clusterId: $clusterId
      status: $status
      type: $type
      page: { first: $first, after: $after }
    )  {
      edges {
        cursor
        node {
          ...NodeListFields
        }
      }
      pageInfo {
        ...PageInfoFields
      }
      totalCount
    }
  }
  """

  static let GetPendingSubscriptions = """
  # Fetch pending subscription requests for a cluster you own
  query GetPendingSubscriptions($clusterId: ID!) {
    pendingSubscriptions(clusterId: $clusterId) {
      id
      tenantId
      tenantName
      clusterId
      clusterName
      subscriptionStatus
      requestedAt
      accessLevel
    }
  }
  """

  static let GetPlatformOverview = """
  # Fetch platform-wide aggregate metrics and daily statistics for dashboard overview
  # Returns total streams, viewers, bandwidth, and time-series data
  query GetPlatformOverview($timeRange: TimeRangeInput, $days: Int = 7) {
    analytics {
      overview(timeRange: $timeRange) {
        totalStreams
        activeStreams
        totalViewers
        averageViewers
        peakBandwidth
        streamHours
        egressGb
        peakViewers
        totalUploadBytes
        totalDownloadBytes
        viewerHours
        deliveredMinutes
        uniqueViewers
        ingestHours
        peakConcurrentViewers
        totalViews
        timeRange {
          start
          end
        }
        dailyStats(days: $days) {
          id
          date
          egressGb
          viewerHours
          uniqueViewers
          totalSessions
          totalViews
        }
      }
    }
  }
  """

  static let GetPrepaidBalance = """
  query GetPrepaidBalance($currency: String = "USD") {
    prepaidBalance(currency: $currency) {
      id
      tenantId
      balanceCents
      currency
      lowBalanceThresholdCents
      isLowBalance
      createdAt
      updatedAt
    }
  }
  """

  static let GetProcessingUsage = """
  # Fetch transcoding/processing usage records for analytics
  # Returns detailed records and daily summaries for billing display
  query GetProcessingUsage(
    $streamId: ID
    $processType: String
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
  ) {
    analytics {
      usage {
        processing {
          processingUsageConnection(
            streamId: $streamId
            processType: $processType
            timeRange: $timeRange
            page: { first: $first, after: $after }
          ) {
            edges {
              cursor
              node {
                id
                timestamp
                nodeId
                streamId
                stream {
                  streamId
                }
                processType
                trackType
                durationMs
                inputCodec
                outputCodec
                width
                height
                outputWidth
                outputHeight
                outputFpsMeasured
                outputBitrateBps
                rtfOut
                pipelineLagMs
              }
            }
            pageInfo {
              hasNextPage
              endCursor
            }
            totalCount
            summaries {
              date
              livepeerSeconds
              livepeerSegmentCount
              livepeerUniqueStreams
              livepeerH264Seconds
              livepeerVp9Seconds
              livepeerAv1Seconds
              livepeerHevcSeconds
              nativeAvSeconds
              nativeAvSegmentCount
              nativeAvUniqueStreams
              nativeAvH264Seconds
              nativeAvVp9Seconds
              nativeAvAv1Seconds
              nativeAvHevcSeconds
              nativeAvAacSeconds
              nativeAvOpusSeconds
              audioSeconds
              videoSeconds
            }
          }
        }
      }
    }
  }
  """

  static let GetPushTargets = """
  # Fetch push targets for a stream (multistream/restreaming destinations)
  query GetPushTargets($streamId: ID!) {
    stream(id: $streamId) {
      id
      pushTargets {
        ...PushTargetFields 
      }
    }
  }
  """

  static let GetRebufferingEventsConnection = """
  query GetRebufferingEventsConnection(
    $streamId: ID
    $nodeId: String
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
    $last: Int
    $before: String
    $noCache: Boolean = false
  ) {
    analytics {
      health {
        rebufferingEventsConnection(
          streamId: $streamId
          nodeId: $nodeId
          timeRange: $timeRange
          noCache: $noCache
          page: { first: $first, after: $after, last: $last, before: $before }
        ) {
          edges {
            cursor
            node {
              timestamp
              streamId
              nodeId
              bufferState
              previousState
              rebufferStart
              rebufferEnd
            }
          }
          pageInfo {
            hasNextPage
            hasPreviousPage
            startCursor
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetRoutingEfficiency = """
  # Pre-aggregated routing efficiency summary (replaces client-side aggregation of raw events)
  query GetRoutingEfficiency($streamId: ID, $timeRange: TimeRangeInput) {
    analytics {
      infra {
        routingEfficiency(streamId: $streamId, timeRange: $timeRange) {
          totalDecisions
          successCount
          successRate
          avgRoutingDistance
          avgLatencyMs
          topCountries {
            countryCode
            requestCount
          }
        }
      }
    }
  }
  """

  static let GetRoutingEvents = """
  # Fetch paginated routing events for a specific stream
  # Trimmed field set for dashboard use; use full query for map/detail views
  query GetRoutingEvents(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 30
    $after: String
    $noCache: Boolean = false
  ) {
    analytics {
      infra {
        routingEventsConnection(
          streamId: $streamId
          timeRange: $timeRange
          noCache: $noCache
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              timestamp
              streamId
              selectedNode
              nodeId
              status
              clientCountry
              clientLatitude
              clientLongitude
              nodeLatitude
              nodeLongitude
              nodeName
              routingDistance
              candidatesCount
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetRoutingEventsDetailed = """
  # Full routing events query with all fields for map/bucket visualization
  # Used by analytics/audience page; health page uses the trimmed GetRoutingEvents
  query GetRoutingEventsDetailed(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 50
    $after: String
    $noCache: Boolean = false
  ) {
    analytics {
      infra {
        routingEventsConnection(
          streamId: $streamId
          timeRange: $timeRange
          noCache: $noCache
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              timestamp
              streamId
              selectedNode
              nodeId
              status
              details
              score
              clientCountry
              clientLatitude
              clientLongitude
              clientBucket {
                h3Index
                resolution
              }
              nodeLatitude
              nodeLongitude
              nodeName
              nodeBucket {
                h3Index
                resolution
              }
              routingDistance
              latencyMs
              candidatesCount
              eventType
              source
              streamTenantId
              clusterId
              remoteClusterId
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetServiceInstancesConnection = """
  # Fetch paginated service instances with filtering by cluster, node, and status
  # Returns running service instances with health and process details
  query GetServiceInstancesConnection(
    $clusterId: String
    $nodeId: String
    $status: InstanceStatus
    $first: Int = 50
    $after: String
  ) {
    analytics {
      infra {
        serviceInstancesConnection(
          clusterId: $clusterId
          nodeId: $nodeId
          status: $status
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              id
              instanceId
              clusterId
              nodeId
              serviceId
              version
              port
              processId
              containerId
              status
              healthStatus
              startedAt
              stoppedAt
              lastHealthCheck
            }
          }
          pageInfo {
            hasNextPage
            hasPreviousPage
            startCursor
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetServiceInstancesHealth = """
  # Get health status of service instances
  query GetServiceInstancesHealth($serviceId: String) {
    analytics {
      infra {
        serviceInstancesHealth(serviceId: $serviceId) {
          instanceId
          serviceId
          clusterId
          protocol
          host
          port
          healthEndpoint
          status
          lastHealthCheck
        }
      }
    }
  }
  """

  static let GetSkipperConversation = """
  query GetSkipperConversation($id: ID!) {
    skipperConversation(id: $id) {
      id
      title
      messages {
        id
        role
        content
        confidence
        sources
        toolsUsed
        confidenceBlocks
        createdAt
      }
      createdAt
      updatedAt
    }
  }
  """

  static let GetSkipperConversations = """
  query GetSkipperConversations($limit: Int, $offset: Int) {
    skipperConversations(limit: $limit, offset: $offset) {
      id
      title
      createdAt
      updatedAt
    }
  }
  """

  static let GetSkipperReports = """
  query GetSkipperReports($limit: Int = 20, $offset: Int = 0) {
    skipperReports(limit: $limit, offset: $offset) {
      nodes {
        id
        trigger
        summary
        metricsReviewed
        rootCause
        recommendations {
          text
          confidence
        }
        createdAt
        readAt
      }
      totalCount
      unreadCount
    }
  }
  """

  static let GetSkipperUnreadReportCount = """
  query GetSkipperUnreadReportCount {
    skipperUnreadReportCount
  }
  """

  static let GetStorageEventsConnection = """
  # Fetch storage lifecycle events (freeze/defrost)
  query GetStorageEventsConnection(
    $streamId: ID
    $assetType: String
    $timeRange: TimeRangeInput
    $first: Int = 50
    $after: String
  ) {
    analytics {
      lifecycle {
        storageEventsConnection(
          streamId: $streamId
          assetType: $assetType
          timeRange: $timeRange
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              id
              timestamp
              assetHash
              action
              assetType
              sizeBytes
              nodeId
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetStorageUsage = """
  # Fetch paginated storage usage metrics broken down by content type (DVR, clips, VOD)
  # Returns time-series storage data including frozen/archived content
  query GetStorageUsage(
    $nodeId: String
    $storageScope: String
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
  ) {
    analytics {
      usage {
        storage {
          storageUsageConnection(
            nodeId: $nodeId
            storageScope: $storageScope
            timeRange: $timeRange
            page: { first: $first, after: $after }
          ) {
            edges {
              cursor
              node {
                id
                timestamp
                nodeId
                storageScope
                dvrBytes
                clipBytes
                vodBytes
                totalBytes
                fileCount
                frozenDvrBytes
                frozenClipBytes
                frozenVodBytes
              }
            }
            pageInfo {
              hasNextPage
              hasPreviousPage
              startCursor
              endCursor
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetStream = """
  # Fetch stream with core fields, operational metrics, analytics, and current health
  query GetStream($id: ID!, $streamId: ID!, $timeRange: TimeRangeInput) {
    stream(id: $id) {
      ...StreamCoreFields
      metrics {
        ...StreamMetricsFields
      }
    }
    analytics {
      health {
        streamHealthConnection(streamId: $streamId, timeRange: $timeRange, page: { first: 1 }) {
          edges {
            node {
              ...StreamHealthFields
            }
          }
        }
      }
    }
  }
  """

  static let GetStreamAnalyticsDailyConnection = """
  query GetStreamAnalyticsDailyConnection(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
    $last: Int
    $before: String
    $noCache: Boolean = false
  ) {
    analytics {
      usage {
        streaming {
          streamAnalyticsDailyConnection(
            streamId: $streamId
            timeRange: $timeRange
            noCache: $noCache
            page: { first: $first, after: $after, last: $last, before: $before }
          ) {
            edges {
              cursor
              node {
                id
                day
                streamId
                stream {
                  streamId
                }
                totalViews
                uniqueViewers
                uniqueCountries
                uniqueCities
                egressBytes
              }
            }
            pageInfo {
              hasNextPage
              hasPreviousPage
              startCursor
              endCursor
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetStreamAnalyticsSummariesConnection = """
  query GetStreamAnalyticsSummariesConnection(
    $timeRange: TimeRangeInput!
    $sortBy: StreamSummarySortField = EGRESS_GB
    $sortOrder: SortOrder = DESC
    $first: Int = 10
    $after: String
    $last: Int
    $before: String
  ) {
    analytics {
      usage {
        streaming {
          streamAnalyticsSummariesConnection(
            timeRange: $timeRange
            sortBy: $sortBy
            sortOrder: $sortOrder
            page: { first: $first, after: $after, last: $last, before: $before }
          ) {
            edges {
              cursor
              node {
                streamId
                stream {
                  streamId
                  name
                  metrics {
                    status
                  }
                }
                rangeEgressGb
                rangeUniqueViewers
                rangeTotalViews
                rangeViewerHours
                rangeEgressSharePercent
                rangeViewerSharePercent
              }
            }
            pageInfo {
              hasNextPage
              hasPreviousPage
              startCursor
              endCursor
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetStreamAnalyticsSummary = """
  # Stream analytics: summary stats, hourly viewer hours, daily analytics, and quality tiers
  # Loaded immediately on the analytics page
  query GetStreamAnalyticsSummary(
    $id: ID!
    $streamId: ID!
    $timeRange: TimeRangeInput
    $hourlyFirst: Int = 72
    $qualityFirst: Int = 30
  ) {
    stream(id: $id) {
      id
      streamId
      name
      playbackId
    }
    analytics {
      usage {
        streaming {
          streamAnalyticsSummary(streamId: $streamId, timeRange: $timeRange) {
            streamId
            rangeAvgViewers
            rangePeakConcurrentViewers
            rangeTotalViews
            rangeTotalSessions
            rangeAvgBufferHealth
            rangeAvgBitrate
            rangeAvgFps
            rangePacketLossRate
            rangeAvgConnectionTime
            rangeViewerHours
            rangeEgressGb
            rangeAvgSessionSeconds
            rangeAvgBytesPerSession
            rangeUniqueViewers
            rangeUniqueCountries
            rangeRebufferCount
            rangeIssueCount
            rangeBufferDryCount
            rangeQuality {
              tier2160pMinutes
              tier1440pMinutes
              tier1080pMinutes
              tier720pMinutes
              tier480pMinutes
              tierSdMinutes
              codecH264Minutes
              codecH265Minutes
              codecVp9Minutes
              codecAv1Minutes
            }
          }
          viewerHoursHourlyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $hourlyFirst }
          ) {
            edges {
              node {
                hour
                streamId
                uniqueViewers
                totalSessionSeconds
                viewerHours
                egressGb
              }
            }
          }
          streamAnalyticsDailyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: 30 }
          ) {
            edges {
              node {
                day
                streamId
                totalViews
                uniqueViewers
                uniqueCountries
                uniqueCities
                egressBytes
              }
            }
          }
          qualityTierDailyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $qualityFirst }
          ) {
            edges {
              node {
                day
                streamId
                tier2160pMinutes
                tier1440pMinutes
                tier1080pMinutes
                tier720pMinutes
                tier480pMinutes
                tierSdMinutes
                codecH264Minutes
                codecH265Minutes
                codecVp9Minutes
                codecAv1Minutes
                avgBitrate
                avgFps
              }
            }
          }
        }
      }
    }
  }
  """

  static let GetStreamEvents = """
  # Fetch stream lifecycle events (historical)
  query GetStreamEvents($streamId: ID!, $timeRange: TimeRangeInput, $first: Int = 50) {
    analytics {
      lifecycle {
        streamEventsConnection(streamId: $streamId, timeRange: $timeRange, page: { first: $first }) {
          edges {
            cursor
            node {
              eventId
              type
              streamId
              nodeId
              status
              timestamp
              details
              payload
              source
            }
          }
          pageInfo {
            ...PageInfoFields
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetStreamHealth5mTimeSeries = """
  # Stream health 5-minute aggregates time-series
  # Deferred load on the analytics page
  query GetStreamHealth5mTimeSeries($streamId: ID!, $timeRange: TimeRangeInput, $first: Int = 100) {
    analytics {
      health {
        streamHealth5mConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $first }
        ) {
          edges {
            node {
              timestamp
              nodeId
              avgBitrate
              avgFps
              qualityTier
              issueCount
              rebufferCount
            }
          }
        }
      }
    }
  }
  """

  static let GetStreamHealthClients = """
  # Client-side health: rebuffering events and QoE metrics
  # Deferred load on the health page
  query GetStreamHealthClients($streamId: ID!, $timeRange: TimeRangeInput, $first: Int = 50) {
    analytics {
      health {
        rebufferingEventsConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $first }
        ) {
          edges {
            node {
              timestamp
              streamId
              nodeId
              bufferState
              previousState
              rebufferStart
              rebufferEnd
            }
          }
        }
        clientQoeConnection(streamId: $streamId, timeRange: $timeRange, page: { first: $first }) {
          edges {
            node {
              timestamp
              streamId
              nodeId
              packetLossRate
              activeSessions
              avgBandwidthIn
              avgBandwidthOut
              avgConnectionTime
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
        }
      }
    }
  }
  """

  static let GetStreamHealthCore = """
  # Primary stream health: raw metrics time-series and 5-minute aggregates
  # Loaded immediately on the health page
  query GetStreamHealthCore(
    $id: ID!
    $streamId: ID!
    $timeRange: TimeRangeInput
    $metricsFirst: Int = 50
  ) {
    stream(id: $id) {
      id
      name
      playbackId
      metrics {
        status
        isLive
        currentViewers
        bufferState
        qualityTier
        primaryWidth
        primaryHeight
        primaryFps
        primaryCodec
        primaryBitrate
        hasIssues
        issuesDescription
      }
    }
    analytics {
      health {
        streamHealthConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $metricsFirst }
        ) {
          edges {
            node {
              timestamp
              streamId
              nodeId
              hasIssues
              issuesDescription
              bitrate
              fps
              width
              height
              codec
              qualityTier
              gopSize
              bufferState
              bufferHealth
              bufferSize
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
        }
        streamHealth5mConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $metricsFirst }
        ) {
          edges {
            cursor
            node {
              id
              timestamp
              nodeId
              rebufferCount
              issueCount
              sampleIssues
              avgBitrate
              avgFps
              bufferDryCount
              qualityTier
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetStreamHealthLifecycle = """
  # Lifecycle health: track list snapshots and buffer events
  # Deferred/on-demand load on the health page
  query GetStreamHealthLifecycle(
    $streamId: ID!
    $timeRange: TimeRangeInput
    $bufferFirst: Int = 100
  ) {
    analytics {
      lifecycle {
        trackListConnection(streamId: $streamId, timeRange: $timeRange, page: { first: 20 }) {
          edges {
            node {
              timestamp
              streamId
              nodeId
              trackList
              trackCount
              tracks {
                trackName
                trackType
                codec
                bitrateKbps
                width
                height
                fps
                buffer
                jitter
                channels
                sampleRate
                hasBFrames
              }
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
        }
        bufferEventsConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $bufferFirst }
        ) {
          edges {
            cursor
            node {
              eventId
              timestamp
              nodeId
              bufferState
              eventData
              payload
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetStreamHealthSummary = """
  # Pre-aggregated stream health summary for dashboard views
  query GetStreamHealthSummary($streamId: ID, $timeRange: TimeRangeInput) {
    analytics {
      health {
        streamHealthSummary(streamId: $streamId, timeRange: $timeRange) {
          avgBitrate
          avgFps
          avgBufferHealth
          totalRebufferCount
          totalIssueCount
          sampleCount
          hasActiveIssues
          currentQualityTier
        }
      }
    }
  }
  """

  static let GetStreamingConfig = """
  query GetStreamingConfig {
    streamingConfig {
      preferredClusterLabel
      ingestDomain
      edgeDomain
      playDomain
      chandlerDomain
      officialClusterLabel
      officialIngestDomain
      officialEdgeDomain
      officialPlayDomain
      officialChandlerDomain
      srtPort
      rtmpPort
    }
  }
  """

  static let GetStreamKeys = """
  # Fetch paginated list of stream keys for a specific stream
  # Returns active/inactive keys with usage timestamps for credential management
  query GetStreamKeys($streamId: ID!, $first: Int = 50, $after: String) {
    streamKeysConnection(streamId: $streamId, page: { first: $first, after: $after }) {
      edges {
        cursor
        node {
          id
          streamId
          keyValue
          keyName
          isActive
          lastUsedAt
          createdAt
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetStreamOverviewCharts = """
  # Stream overview charts: hourly connections, quality tier breakdown, and health time-series
  # Deferred load on the stream detail page
  query GetStreamOverviewCharts(
    $streamId: ID!
    $timeRange: TimeRangeInput
    $first: Int = 50
    $qualityFirst: Int = 30
    $healthFirst: Int = 24
  ) {
    analytics {
      usage {
        streaming {
          streamConnectionHourlyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $first }
          ) {
            edges {
              cursor
              node {
                hour
                streamId
                totalBytes
                uniqueViewers
                totalSessions
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
          qualityTierDailyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $qualityFirst }
          ) {
            edges {
              cursor
              node {
                day
                streamId
                tier2160pMinutes
                tier1440pMinutes
                tier1080pMinutes
                tier720pMinutes
                tier480pMinutes
                tierSdMinutes
                codecH264Minutes
                codecH265Minutes
                avgBitrate
                avgFps
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
        }
      }
      health {
        streamHealthConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $healthFirst }
        ) {
          edges {
            node {
              timestamp
              streamId
              nodeId
              hasIssues
              issuesDescription
              bitrate
              fps
              width
              height
              codec
              qualityTier
              bufferState
              bufferHealth
            }
          }
          pageInfo {
            ...PageInfoFields
          }
        }
      }
    }
  }
  """

  static let GetStreamOverviewCore = """
  # Stream overview: core fields, metrics, analytics summary, and viewer time-series
  # Loaded immediately on the stream detail page
  query GetStreamOverviewCore(
    $id: ID!
    $streamId: ID!
    $timeRange: TimeRangeInput
    $viewerFirst: Int = 100
    $viewerInterval: String = "1h"
  ) {
    stream(id: $id) {
      ...StreamCoreFields
  
      metrics {
        ...StreamMetricsFields
      }
    }
    analytics {
      usage {
        streaming {
          streamAnalyticsSummary(streamId: $streamId, timeRange: $timeRange) {
            streamId
            rangeAvgViewers
            rangePeakConcurrentViewers
            rangeTotalViews
            rangeTotalSessions
            rangeUniqueViewers
            rangeUniqueCountries
            rangeViewerHours
            rangeEgressGb
            rangeAvgSessionSeconds
            rangeAvgBytesPerSession
            rangeAvgBufferHealth
            rangeAvgBitrate
            rangeAvgFps
            rangePacketLossRate
            rangeAvgConnectionTime
            rangeRebufferCount
            rangeIssueCount
            rangeBufferDryCount
            rangeQuality {
              tier2160pMinutes
              tier1440pMinutes
              tier1080pMinutes
              tier720pMinutes
              tier480pMinutes
              tierSdMinutes
              codecH264Minutes
              codecH265Minutes
              codecVp9Minutes
              codecAv1Minutes
            }
          }
          viewerTimeSeriesConnection(
            streamId: $streamId
            timeRange: $timeRange
            interval: $viewerInterval
            page: { first: $viewerFirst }
          ) {
            edges {
              cursor
              node {
                timestamp
                streamId
                viewerCount
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetStreamsConnection = """
  # Fetch paginated list of streams with core fields and live status metrics
  query GetStreamsConnection($first: Int = 50, $after: String) {
    streamsConnection(page: { first: $first, after: $after })  {
      edges {
        cursor
        node {
          ...StreamCoreFields
          metrics {
            ...StreamMetricsListFields
          }
        }
      }
      pageInfo {
        ...PageInfoFields
      }
      totalCount
    }
  }
  """

  static let GetStreamSessions = """
  # Fetch stream session/analytics data including hourly aggregates, quality metrics, and viewer sessions
  # Trimmed field set and reduced page sizes for lower complexity
  query GetStreamSessions(
    $streamId: ID!
    $timeRange: TimeRangeInput
    $first: Int = 50
    $sessionFirst: Int = 25
  ) {
    analytics {
      usage {
        streaming {
          streamConnectionHourlyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $first }
          ) {
            edges {
              cursor
              node {
                id
                hour
                streamId
                totalBytes
                uniqueViewers
                totalSessions
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
        }
      }
      lifecycle {
        viewerSessionsConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $sessionFirst }
        ) {
          edges {
            cursor
            node {
              id
              timestamp
              sessionId
              connector
              countryCode
              city
              durationSeconds
              connectionQuality
            }
          }
          pageInfo {
            ...PageInfoFields
          }
          totalCount
        }
      }
      health {
        clientQoeConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $sessionFirst }
        ) {
          edges {
            cursor
            node {
              id
              timestamp
              streamId
              nodeId
              activeSessions
              avgBandwidthIn
              avgBandwidthOut
              avgConnectionTime
              packetLossRate
            }
          }
          pageInfo {
            ...PageInfoFields
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetTenantAnalyticsDailyConnection = """
  query GetTenantAnalyticsDailyConnection(
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
    $last: Int
    $before: String
    $noCache: Boolean = false
  ) {
    analytics {
      usage {
        streaming {
          tenantAnalyticsDailyConnection(
            timeRange: $timeRange
            noCache: $noCache
            page: { first: $first, after: $after, last: $last, before: $before }
          ) {
            edges {
              cursor
              node {
                id
                day
                totalStreams
                totalViews
                uniqueViewers
                egressBytes
              }
            }
            pageInfo {
              hasNextPage
              hasPreviousPage
              startCursor
              endCursor
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetUsageAggregates = """
  query GetUsageAggregates(
    $timeRange: TimeRangeInput!
    $granularity: String = "daily"
    $usageTypes: [String!]
  ) {
    usageAggregates(timeRange: $timeRange, granularity: $granularity, usageTypes: $usageTypes) {
      usageType
      periodStart
      periodEnd
      usageValue
      granularity
    }
  }
  """

  static let GetUsageRecords = """
  # Fetch paginated usage records by cluster and usage type for billing analysis
  query GetUsageRecords($timeRange: TimeRangeInput, $first: Int = 50, $after: String) {
    usageRecordsConnection(timeRange: $timeRange, page: { first: $first, after: $after }) {
      edges {
        cursor
        node {
          id
          clusterId
          clusterName
          usageType
          usageValue
          createdAt
          periodStart
          periodEnd
          granularity
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let GetViewerGeoHourly = """
  # Hourly geographic viewer aggregates (from viewer_geo_hourly MV)
  query GetViewerGeoHourly($timeRange: TimeRangeInput, $first: Int = 100, $after: String) {
    analytics {
      usage {
        streaming {
          viewerGeoHourlyConnection(timeRange: $timeRange, page: { first: $first, after: $after }) {
            edges {
              cursor
              node {
                id
                hour
                countryCode
                viewerCount
                viewerHours
                egressGb
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetViewerHoursHourly = """
  # Hourly viewer hours aggregates (from viewer_hours_hourly MV)
  query GetViewerHoursHourly(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 100
    $after: String
  ) {
    analytics {
      usage {
        streaming {
          viewerHoursHourlyConnection(
            streamId: $streamId
            timeRange: $timeRange
            page: { first: $first, after: $after }
          ) {
            edges {
              cursor
              node {
                id
                hour
                streamId
                countryCode
                uniqueViewers
                totalSessionSeconds
                totalBytes
                viewerHours
                egressGb
              }
            }
            pageInfo {
              ...PageInfoFields
            }
            totalCount
          }
        }
      }
    }
  }
  """

  static let GetViewerSessionsConnection = """
  # Fetch viewer session details for a stream
  query GetViewerSessionsConnection(
    $streamId: ID
    $timeRange: TimeRangeInput
    $first: Int = 50
    $after: String
  ) {
    analytics {
      lifecycle {
        viewerSessionsConnection(
          streamId: $streamId
          timeRange: $timeRange
          page: { first: $first, after: $after }
        )  {
          edges {
            cursor
            node {
              id
              timestamp
              sessionId
              connector
              countryCode
              city
              durationSeconds
              connectionQuality
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
          totalCount
        }
      }
    }
  }
  """

  static let GetVodAssetsConnection = """
  query GetVodAssetsConnection($first: Int = 50, $after: String, $last: Int, $before: String) {
    vodAssetsConnection(page: { first: $first, after: $after, last: $last, before: $before }) {
      edges {
        cursor
        node {
          id
          artifactHash
          playbackId
          title
          description
          filename
          status
          storageLocation
          sizeBytes
          durationMs
          resolution
          videoCodec
          audioCodec
          bitrateKbps
          createdAt
          updatedAt
          expiresAt
          errorMessage
        }
      }
      pageInfo {
        hasNextPage
        hasPreviousPage
        startCursor
        endCursor
      }
      totalCount
    }
  }
  """

  static let ResolveViewerEndpoint = """
  # Resolve viewer endpoint for playback
  # Returns optimal node(s) for streaming content to the viewer
  query ResolveViewerEndpoint($contentId: String!) {
    resolveViewerEndpoint(contentId: $contentId) {
      primary {
        ...ViewerEndpointFields
      }
      fallbacks {
        ...ViewerEndpointFields
      }
      metadata {
        ...PlaybackMetadataFields
      }
    }
  }
  """

  // MARK: - mutations

  static let AbortVodUpload = """
  mutation AbortVodUpload($uploadId: ID!) {
    abortVodUpload(uploadId: $uploadId) {
      ... on DeleteSuccess {
        success
        deletedId
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let AcceptClusterInvite = """
  # Accept a cluster invitation using an invite token
  mutation AcceptClusterInvite($inviteToken: String!) {
    acceptClusterInvite(inviteToken: $inviteToken) {
      ... on ClusterSubscription {
        id
        subscriptionStatus
        approvedAt
      }
      ... on ValidationError {
        message
        code
        field
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let ApproveClusterSubscription = """
  # Approve a pending cluster subscription request
  mutation ApproveClusterSubscription($subscriptionId: ID!) {
    approveClusterSubscription(subscriptionId: $subscriptionId) {
      ... on ClusterSubscription {
        id
        subscriptionStatus
        tenantName
        clusterName
      }
      ... on ValidationError {
        message
        code
        field
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let CompleteVodUpload = """
  mutation CompleteVodUpload($input: CompleteVodUploadInput!) {
    completeVodUpload(input: $input) {
      ... on VodAsset {
        id
        artifactHash
        playbackId
        title
        description
        filename
        status
        storageLocation
        sizeBytes
        durationMs
        resolution
        videoCodec
        audioCodec
        bitrateKbps
        createdAt
        updatedAt
        expiresAt
        errorMessage
      }
      ... on ValidationError {
        message
        field
        code
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let CreateAPIToken = """
  # Create a new developer API token for programmatic access
  mutation CreateAPIToken($input: CreateDeveloperTokenInput!) {
    createDeveloperToken(input: $input) {
      __typename
      ... on DeveloperToken {
        id
        tokenName
        tokenValue
        permissions
        status
        expiresAt
        lastUsedAt
        createdAt
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on RateLimitError {
        ...RateLimitErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreateCardTopup = """
  mutation CreateCardTopup($input: CreateCardTopupInput!) {
    createCardTopup(input: $input) {
      topupId
      checkoutUrl
      expiresAt
    }
  }
  """

  static let CreateClip = """
  # Create a new clip from a stream recording
  mutation CreateClip($input: CreateClipInput!) {
    createClip(input: $input) {
      __typename
      ... on Clip {
        ...ClipFields
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreateConversation = """
  mutation CreateConversation($input: CreateConversationInput!) {
    createConversation(input: $input) {
      ... on Conversation {
        id
        subject
        status
        unreadCount
        createdAt
        updatedAt
      }
      ... on ValidationError {
        message
        code
      }
    }
  }
  """

  static let CreateCryptoTopup = """
  mutation CreateCryptoTopup($input: CreateCryptoTopupInput!) {
    createCryptoTopup(input: $input) {
      topupId
      depositAddress
      asset
      assetSymbol
      expectedAmountCents
      expiresAt
    }
  }
  """

  static let CreatePayment = """
  # Create a new payment transaction
  mutation CreatePayment($input: CreatePaymentInput!) {
    createPayment(input: $input) {
      __typename
      ... on Payment {
        id
        amount
        currency
        method
        status
        createdAt
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreatePrivateCluster = """
  # Create a new private cluster with bootstrap token
  mutation CreatePrivateCluster($input: CreatePrivateClusterInput!) {
    createPrivateCluster(input: $input) {
      __typename
      ... on CreatePrivateClusterResponse {
        cluster {
          ...ClusterFields
        }
        bootstrapToken {
          ...BootstrapTokenFields
        }
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreatePushTarget = """
  # Create a new multistream push target for a stream
  mutation CreatePushTarget($streamId: ID!, $input: CreatePushTargetInput!) {
    createPushTarget(streamId: $streamId, input: $input) {
      ...PushTargetFields 
    }
  }
  """

  static let CreateStream = """
  # Create a new stream with the specified name and settings
  mutation CreateStream($input: CreateStreamInput!) {
    createStream(input: $input) {
      __typename
      ... on Stream {
        ...StreamCoreFields
        metrics {
          ...StreamMetricsFields
        }
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreateStreamKey = """
  # Create a new stream key for a stream
  mutation CreateStreamKey($streamId: ID!, $input: CreateStreamKeyInput!) {
    createStreamKey(streamId: $streamId, input: $input) {
      __typename
      ... on StreamKey {
        id
        streamId
        keyValue
        keyName
        isActive
        lastUsedAt
        createdAt
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let CreateVodUpload = """
  mutation CreateVodUpload($input: CreateVodUploadInput!) {
    createVodUpload(input: $input) {
      ... on VodUploadSession {
        id
        artifactId
        artifactHash
        playbackId
        partSize
        parts {
          partNumber
          presignedUrl
        }
        expiresAt
      }
      ... on ValidationError {
        message
        field
        code
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let DeleteClip = """
  # Delete a clip by its ID
  mutation DeleteClip($id: ID!) {
    deleteClip(id: $id) {
      __typename
      ... on DeleteSuccess {
        ...DeleteSuccessFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let DeleteDVR = """
  # Delete a DVR recording by its hash
  mutation DeleteDVR($dvrHash: ID!) {
    deleteDVR(dvrHash: $dvrHash) {
      ... on DeleteSuccess {
        ...DeleteSuccessFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let DeletePushTarget = """
  # Delete a multistream push target
  mutation DeletePushTarget($id: ID!) {
    deletePushTarget(id: $id) {
      ...DeleteSuccessFields 
    }
  }
  """

  static let DeleteSkipperConversation = """
  mutation DeleteSkipperConversation($id: ID!) {
    deleteSkipperConversation(id: $id)
  }
  """

  static let DeleteStream = """
  # Delete a stream by its ID
  mutation DeleteStream($id: ID!) {
    deleteStream(id: $id) {
      __typename
      ... on DeleteSuccess {
        ...DeleteSuccessFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let DeleteStreamKey = """
  # Delete a stream key from a stream
  mutation DeleteStreamKey($streamId: ID!, $keyId: ID!) {
    deleteStreamKey(streamId: $streamId, keyId: $keyId) {
      __typename
      ... on DeleteSuccess {
        ...DeleteSuccessFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let DeleteVodAsset = """
  mutation DeleteVodAsset($id: ID!) {
    deleteVodAsset(id: $id) {
      ... on DeleteSuccess {
        success
        deletedId
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let GetCryptoTopupStatus = """
  mutation GetCryptoTopupStatus($topupId: ID!) {
    cryptoTopupStatus(topupId: $topupId) {
      id
      depositAddress
      asset
      status
      txHash
      confirmations
      receivedAmountWei
      creditedAmountCents
      expiresAt
      detectedAt
    }
  }
  """

  static let LinkEmail = """
  mutation LinkEmail($input: LinkEmailInput!) {
    linkEmail(input: $input) {
      ... on LinkEmailPayload {
        success
        message
        verificationSent
      }
      ... on ValidationError {
        message
        field
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let LinkWallet = """
  mutation LinkWallet($input: WalletLoginInput!) {
    linkWallet(input: $input) {
      ... on WalletIdentity {
        id
        address
        createdAt
        lastAuthAt
      }
      ... on ValidationError {
        message
        field
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let MarkSkipperReportsRead = """
  mutation MarkSkipperReportsRead($ids: [ID!]) {
    markSkipperReportsRead(ids: $ids)
  }
  """

  static let PromoteToPaid = """
  mutation PromoteToPaid($tierId: ID!) {
    promoteToPaid(tierId: $tierId) {
      ... on PromoteToPaidPayload {
        success
        message
        newBillingModel
        creditBalanceCents
      }
      ... on ValidationError {
        message
        field
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let RefreshStreamKey = """
  # Regenerate a new stream key for a stream
  mutation RefreshStreamKey($id: ID!) {
    refreshStreamKey(id: $id) {
      __typename
      ... on Stream {
        id
        name
        description
        streamKey
        playbackId
        record
        createdAt
        updatedAt
        metrics {
          status
          isLive
          currentViewers
          startedAt
          updatedAt
        }
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let RejectClusterSubscription = """
  # Reject a pending cluster subscription request
  mutation RejectClusterSubscription($subscriptionId: ID!, $reason: String) {
    rejectClusterSubscription(subscriptionId: $subscriptionId, reason: $reason) {
      ... on ClusterSubscription {
        id
        subscriptionStatus
        tenantName
        clusterName
      }
      ... on ValidationError {
        message
        code
        field
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let RequestClusterSubscription = """
  # Request to subscribe to a cluster (with optional invite token)
  mutation RequestClusterSubscription($clusterId: ID!, $inviteToken: String) {
    requestClusterSubscription(clusterId: $clusterId, inviteToken: $inviteToken) {
      ... on ClusterSubscription {
        id
        subscriptionStatus
        clusterName
      }
      ... on ValidationError {
        message
        code
        field
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let RevokeAPIToken = """
  # Revoke a developer API token by its ID
  mutation RevokeAPIToken($id: ID!) {
    revokeDeveloperToken(id: $id) {
      __typename
      ... on DeleteSuccess {
        ...DeleteSuccessFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let SendMessage = """
  mutation SendMessage($input: SendMessageInput!) {
    sendMessage(input: $input) {
      ... on Message {
        id
        conversationId
        content
        sender
        createdAt
      }
      ... on ValidationError {
        message
        code
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
    }
  }
  """

  static let SetPreferredCluster = """
  # Set the tenant's preferred cluster for DNS steering and primary URIs
  mutation SetPreferredCluster($clusterId: ID!) {
    setPreferredCluster(clusterId: $clusterId) {
      ... on Cluster {
        id
        clusterId
        clusterName
      }
      ... on ValidationError {
        message
        code
        field
      }
      ... on NotFoundError {
        message
        code
        resourceType
        resourceId
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let StartDVR = """
  # Start DVR recording for a stream with optional expiration
  mutation StartDVR($streamId: ID!, $expiresAt: Int) {
    startDVR(streamId: $streamId, expiresAt: $expiresAt) {
      __typename
      ... on DVRRequest {
        ...DVRRequestFields
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let SubscribeToCluster = """
  # Subscribe to real-time updates from a cluster
  mutation SubscribeToCluster($clusterId: ID!) {
    subscribeToCluster(clusterId: $clusterId)
  }
  """

  static let UnlinkWallet = """
  mutation UnlinkWallet($walletId: ID!) {
    unlinkWallet(walletId: $walletId) {
      ... on DeleteSuccess {
        success
        deletedId
      }
      ... on NotFoundError {
        message
      }
      ... on AuthError {
        message
        code
      }
    }
  }
  """

  static let UnsubscribeFromCluster = """
  # Unsubscribe from real-time cluster updates
  mutation UnsubscribeFromCluster($clusterId: ID!) {
    unsubscribeFromCluster(clusterId: $clusterId)
  }
  """

  static let UpdateBillingDetails = """
  mutation UpdateBillingDetails($input: UpdateBillingDetailsInput!) {
    updateBillingDetails(input: $input) {
      email
      company
      vatNumber
      address {
        street
        city
        state
        postalCode
        country
      }
      isComplete
      updatedAt
    }
  }
  """

  static let UpdatePushTarget = """
  # Update a multistream push target
  mutation UpdatePushTarget($id: ID!, $input: UpdatePushTargetInput!) {
    updatePushTarget(id: $id, input: $input) {
      ...PushTargetFields 
    }
  }
  """

  static let UpdateSkipperConversation = """
  mutation UpdateSkipperConversation($id: ID!, $title: String!) {
    updateSkipperConversation(id: $id, title: $title) {
      id
      title
      updatedAt
    }
  }
  """

  static let UpdateStream = """
  # Update stream settings and configuration
  mutation UpdateStream($id: ID!, $input: UpdateStreamInput!) {
    updateStream(id: $id, input: $input) {
      __typename
      ... on Stream {
        ...StreamCoreFields
        metrics {
          ...StreamMetricsFields
        }
      }
      ... on ValidationError {
        ...ValidationErrorFields 
      }
      ... on NotFoundError {
        ...NotFoundErrorFields 
      }
      ... on AuthError {
        ...AuthErrorFields 
      }
    }
  }
  """

  static let WalletLogin = """
  mutation WalletLogin($input: WalletLoginInput!) {
    walletLogin(input: $input) {
      ... on WalletLoginPayload {
        token
        user {
          id
          email
          name
          role
        }
        expiresAt
        isNewAccount
      }
      ... on ValidationError {
        message
        field
      }
    }
  }
  """

  // MARK: - subscriptions

  static let ClipLifecycle = """
  # Real-time clip generation progress and completion updates
  # Monitors clip creation stages, upload progress, S3 URLs, and error states
  subscription ClipLifecycle($streamId: ID!) {
    liveClipLifecycle(streamId: $streamId) {
      stage
      clipHash
      playbackId
      progressPercent
      filePath
      s3Url
      sizeBytes
      error
      startedAt
      completedAt
      nodeId
      streamId
      stream {
        streamId
      }
      startUnix
      stopUnix
      durationSec
      clipMode
    }
  }
  """

  static let ConnectionEventsLive = """
  # Real-time viewer connection/disconnection events
  # Shows geographic distribution and session metrics for live streams
  subscription ConnectionEventsLive($streamId: ID) {
    liveConnectionEvents(streamId: $streamId) {
      eventId
      timestamp
      streamId
      sessionId
      connector
      nodeId
      countryCode
      city
      latitude
      longitude
      eventType
      sessionDurationSeconds
      bytesTransferred
    }
  }
  """

  static let DvrLifecycle = """
  # Real-time DVR recording lifecycle updates from start to completion
  # Tracks recording status, progress, manifest paths, and error states
  subscription DvrLifecycle($streamId: ID!) {
    liveDvrLifecycle(streamId: $streamId) {
      status
      dvrHash
      playbackId
      manifestPath
      startedAt
      endedAt
      sizeBytes
      segmentCount
      error
      nodeId
      streamId
      stream {
        streamId
      }
    }
  }
  """

  static let LiveConversationUpdates = """
  subscription LiveConversationUpdates($conversationId: ID) {
    liveConversationUpdates(conversationId: $conversationId) {
      id
      subject
      status
      updatedAt
      lastMessage {
        id
        conversationId
        sender
        createdAt
      }
    }
  }
  """

  static let LiveMessageReceived = """
  subscription LiveMessageReceived($conversationId: ID!) {
    liveMessageReceived(conversationId: $conversationId) {
      id
      conversationId
      sender
      createdAt
    }
  }
  """

  static let ProcessingEventsLive = """
  # Real-time transcoding/processing events
  # Tracks encode performance, renditions, and billing metrics
  subscription ProcessingEventsLive($streamId: ID) {
    liveProcessingEvents(streamId: $streamId) {
      timestamp
      nodeId
      streamId
      processType
      trackType
      durationMs
      inputCodec
      outputCodec
      segmentNumber
      width
      height
      renditionCount
      inputBytes
      outputBytesTotal
      turnaroundMs
      speedFactor
    }
  }
  """

  static let SkipperChat = """
  subscription SkipperChat($input: SkipperChatInput!) {
    skipperChat(input: $input) {
      ... on SkipperToken {
        __typename
        content
      }
      ... on SkipperToolStartEvent {
        __typename
        tool
      }
      ... on SkipperToolEndEvent {
        __typename
        tool
        error
      }
      ... on SkipperMeta {
        __typename
        confidence
        citations {
          label
          url
        }
        externalLinks {
          label
          url
        }
        details {
          title
          payload
        }
        blocks {
          content
          confidence
          sources {
            label
            url
          }
        }
      }
      ... on SkipperDone {
        __typename
        conversationId
        tokensInput
        tokensOutput
      }
    }
  }
  """

  static let StorageEventsLive = """
  # Real-time storage operations for clips/DVR
  # Tracks freeze/defrost lifecycle and storage usage
  subscription StorageEventsLive($streamId: ID) {
    liveStorageEvents(streamId: $streamId) {
      timestamp
      streamId
      assetHash
      action
      assetType
      sizeBytes
      s3Url
      localPath
      nodeId
      durationMs
      warmDurationMs
      error
    }
  }
  """

  static let StreamEvents = """
  # Real-time stream lifecycle events including status changes and state transitions
  # Returns StreamSubscriptionEvent with rich proto payloads
  subscription StreamEvents($streamId: ID) {
    liveStreamEvents(streamId: $streamId) {
      eventId
      streamId
      nodeId
      type
      status
      timestamp
      details
      payload
      source
    }
  }
  """

  static let SystemHealth = """
  # System health and resource utilization updates for all infrastructure nodes
  # Monitors CPU, memory, disk usage, and node status across the platform
  subscription SystemHealth {
    liveSystemHealth {
      nodeId
      node
      location
      status
      cpuTenths
      isHealthy
      ramMax
      ramCurrent
      diskTotalBytes
      diskUsedBytes
      shmTotalBytes
      shmUsedBytes
      timestamp
    }
  }
  """

  static let TrackListUpdates = """
  # Live updates to stream media tracks and encoding parameters
  # Includes video/audio codec details, bitrates, resolution, and quality tier changes
  subscription TrackListUpdates($streamId: ID!) {
    liveTrackListUpdates(streamId: $streamId) {
      streamId
      totalTracks
      videoTrackCount
      audioTrackCount
      qualityTier
      primaryWidth
      primaryHeight
      primaryFps
      primaryVideoBitrate
      primaryVideoCodec
      tracks {
        trackName
        trackType
        codec
        bitrateKbps
        bitrateBps
        buffer
        jitter
        width
        height
        fps
        resolution
        hasBFrames
        channels
        sampleRate
      }
    }
  }
  """

  static let ViewerMetricsStream = """
  # Live viewer connection and playback quality metrics
  # Includes bandwidth, packet loss, geographic location, and connection statistics
  subscription ViewerMetricsStream($streamId: ID!) {
    liveViewerMetrics(streamId: $streamId) {
      nodeId
      streamId
      action
      protocol
      host
      sessionId
      connectionTime
      position
      bandwidthInBps
      bandwidthOutBps
      bytesDownloaded
      bytesUploaded
      packetsSent
      packetsLost
      packetsRetransmitted
      # GeoIP enriched fields
      clientCity
      clientCountry
      clientLatitude
      clientLongitude
    }
  }
  """

  static let VodLifecycle = """
  # Real-time VOD upload progress and completion updates
  # Monitors VOD upload stages, S3 processing, and error states
  subscription VodLifecycle {
    liveVodLifecycle {
      status
      vodHash
      playbackId
      uploadId
      filename
      contentType
      sizeBytes
      s3Url
      filePath
      error
      startedAt
      completedAt
      nodeId
      expiresAt
      durationMs
      resolution
      videoCodec
      audioCodec
    }
  }
  """
}
