export type Maybe<T> = T | null | undefined;
export type InputMaybe<T> = T | null | undefined;
export type Exact<T extends { [key: string]: unknown }> = { [K in keyof T]: T[K] };
export type MakeOptional<T, K extends keyof T> = Omit<T, K> & { [SubKey in K]?: Maybe<T[SubKey]> };
export type MakeMaybe<T, K extends keyof T> = Omit<T, K> & { [SubKey in K]: Maybe<T[SubKey]> };
export type MakeEmpty<T extends { [key: string]: unknown }, K extends keyof T> = { [_ in K]?: never };
export type Incremental<T> = T | { [P in keyof T]?: P extends ' $fragmentName' | '__typename' ? T[P] : never };
/** All built-in and custom scalars, mapped to their actual values */
export type Scalars = {
  ID: { input: string; output: string; }
  String: { input: string; output: string; }
  Boolean: { input: boolean; output: boolean; }
  Int: { input: number; output: number; }
  Float: { input: number; output: number; }
  Currency: { input: any; output: any; }
  JSON: { input: any; output: any; }
  Money: { input: any; output: any; }
  Time: { input: string; output: string; }
};

export enum AlertSeverity {
  Critical = 'CRITICAL',
  High = 'HIGH',
  Low = 'LOW',
  Medium = 'MEDIUM'
}

export enum AlertType {
  HighJitter = 'HIGH_JITTER',
  KeyframeInstability = 'KEYFRAME_INSTABILITY',
  PacketLoss = 'PACKET_LOSS',
  QualityDegradation = 'QUALITY_DEGRADATION',
  Rebuffering = 'REBUFFERING'
}

export type AvailableCluster = {
  __typename?: 'AvailableCluster';
  autoEnroll: Scalars['Boolean']['output'];
  clusterId: Scalars['String']['output'];
  clusterName: Scalars['String']['output'];
  tiers: Array<Scalars['String']['output']>;
};

export type BillingStatus = {
  __typename?: 'BillingStatus';
  currentTier: BillingTier;
  nextBillingDate: Scalars['Time']['output'];
  outstandingAmount: Scalars['Float']['output'];
  status: Scalars['String']['output'];
};

export type BillingTier = {
  __typename?: 'BillingTier';
  currency: Scalars['String']['output'];
  description?: Maybe<Scalars['String']['output']>;
  features: Array<Scalars['String']['output']>;
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  price: Scalars['Float']['output'];
};

export type BootstrapToken = {
  __typename?: 'BootstrapToken';
  createdAt: Scalars['Time']['output'];
  expiresAt?: Maybe<Scalars['Time']['output']>;
  id: Scalars['ID']['output'];
  isActive: Scalars['Boolean']['output'];
  lastUsedAt?: Maybe<Scalars['Time']['output']>;
  name: Scalars['String']['output'];
  token?: Maybe<Scalars['String']['output']>;
  type: BootstrapTokenType;
  usageCount: Scalars['Int']['output'];
  usageLimit?: Maybe<Scalars['Int']['output']>;
};

export enum BootstrapTokenType {
  EdgeNode = 'EDGE_NODE',
  Service = 'SERVICE'
}

export enum BufferState {
  Dry = 'DRY',
  Empty = 'EMPTY',
  Full = 'FULL',
  Recover = 'RECOVER'
}

export type CityMetric = {
  __typename?: 'CityMetric';
  city: Scalars['String']['output'];
  countryCode?: Maybe<Scalars['String']['output']>;
  latitude?: Maybe<Scalars['Float']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  percentage: Scalars['Float']['output'];
  viewerCount: Scalars['Int']['output'];
};

export type Clip = {
  __typename?: 'Clip';
  createdAt: Scalars['Time']['output'];
  description?: Maybe<Scalars['String']['output']>;
  duration: Scalars['Int']['output'];
  endTime: Scalars['Int']['output'];
  id: Scalars['ID']['output'];
  playbackId: Scalars['String']['output'];
  startTime: Scalars['Int']['output'];
  status: Scalars['String']['output'];
  stream: Scalars['String']['output'];
  title: Scalars['String']['output'];
  updatedAt: Scalars['Time']['output'];
  viewingUrls?: Maybe<ClipViewingUrls>;
};

export type ClipEvent = {
  __typename?: 'ClipEvent';
  contentType?: Maybe<Scalars['String']['output']>;
  filePath?: Maybe<Scalars['String']['output']>;
  ingestNodeId?: Maybe<Scalars['String']['output']>;
  internalName: Scalars['String']['output'];
  message?: Maybe<Scalars['String']['output']>;
  percent?: Maybe<Scalars['Int']['output']>;
  requestId: Scalars['String']['output'];
  s3Url?: Maybe<Scalars['String']['output']>;
  sizeBytes?: Maybe<Scalars['Int']['output']>;
  stage: Scalars['String']['output'];
  startUnix?: Maybe<Scalars['Int']['output']>;
  stopUnix?: Maybe<Scalars['Int']['output']>;
  timestamp: Scalars['Time']['output'];
};

export type ClipViewingUrls = {
  __typename?: 'ClipViewingUrls';
  dash?: Maybe<Scalars['String']['output']>;
  hls?: Maybe<Scalars['String']['output']>;
  mp4?: Maybe<Scalars['String']['output']>;
  webm?: Maybe<Scalars['String']['output']>;
};

export type Cluster = {
  __typename?: 'Cluster';
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  nodes: Array<Node>;
  region: Scalars['String']['output'];
  serviceInstances: Array<ServiceInstance>;
  status: NodeStatus;
};


export type ClusterServiceInstancesArgs = {
  nodeId?: InputMaybe<Scalars['String']['input']>;
  status?: InputMaybe<InstanceStatus>;
};

export type ClusterAccess = {
  __typename?: 'ClusterAccess';
  accessLevel: Scalars['String']['output'];
  clusterId: Scalars['String']['output'];
  clusterName: Scalars['String']['output'];
  resourceLimits?: Maybe<Scalars['JSON']['output']>;
};

export type ConnectionEvent = {
  __typename?: 'ConnectionEvent';
  city?: Maybe<Scalars['String']['output']>;
  connectionAddr: Scalars['String']['output'];
  connector: Scalars['String']['output'];
  countryCode?: Maybe<Scalars['String']['output']>;
  eventId: Scalars['String']['output'];
  eventType: Scalars['String']['output'];
  internalName: Scalars['String']['output'];
  latitude?: Maybe<Scalars['Float']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  nodeId: Scalars['String']['output'];
  sessionId: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export type ContentMetadata = {
  __typename?: 'ContentMetadata';
  clipSource?: Maybe<Scalars['String']['output']>;
  contentId: Scalars['String']['output'];
  contentType: Scalars['String']['output'];
  createdAt?: Maybe<Scalars['String']['output']>;
  description?: Maybe<Scalars['String']['output']>;
  duration?: Maybe<Scalars['Int']['output']>;
  isLive: Scalars['Boolean']['output'];
  recordingSize?: Maybe<Scalars['Int']['output']>;
  status: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
  title?: Maybe<Scalars['String']['output']>;
  viewCount?: Maybe<Scalars['Int']['output']>;
};

export type CountryMetric = {
  __typename?: 'CountryMetric';
  cities?: Maybe<Array<CityMetric>>;
  countryCode: Scalars['String']['output'];
  percentage: Scalars['Float']['output'];
  viewerCount: Scalars['Int']['output'];
};

export type CountryTimeSeries = {
  __typename?: 'CountryTimeSeries';
  countryCode: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  viewerCount: Scalars['Int']['output'];
};

export type CreateBootstrapTokenInput = {
  expiresIn?: InputMaybe<Scalars['Int']['input']>;
  name: Scalars['String']['input'];
  type: BootstrapTokenType;
  usageLimit?: InputMaybe<Scalars['Int']['input']>;
};

export type CreateClipInput = {
  description?: InputMaybe<Scalars['String']['input']>;
  endTime: Scalars['Int']['input'];
  startTime: Scalars['Int']['input'];
  stream: Scalars['String']['input'];
  title: Scalars['String']['input'];
};

export type CreateDeveloperTokenInput = {
  expiresIn?: InputMaybe<Scalars['Int']['input']>;
  name: Scalars['String']['input'];
  permissions?: InputMaybe<Scalars['String']['input']>;
};

export type CreatePaymentInput = {
  amount: Scalars['Money']['input'];
  currency?: InputMaybe<Scalars['Currency']['input']>;
  invoiceId: Scalars['ID']['input'];
  method: PaymentMethod;
  returnUrl?: InputMaybe<Scalars['String']['input']>;
};

export type CreateStreamInput = {
  description?: InputMaybe<Scalars['String']['input']>;
  name: Scalars['String']['input'];
  record?: InputMaybe<Scalars['Boolean']['input']>;
};

export type CreateStreamKeyInput = {
  name: Scalars['String']['input'];
};

export type DvrRequest = {
  __typename?: 'DVRRequest';
  createdAt: Scalars['Time']['output'];
  durationSeconds?: Maybe<Scalars['Int']['output']>;
  dvrHash: Scalars['ID']['output'];
  endedAt?: Maybe<Scalars['Time']['output']>;
  errorMessage?: Maybe<Scalars['String']['output']>;
  internalName: Scalars['String']['output'];
  manifestPath?: Maybe<Scalars['String']['output']>;
  sizeBytes?: Maybe<Scalars['Int']['output']>;
  startedAt?: Maybe<Scalars['Time']['output']>;
  status: Scalars['String']['output'];
  storageNodeId?: Maybe<Scalars['String']['output']>;
  updatedAt: Scalars['Time']['output'];
};

export type DvrRequestList = {
  __typename?: 'DVRRequestList';
  dvrRecordings: Array<DvrRequest>;
  limit: Scalars['Int']['output'];
  page: Scalars['Int']['output'];
  total: Scalars['Int']['output'];
};

export type DeveloperToken = {
  __typename?: 'DeveloperToken';
  createdAt: Scalars['Time']['output'];
  expiresAt?: Maybe<Scalars['Time']['output']>;
  id: Scalars['ID']['output'];
  lastUsedAt?: Maybe<Scalars['Time']['output']>;
  name: Scalars['String']['output'];
  permissions: Scalars['String']['output'];
  status: Scalars['String']['output'];
  token?: Maybe<Scalars['String']['output']>;
};

export type GeographicDistribution = {
  __typename?: 'GeographicDistribution';
  stream?: Maybe<Scalars['String']['output']>;
  timeRange: TimeRange;
  topCities: Array<CityMetric>;
  topCountries: Array<CountryMetric>;
  totalViewers: Scalars['Int']['output'];
  uniqueCities: Scalars['Int']['output'];
  uniqueCountries: Scalars['Int']['output'];
  viewersByCountry: Array<CountryTimeSeries>;
};

export enum InstanceStatus {
  Error = 'ERROR',
  Running = 'RUNNING',
  Starting = 'STARTING',
  Stopped = 'STOPPED',
  Stopping = 'STOPPING',
  Unknown = 'UNKNOWN'
}

export type Invoice = {
  __typename?: 'Invoice';
  amount: Scalars['Money']['output'];
  createdAt: Scalars['Time']['output'];
  currency: Scalars['Currency']['output'];
  dueDate: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  lineItems: Array<LineItem>;
  status: InvoiceStatus;
};

export enum InvoiceStatus {
  Cancelled = 'CANCELLED',
  Failed = 'FAILED',
  Paid = 'PAID',
  Pending = 'PENDING'
}

export type LineItem = {
  __typename?: 'LineItem';
  description: Scalars['String']['output'];
  quantity: Scalars['Int']['output'];
  total: Scalars['Money']['output'];
  unitPrice: Scalars['Money']['output'];
};

export type LoadBalancingMetric = {
  __typename?: 'LoadBalancingMetric';
  clientCountry?: Maybe<Scalars['String']['output']>;
  clientIp?: Maybe<Scalars['String']['output']>;
  clientLatitude?: Maybe<Scalars['Float']['output']>;
  clientLongitude?: Maybe<Scalars['Float']['output']>;
  details?: Maybe<Scalars['String']['output']>;
  eventType?: Maybe<Scalars['String']['output']>;
  nodeId?: Maybe<Scalars['String']['output']>;
  nodeLatitude?: Maybe<Scalars['Float']['output']>;
  nodeLongitude?: Maybe<Scalars['Float']['output']>;
  nodeName?: Maybe<Scalars['String']['output']>;
  routingDistance?: Maybe<Scalars['Float']['output']>;
  score?: Maybe<Scalars['Int']['output']>;
  selectedNode: Scalars['String']['output'];
  source?: Maybe<Scalars['String']['output']>;
  status: Scalars['String']['output'];
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export type Mutation = {
  __typename?: 'Mutation';
  createBootstrapToken: BootstrapToken;
  createClip: Clip;
  createDeveloperToken: DeveloperToken;
  createPayment: Payment;
  createStream: Stream;
  createStreamKey: StreamKey;
  deleteClip: Scalars['Boolean']['output'];
  deleteStream: Scalars['Boolean']['output'];
  deleteStreamKey: Scalars['Boolean']['output'];
  refreshStreamKey: Stream;
  revokeBootstrapToken: Scalars['Boolean']['output'];
  revokeDeveloperToken: Scalars['Boolean']['output'];
  setStreamRecordingConfig: RecordingConfig;
  startDVR: DvrRequest;
  stopDVR: Scalars['Boolean']['output'];
  updateStream: Stream;
  updateTenant: Tenant;
};


export type MutationCreateBootstrapTokenArgs = {
  input: CreateBootstrapTokenInput;
};


export type MutationCreateClipArgs = {
  input: CreateClipInput;
};


export type MutationCreateDeveloperTokenArgs = {
  input: CreateDeveloperTokenInput;
};


export type MutationCreatePaymentArgs = {
  input: CreatePaymentInput;
};


export type MutationCreateStreamArgs = {
  input: CreateStreamInput;
};


export type MutationCreateStreamKeyArgs = {
  input: CreateStreamKeyInput;
  streamId: Scalars['ID']['input'];
};


export type MutationDeleteClipArgs = {
  id: Scalars['ID']['input'];
};


export type MutationDeleteStreamArgs = {
  id: Scalars['ID']['input'];
};


export type MutationDeleteStreamKeyArgs = {
  keyId: Scalars['ID']['input'];
  streamId: Scalars['ID']['input'];
};


export type MutationRefreshStreamKeyArgs = {
  id: Scalars['ID']['input'];
};


export type MutationRevokeBootstrapTokenArgs = {
  id: Scalars['ID']['input'];
};


export type MutationRevokeDeveloperTokenArgs = {
  id: Scalars['ID']['input'];
};


export type MutationSetStreamRecordingConfigArgs = {
  enabled: Scalars['Boolean']['input'];
  format?: InputMaybe<Scalars['String']['input']>;
  internalName: Scalars['String']['input'];
  retentionDays?: InputMaybe<Scalars['Int']['input']>;
  segmentDuration?: InputMaybe<Scalars['Int']['input']>;
};


export type MutationStartDvrArgs = {
  internalName: Scalars['String']['input'];
  streamId?: InputMaybe<Scalars['ID']['input']>;
};


export type MutationStopDvrArgs = {
  dvrHash: Scalars['ID']['input'];
};


export type MutationUpdateStreamArgs = {
  id: Scalars['ID']['input'];
  input: UpdateStreamInput;
};


export type MutationUpdateTenantArgs = {
  input: UpdateTenantInput;
};

export type Node = {
  __typename?: 'Node';
  cluster: Scalars['String']['output'];
  clusterInfo?: Maybe<Cluster>;
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  ipAddress?: Maybe<Scalars['String']['output']>;
  lastSeen: Scalars['Time']['output'];
  latitude?: Maybe<Scalars['Float']['output']>;
  location?: Maybe<Scalars['String']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  metrics?: Maybe<Array<NodeMetric>>;
  metrics1h?: Maybe<Array<NodeMetricHourly>>;
  name: Scalars['String']['output'];
  region: Scalars['String']['output'];
  serviceInstances?: Maybe<Array<ServiceInstance>>;
  status: NodeStatus;
  type: Scalars['String']['output'];
};


export type NodeMetricsArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type NodeMetrics1hArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type NodeServiceInstancesArgs = {
  status?: InputMaybe<InstanceStatus>;
};

export type NodeMetric = {
  __typename?: 'NodeMetric';
  cpuUsage: Scalars['Float']['output'];
  diskUsage: Scalars['Float']['output'];
  healthScore: Scalars['Float']['output'];
  latitude?: Maybe<Scalars['Float']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  memoryUsage: Scalars['Float']['output'];
  metadata?: Maybe<Scalars['JSON']['output']>;
  networkRx: Scalars['Int']['output'];
  networkTx: Scalars['Int']['output'];
  nodeId: Scalars['String']['output'];
  status: Scalars['String']['output'];
  tags?: Maybe<Array<Scalars['String']['output']>>;
  timestamp: Scalars['Time']['output'];
};

export type NodeMetricHourly = {
  __typename?: 'NodeMetricHourly';
  avgCpu: Scalars['Float']['output'];
  avgHealthScore: Scalars['Float']['output'];
  avgMemory: Scalars['Float']['output'];
  nodeId: Scalars['String']['output'];
  peakCpu: Scalars['Float']['output'];
  peakMemory: Scalars['Float']['output'];
  timestamp: Scalars['Time']['output'];
  totalBandwidthIn: Scalars['Int']['output'];
  totalBandwidthOut: Scalars['Int']['output'];
  wasHealthy: Scalars['Boolean']['output'];
};

export enum NodeStatus {
  Degraded = 'DEGRADED',
  Healthy = 'HEALTHY',
  Unhealthy = 'UNHEALTHY'
}

export type PaginationInput = {
  limit?: InputMaybe<Scalars['Int']['input']>;
  offset?: InputMaybe<Scalars['Int']['input']>;
};

export type Payment = {
  __typename?: 'Payment';
  amount: Scalars['Money']['output'];
  createdAt: Scalars['Time']['output'];
  currency: Scalars['Currency']['output'];
  id: Scalars['ID']['output'];
  method: PaymentMethod;
  status: PaymentStatus;
};

export enum PaymentMethod {
  BankTransfer = 'BANK_TRANSFER',
  Card = 'CARD',
  Crypto = 'CRYPTO'
}

export enum PaymentStatus {
  Confirmed = 'CONFIRMED',
  Failed = 'FAILED',
  Pending = 'PENDING'
}

export type PlatformOverview = {
  __typename?: 'PlatformOverview';
  timeRange: TimeRange;
  totalBandwidth: Scalars['Float']['output'];
  totalStreams: Scalars['Int']['output'];
  totalUsers: Scalars['Int']['output'];
  totalViewers: Scalars['Int']['output'];
};

export type Query = {
  __typename?: 'Query';
  billingStatus: BillingStatus;
  billingTiers: Array<BillingTier>;
  bootstrapTokens: Array<BootstrapToken>;
  clip?: Maybe<Clip>;
  clipEvents: Array<ClipEvent>;
  clipViewingUrls: ClipViewingUrls;
  clips: Array<Clip>;
  cluster?: Maybe<Cluster>;
  clusters: Array<Cluster>;
  clustersAccess: Array<ClusterAccess>;
  clustersAvailable: Array<AvailableCluster>;
  connectionEvents: Array<ConnectionEvent>;
  currentStreamHealth?: Maybe<StreamHealthMetric>;
  developerTokens: Array<DeveloperToken>;
  discoverServices: Array<ServiceInstance>;
  dvrRequests: DvrRequestList;
  geographicDistribution: GeographicDistribution;
  invoice?: Maybe<Invoice>;
  invoices: Array<Invoice>;
  loadBalancingMetrics: Array<LoadBalancingMetric>;
  node?: Maybe<Node>;
  nodeMetrics: Array<NodeMetric>;
  nodeMetrics1h: Array<NodeMetricHourly>;
  nodes: Array<Node>;
  platformOverview: PlatformOverview;
  rebufferingEvents: Array<RebufferingEvent>;
  recordingConfig: RecordingConfig;
  recordings: Array<Recording>;
  resolveViewerEndpoint: ViewerEndpointResponse;
  routingEvents: Array<RoutingEvent>;
  serviceInstances: Array<ServiceInstance>;
  serviceInstancesHealth: Array<ServiceInstanceHealth>;
  stream?: Maybe<Stream>;
  streamAnalytics?: Maybe<StreamAnalytics>;
  streamHealthAlerts: Array<StreamHealthAlert>;
  streamHealthMetrics: Array<StreamHealthMetric>;
  streamKeys: Array<StreamKey>;
  streamMeta: StreamMetaResponse;
  streams: Array<Stream>;
  tenant?: Maybe<Tenant>;
  tenantClusterAssignments: Array<TenantClusterAssignment>;
  trackListEvents: Array<TrackListEvent>;
  usageRecords: Array<UsageRecord>;
  validateStreamKey: StreamValidation;
  viewerGeographics: Array<ViewerGeographic>;
  viewerMetrics: Array<ViewerMetric>;
  viewerMetrics5m: Array<ViewerMetrics5m>;
};


export type QueryClipArgs = {
  id: Scalars['ID']['input'];
};


export type QueryClipEventsArgs = {
  internalName?: InputMaybe<Scalars['String']['input']>;
  pagination?: InputMaybe<PaginationInput>;
  stage?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryClipViewingUrlsArgs = {
  clipId: Scalars['ID']['input'];
};


export type QueryClipsArgs = {
  streamId?: InputMaybe<Scalars['ID']['input']>;
};


export type QueryClusterArgs = {
  id: Scalars['ID']['input'];
};


export type QueryConnectionEventsArgs = {
  pagination?: InputMaybe<PaginationInput>;
  sortOrder?: InputMaybe<SortOrder>;
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryCurrentStreamHealthArgs = {
  stream: Scalars['String']['input'];
};


export type QueryDiscoverServicesArgs = {
  clusterId?: InputMaybe<Scalars['String']['input']>;
  type: Scalars['String']['input'];
};


export type QueryDvrRequestsArgs = {
  internalName?: InputMaybe<Scalars['String']['input']>;
  pagination?: InputMaybe<PaginationInput>;
  status?: InputMaybe<Scalars['String']['input']>;
};


export type QueryGeographicDistributionArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryInvoiceArgs = {
  id: Scalars['ID']['input'];
};


export type QueryLoadBalancingMetricsArgs = {
  pagination?: InputMaybe<PaginationInput>;
  sortOrder?: InputMaybe<SortOrder>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryNodeArgs = {
  id: Scalars['ID']['input'];
};


export type QueryNodeMetricsArgs = {
  nodeId?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryNodeMetrics1hArgs = {
  nodeId?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryNodesArgs = {
  clusterId?: InputMaybe<Scalars['String']['input']>;
  status?: InputMaybe<NodeStatus>;
  tag?: InputMaybe<Scalars['String']['input']>;
  type?: InputMaybe<Scalars['String']['input']>;
};


export type QueryPlatformOverviewArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryRebufferingEventsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryRecordingConfigArgs = {
  internalName: Scalars['String']['input'];
};


export type QueryRecordingsArgs = {
  streamId?: InputMaybe<Scalars['ID']['input']>;
};


export type QueryResolveViewerEndpointArgs = {
  contentId: Scalars['String']['input'];
  contentType: Scalars['String']['input'];
};


export type QueryRoutingEventsArgs = {
  pagination?: InputMaybe<PaginationInput>;
  sortOrder?: InputMaybe<SortOrder>;
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryServiceInstancesArgs = {
  clusterId?: InputMaybe<Scalars['String']['input']>;
  nodeId?: InputMaybe<Scalars['String']['input']>;
  status?: InputMaybe<InstanceStatus>;
};


export type QueryServiceInstancesHealthArgs = {
  serviceId?: InputMaybe<Scalars['String']['input']>;
};


export type QueryStreamArgs = {
  id: Scalars['ID']['input'];
};


export type QueryStreamAnalyticsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamHealthAlertsArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamHealthMetricsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamKeysArgs = {
  streamId: Scalars['ID']['input'];
};


export type QueryStreamMetaArgs = {
  includeRaw?: InputMaybe<Scalars['Boolean']['input']>;
  streamKey: Scalars['String']['input'];
  targetBaseUrl?: InputMaybe<Scalars['String']['input']>;
  targetNodeId?: InputMaybe<Scalars['String']['input']>;
};


export type QueryTrackListEventsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryUsageRecordsArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryValidateStreamKeyArgs = {
  streamKey: Scalars['String']['input'];
};


export type QueryViewerGeographicsArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryViewerMetricsArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryViewerMetrics5mArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};

export type RebufferingEvent = {
  __typename?: 'RebufferingEvent';
  bufferState: BufferState;
  frameJitterMs?: Maybe<Scalars['Float']['output']>;
  healthScore?: Maybe<Scalars['Float']['output']>;
  nodeId: Scalars['String']['output'];
  packetLossPercentage?: Maybe<Scalars['Float']['output']>;
  previousState: BufferState;
  rebufferEnd: Scalars['Boolean']['output'];
  rebufferStart: Scalars['Boolean']['output'];
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export type Recording = {
  __typename?: 'Recording';
  createdAt: Scalars['Time']['output'];
  duration?: Maybe<Scalars['Int']['output']>;
  endTime?: Maybe<Scalars['Time']['output']>;
  fileSizeBytes?: Maybe<Scalars['Int']['output']>;
  id: Scalars['ID']['output'];
  playbackId?: Maybe<Scalars['String']['output']>;
  startTime?: Maybe<Scalars['Time']['output']>;
  status: Scalars['String']['output'];
  streamId: Scalars['ID']['output'];
  thumbnailUrl?: Maybe<Scalars['String']['output']>;
  title?: Maybe<Scalars['String']['output']>;
  updatedAt: Scalars['Time']['output'];
};

export type RecordingConfig = {
  __typename?: 'RecordingConfig';
  enabled: Scalars['Boolean']['output'];
  format: Scalars['String']['output'];
  retentionDays: Scalars['Int']['output'];
  segmentDuration: Scalars['Int']['output'];
};

export type RoutingEvent = {
  __typename?: 'RoutingEvent';
  clientCountry?: Maybe<Scalars['String']['output']>;
  clientIp?: Maybe<Scalars['String']['output']>;
  clientLatitude?: Maybe<Scalars['Float']['output']>;
  clientLongitude?: Maybe<Scalars['Float']['output']>;
  details?: Maybe<Scalars['String']['output']>;
  nodeLatitude?: Maybe<Scalars['Float']['output']>;
  nodeLongitude?: Maybe<Scalars['Float']['output']>;
  nodeName?: Maybe<Scalars['String']['output']>;
  score?: Maybe<Scalars['Int']['output']>;
  selectedNode: Scalars['String']['output'];
  status: Scalars['String']['output'];
  streamName: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export type ServiceInstance = {
  __typename?: 'ServiceInstance';
  cluster?: Maybe<Cluster>;
  clusterId: Scalars['String']['output'];
  containerId?: Maybe<Scalars['String']['output']>;
  cpuUsagePercent?: Maybe<Scalars['Float']['output']>;
  healthStatus: NodeStatus;
  id: Scalars['ID']['output'];
  instanceId: Scalars['String']['output'];
  lastHealthCheck?: Maybe<Scalars['Time']['output']>;
  memoryUsageMb?: Maybe<Scalars['Int']['output']>;
  node?: Maybe<Node>;
  nodeId?: Maybe<Scalars['String']['output']>;
  port?: Maybe<Scalars['Int']['output']>;
  processId?: Maybe<Scalars['Int']['output']>;
  serviceId: Scalars['String']['output'];
  startedAt?: Maybe<Scalars['Time']['output']>;
  status: InstanceStatus;
  stoppedAt?: Maybe<Scalars['Time']['output']>;
  version?: Maybe<Scalars['String']['output']>;
};

export type ServiceInstanceHealth = {
  __typename?: 'ServiceInstanceHealth';
  clusterId: Scalars['String']['output'];
  healthEndpoint?: Maybe<Scalars['String']['output']>;
  host?: Maybe<Scalars['String']['output']>;
  instanceId: Scalars['String']['output'];
  lastHealthCheck?: Maybe<Scalars['Time']['output']>;
  port: Scalars['Int']['output'];
  protocol: Scalars['String']['output'];
  serviceId: Scalars['String']['output'];
  status: Scalars['String']['output'];
};

export enum SortOrder {
  Asc = 'ASC',
  Desc = 'DESC'
}

export type Stream = {
  __typename?: 'Stream';
  createdAt: Scalars['Time']['output'];
  description?: Maybe<Scalars['String']['output']>;
  events: Array<StreamEvent>;
  health: Array<StreamHealthMetric>;
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  playbackId: Scalars['String']['output'];
  record: Scalars['Boolean']['output'];
  recordings: Array<Recording>;
  status: StreamStatus;
  streamKey: Scalars['String']['output'];
  updatedAt: Scalars['Time']['output'];
  viewerMetrics5m: Array<ViewerMetrics5m>;
};


export type StreamEventsArgs = {
  pagination?: InputMaybe<PaginationInput>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type StreamHealthArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type StreamViewerMetrics5mArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};

export type StreamAnalytics = {
  __typename?: 'StreamAnalytics';
  avgBitrate?: Maybe<Scalars['Int']['output']>;
  avgBufferHealth?: Maybe<Scalars['Float']['output']>;
  avgViewers?: Maybe<Scalars['Float']['output']>;
  bandwidthIn: Scalars['Float']['output'];
  bandwidthOut: Scalars['Float']['output'];
  bitrateKbps?: Maybe<Scalars['Int']['output']>;
  createdAt: Scalars['Time']['output'];
  currentBufferState?: Maybe<Scalars['String']['output']>;
  currentCodec?: Maybe<Scalars['String']['output']>;
  currentFps?: Maybe<Scalars['Float']['output']>;
  currentHealthScore?: Maybe<Scalars['Float']['output']>;
  currentIssues?: Maybe<Scalars['String']['output']>;
  currentResolution?: Maybe<Scalars['String']['output']>;
  currentViewers: Scalars['Int']['output'];
  downbytes: Scalars['Float']['output'];
  firstMs?: Maybe<Scalars['Int']['output']>;
  id: Scalars['ID']['output'];
  inputs: Scalars['Int']['output'];
  internalName: Scalars['String']['output'];
  lastMs?: Maybe<Scalars['Int']['output']>;
  lastUpdated: Scalars['Time']['output'];
  latitude?: Maybe<Scalars['Float']['output']>;
  location?: Maybe<Scalars['String']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  mistStatus?: Maybe<Scalars['String']['output']>;
  nodeId?: Maybe<Scalars['String']['output']>;
  nodeName?: Maybe<Scalars['String']['output']>;
  outputs: Scalars['Int']['output'];
  packetLossRate?: Maybe<Scalars['Float']['output']>;
  packetsLost: Scalars['Float']['output'];
  packetsRetrans: Scalars['Float']['output'];
  packetsSent: Scalars['Float']['output'];
  peakViewers: Scalars['Int']['output'];
  qualityTier?: Maybe<Scalars['String']['output']>;
  resolution?: Maybe<Scalars['String']['output']>;
  sessionEndTime?: Maybe<Scalars['Time']['output']>;
  sessionStartTime?: Maybe<Scalars['Time']['output']>;
  status?: Maybe<Scalars['String']['output']>;
  streamId?: Maybe<Scalars['ID']['output']>;
  tenantId: Scalars['ID']['output'];
  totalBandwidthGb: Scalars['Float']['output'];
  totalConnections: Scalars['Int']['output'];
  totalSessionDuration: Scalars['Int']['output'];
  trackCount: Scalars['Int']['output'];
  uniqueCities?: Maybe<Scalars['Int']['output']>;
  uniqueCountries?: Maybe<Scalars['Int']['output']>;
  upbytes: Scalars['Float']['output'];
};

export type StreamEvent = {
  __typename?: 'StreamEvent';
  details?: Maybe<Scalars['JSON']['output']>;
  status: StreamStatus;
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  type: StreamEventType;
};

export enum StreamEventType {
  BufferUpdate = 'BUFFER_UPDATE',
  StreamEnd = 'STREAM_END',
  StreamError = 'STREAM_ERROR',
  StreamStart = 'STREAM_START',
  TrackListUpdate = 'TRACK_LIST_UPDATE'
}

export type StreamHealthAlert = {
  __typename?: 'StreamHealthAlert';
  alertType: AlertType;
  bufferState?: Maybe<BufferState>;
  frameJitterMs?: Maybe<Scalars['Float']['output']>;
  healthScore?: Maybe<Scalars['Float']['output']>;
  issuesDescription?: Maybe<Scalars['String']['output']>;
  nodeId: Scalars['String']['output'];
  packetLossPercentage?: Maybe<Scalars['Float']['output']>;
  qualityTier?: Maybe<Scalars['String']['output']>;
  severity: AlertSeverity;
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export type StreamHealthMetric = {
  __typename?: 'StreamHealthMetric';
  audioBitrate?: Maybe<Scalars['Int']['output']>;
  audioChannels?: Maybe<Scalars['Int']['output']>;
  audioCodec?: Maybe<Scalars['String']['output']>;
  audioSampleRate?: Maybe<Scalars['Int']['output']>;
  bitrate?: Maybe<Scalars['Int']['output']>;
  bufferHealth?: Maybe<Scalars['Float']['output']>;
  bufferState: BufferState;
  codec?: Maybe<Scalars['String']['output']>;
  fps?: Maybe<Scalars['Float']['output']>;
  frameJitterMs?: Maybe<Scalars['Float']['output']>;
  hasIssues: Scalars['Boolean']['output'];
  healthScore: Scalars['Float']['output'];
  height?: Maybe<Scalars['Int']['output']>;
  issuesDescription?: Maybe<Scalars['String']['output']>;
  keyframeStabilityMs?: Maybe<Scalars['Float']['output']>;
  nodeId: Scalars['String']['output'];
  packetLossPercentage?: Maybe<Scalars['Float']['output']>;
  packetsLost?: Maybe<Scalars['Int']['output']>;
  packetsSent?: Maybe<Scalars['Int']['output']>;
  qualityTier?: Maybe<Scalars['String']['output']>;
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  trackMetadata?: Maybe<Scalars['JSON']['output']>;
  width?: Maybe<Scalars['Int']['output']>;
};

export type StreamKey = {
  __typename?: 'StreamKey';
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  isActive: Scalars['Boolean']['output'];
  keyName?: Maybe<Scalars['String']['output']>;
  keyValue: Scalars['String']['output'];
  lastUsedAt?: Maybe<Scalars['Time']['output']>;
  streamId: Scalars['ID']['output'];
};

export type StreamMetaResponse = {
  __typename?: 'StreamMetaResponse';
  metaSummary: StreamMetaSummary;
  raw?: Maybe<Scalars['JSON']['output']>;
};

export type StreamMetaSummary = {
  __typename?: 'StreamMetaSummary';
  bufferWindowMs: Scalars['Int']['output'];
  height?: Maybe<Scalars['Int']['output']>;
  isLive: Scalars['Boolean']['output'];
  jitterMs: Scalars['Int']['output'];
  lastMs?: Maybe<Scalars['Int']['output']>;
  nowMs?: Maybe<Scalars['Int']['output']>;
  tracks: Array<StreamMetaTrack>;
  type?: Maybe<Scalars['String']['output']>;
  unixOffsetMs: Scalars['Int']['output'];
  version?: Maybe<Scalars['Int']['output']>;
  width?: Maybe<Scalars['Int']['output']>;
};

export type StreamMetaTrack = {
  __typename?: 'StreamMetaTrack';
  bitrateBps?: Maybe<Scalars['Int']['output']>;
  channels?: Maybe<Scalars['Int']['output']>;
  codec: Scalars['String']['output'];
  firstMs?: Maybe<Scalars['Int']['output']>;
  height?: Maybe<Scalars['Int']['output']>;
  id: Scalars['String']['output'];
  lastMs?: Maybe<Scalars['Int']['output']>;
  nowMs?: Maybe<Scalars['Int']['output']>;
  rate?: Maybe<Scalars['Int']['output']>;
  type: Scalars['String']['output'];
  width?: Maybe<Scalars['Int']['output']>;
};

export enum StreamStatus {
  Ended = 'ENDED',
  Live = 'LIVE',
  Offline = 'OFFLINE',
  Recording = 'RECORDING'
}

export type StreamTrack = {
  __typename?: 'StreamTrack';
  bitrateBps?: Maybe<Scalars['Int']['output']>;
  bitrateKbps?: Maybe<Scalars['Int']['output']>;
  buffer?: Maybe<Scalars['Int']['output']>;
  channels?: Maybe<Scalars['Int']['output']>;
  codec?: Maybe<Scalars['String']['output']>;
  fps?: Maybe<Scalars['Float']['output']>;
  hasBFrames?: Maybe<Scalars['Boolean']['output']>;
  height?: Maybe<Scalars['Int']['output']>;
  jitter?: Maybe<Scalars['Int']['output']>;
  resolution?: Maybe<Scalars['String']['output']>;
  sampleRate?: Maybe<Scalars['Int']['output']>;
  trackName: Scalars['String']['output'];
  trackType: Scalars['String']['output'];
  width?: Maybe<Scalars['Int']['output']>;
};

export type StreamValidation = {
  __typename?: 'StreamValidation';
  error?: Maybe<Scalars['String']['output']>;
  streamKey: Scalars['String']['output'];
  valid: Scalars['Boolean']['output'];
};

export type Subscription = {
  __typename?: 'Subscription';
  clipLifecycle: ClipEvent;
  dvrLifecycle: ClipEvent;
  streamEvents: StreamEvent;
  systemHealth: SystemHealthEvent;
  trackListUpdates: TrackListEvent;
  viewerMetrics: ViewerMetrics;
};


export type SubscriptionClipLifecycleArgs = {
  stream: Scalars['String']['input'];
};


export type SubscriptionDvrLifecycleArgs = {
  stream: Scalars['String']['input'];
};


export type SubscriptionStreamEventsArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
};


export type SubscriptionTrackListUpdatesArgs = {
  stream: Scalars['String']['input'];
};


export type SubscriptionViewerMetricsArgs = {
  stream: Scalars['String']['input'];
};

export type SystemHealthEvent = {
  __typename?: 'SystemHealthEvent';
  cluster: Scalars['String']['output'];
  cpuUsage: Scalars['Float']['output'];
  diskUsage: Scalars['Float']['output'];
  healthScore: Scalars['Float']['output'];
  memoryUsage: Scalars['Float']['output'];
  node: Scalars['String']['output'];
  status: NodeStatus;
  timestamp: Scalars['Time']['output'];
};

export type Tenant = {
  __typename?: 'Tenant';
  cluster?: Maybe<Scalars['String']['output']>;
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  settings?: Maybe<Scalars['JSON']['output']>;
};

export type TenantClusterAssignment = {
  __typename?: 'TenantClusterAssignment';
  clusterId: Scalars['String']['output'];
  createdAt: Scalars['Time']['output'];
  deploymentTier?: Maybe<Scalars['String']['output']>;
  fallbackWhenFull: Scalars['Boolean']['output'];
  id: Scalars['ID']['output'];
  isActive: Scalars['Boolean']['output'];
  isPrimary: Scalars['Boolean']['output'];
  maxBandwidthMbpsOnCluster?: Maybe<Scalars['Int']['output']>;
  maxStreamsOnCluster?: Maybe<Scalars['Int']['output']>;
  maxViewersOnCluster?: Maybe<Scalars['Int']['output']>;
  priority: Scalars['Int']['output'];
  tenantId: Scalars['ID']['output'];
  updatedAt: Scalars['Time']['output'];
};

export type TimeRange = {
  __typename?: 'TimeRange';
  end: Scalars['Time']['output'];
  start: Scalars['Time']['output'];
};

export type TimeRangeInput = {
  end: Scalars['Time']['input'];
  start: Scalars['Time']['input'];
};

export type TrackListEvent = {
  __typename?: 'TrackListEvent';
  nodeId?: Maybe<Scalars['String']['output']>;
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  trackCount: Scalars['Int']['output'];
  trackList: Scalars['String']['output'];
  tracks?: Maybe<Array<StreamTrack>>;
};

export type UpdateStreamInput = {
  description?: InputMaybe<Scalars['String']['input']>;
  name?: InputMaybe<Scalars['String']['input']>;
  record?: InputMaybe<Scalars['Boolean']['input']>;
};

export type UpdateTenantInput = {
  name?: InputMaybe<Scalars['String']['input']>;
  settings?: InputMaybe<Scalars['JSON']['input']>;
};

export type UsageRecord = {
  __typename?: 'UsageRecord';
  cost: Scalars['Float']['output'];
  id: Scalars['ID']['output'];
  quantity: Scalars['Float']['output'];
  resourceType: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  unit: Scalars['String']['output'];
};

export type User = {
  __typename?: 'User';
  createdAt: Scalars['Time']['output'];
  email: Scalars['String']['output'];
  id: Scalars['ID']['output'];
  name?: Maybe<Scalars['String']['output']>;
  role: Scalars['String']['output'];
};

export type ViewerEndpoint = {
  __typename?: 'ViewerEndpoint';
  baseUrl: Scalars['String']['output'];
  geoDistance?: Maybe<Scalars['Float']['output']>;
  healthScore?: Maybe<Scalars['Float']['output']>;
  loadScore?: Maybe<Scalars['Float']['output']>;
  nodeId: Scalars['String']['output'];
  outputs?: Maybe<Scalars['JSON']['output']>;
  protocol: Scalars['String']['output'];
  url: Scalars['String']['output'];
};

export type ViewerEndpointResponse = {
  __typename?: 'ViewerEndpointResponse';
  endpoints: Array<ViewerEndpoint>;
  metadata?: Maybe<ContentMetadata>;
};

export type ViewerGeographic = {
  __typename?: 'ViewerGeographic';
  city?: Maybe<Scalars['String']['output']>;
  connectionAddr?: Maybe<Scalars['String']['output']>;
  countryCode?: Maybe<Scalars['String']['output']>;
  eventType?: Maybe<Scalars['String']['output']>;
  latitude?: Maybe<Scalars['Float']['output']>;
  longitude?: Maybe<Scalars['Float']['output']>;
  nodeId?: Maybe<Scalars['String']['output']>;
  source?: Maybe<Scalars['String']['output']>;
  stream?: Maybe<Scalars['String']['output']>;
  timestamp: Scalars['Time']['output'];
  viewerCount?: Maybe<Scalars['Int']['output']>;
};

export type ViewerMetric = {
  __typename?: 'ViewerMetric';
  timestamp: Scalars['Time']['output'];
  viewerCount: Scalars['Int']['output'];
};

export type ViewerMetrics = {
  __typename?: 'ViewerMetrics';
  bandwidth: Scalars['Float']['output'];
  bufferHealth?: Maybe<Scalars['Float']['output']>;
  connectionQuality?: Maybe<Scalars['Float']['output']>;
  currentViewers: Scalars['Int']['output'];
  peakViewers: Scalars['Int']['output'];
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  viewerCount: Scalars['Int']['output'];
};

export type ViewerMetrics5m = {
  __typename?: 'ViewerMetrics5m';
  avgBufferHealth: Scalars['Float']['output'];
  avgConnectionQuality: Scalars['Float']['output'];
  avgViewers: Scalars['Float']['output'];
  internalName: Scalars['String']['output'];
  nodeId: Scalars['String']['output'];
  peakViewers: Scalars['Int']['output'];
  timestamp: Scalars['Time']['output'];
  uniqueCities: Scalars['Int']['output'];
  uniqueCountries: Scalars['Int']['output'];
};

/**
 * A Directive provides a way to describe alternate runtime execution and type validation behavior in a GraphQL document.
 *
 * In some cases, you need to provide options to alter GraphQL's execution behavior in ways field arguments will not suffice, such as conditionally including or skipping a field. Directives provide this by describing additional information to the executor.
 */
export type __Directive = {
  __typename?: '__Directive';
  name: Scalars['String']['output'];
  description?: Maybe<Scalars['String']['output']>;
  isRepeatable: Scalars['Boolean']['output'];
  locations: Array<__DirectiveLocation>;
  args: Array<__InputValue>;
};


/**
 * A Directive provides a way to describe alternate runtime execution and type validation behavior in a GraphQL document.
 *
 * In some cases, you need to provide options to alter GraphQL's execution behavior in ways field arguments will not suffice, such as conditionally including or skipping a field. Directives provide this by describing additional information to the executor.
 */
export type __DirectiveArgsArgs = {
  includeDeprecated?: InputMaybe<Scalars['Boolean']['input']>;
};

/** A Directive can be adjacent to many parts of the GraphQL language, a __DirectiveLocation describes one such possible adjacencies. */
export enum __DirectiveLocation {
  /** Location adjacent to a query operation. */
  Query = 'QUERY',
  /** Location adjacent to a mutation operation. */
  Mutation = 'MUTATION',
  /** Location adjacent to a subscription operation. */
  Subscription = 'SUBSCRIPTION',
  /** Location adjacent to a field. */
  Field = 'FIELD',
  /** Location adjacent to a fragment definition. */
  FragmentDefinition = 'FRAGMENT_DEFINITION',
  /** Location adjacent to a fragment spread. */
  FragmentSpread = 'FRAGMENT_SPREAD',
  /** Location adjacent to an inline fragment. */
  InlineFragment = 'INLINE_FRAGMENT',
  /** Location adjacent to a variable definition. */
  VariableDefinition = 'VARIABLE_DEFINITION',
  /** Location adjacent to a schema definition. */
  Schema = 'SCHEMA',
  /** Location adjacent to a scalar definition. */
  Scalar = 'SCALAR',
  /** Location adjacent to an object type definition. */
  Object = 'OBJECT',
  /** Location adjacent to a field definition. */
  FieldDefinition = 'FIELD_DEFINITION',
  /** Location adjacent to an argument definition. */
  ArgumentDefinition = 'ARGUMENT_DEFINITION',
  /** Location adjacent to an interface definition. */
  Interface = 'INTERFACE',
  /** Location adjacent to a union definition. */
  Union = 'UNION',
  /** Location adjacent to an enum definition. */
  Enum = 'ENUM',
  /** Location adjacent to an enum value definition. */
  EnumValue = 'ENUM_VALUE',
  /** Location adjacent to an input object type definition. */
  InputObject = 'INPUT_OBJECT',
  /** Location adjacent to an input object field definition. */
  InputFieldDefinition = 'INPUT_FIELD_DEFINITION'
}

/** One possible value for a given Enum. Enum values are unique values, not a placeholder for a string or numeric value. However an Enum value is returned in a JSON response as a string. */
export type __EnumValue = {
  __typename?: '__EnumValue';
  name: Scalars['String']['output'];
  description?: Maybe<Scalars['String']['output']>;
  isDeprecated: Scalars['Boolean']['output'];
  deprecationReason?: Maybe<Scalars['String']['output']>;
};

/** Object and Interface types are described by a list of Fields, each of which has a name, potentially a list of arguments, and a return type. */
export type __Field = {
  __typename?: '__Field';
  name: Scalars['String']['output'];
  description?: Maybe<Scalars['String']['output']>;
  args: Array<__InputValue>;
  type: __Type;
  isDeprecated: Scalars['Boolean']['output'];
  deprecationReason?: Maybe<Scalars['String']['output']>;
};


/** Object and Interface types are described by a list of Fields, each of which has a name, potentially a list of arguments, and a return type. */
export type __FieldArgsArgs = {
  includeDeprecated?: InputMaybe<Scalars['Boolean']['input']>;
};

/** Arguments provided to Fields or Directives and the input fields of an InputObject are represented as Input Values which describe their type and optionally a default value. */
export type __InputValue = {
  __typename?: '__InputValue';
  name: Scalars['String']['output'];
  description?: Maybe<Scalars['String']['output']>;
  type: __Type;
  /** A GraphQL-formatted string representing the default value for this input value. */
  defaultValue?: Maybe<Scalars['String']['output']>;
  isDeprecated: Scalars['Boolean']['output'];
  deprecationReason?: Maybe<Scalars['String']['output']>;
};

/** A GraphQL Schema defines the capabilities of a GraphQL server. It exposes all available types and directives on the server, as well as the entry points for query, mutation, and subscription operations. */
export type __Schema = {
  __typename?: '__Schema';
  description?: Maybe<Scalars['String']['output']>;
  /** A list of all types supported by this server. */
  types: Array<__Type>;
  /** The type that query operations will be rooted at. */
  queryType: __Type;
  /** If this server supports mutation, the type that mutation operations will be rooted at. */
  mutationType?: Maybe<__Type>;
  /** If this server support subscription, the type that subscription operations will be rooted at. */
  subscriptionType?: Maybe<__Type>;
  /** A list of all directives supported by this server. */
  directives: Array<__Directive>;
};

/**
 * The fundamental unit of any GraphQL Schema is the type. There are many kinds of types in GraphQL as represented by the `__TypeKind` enum.
 *
 * Depending on the kind of a type, certain fields describe information about that type. Scalar types provide no information beyond a name, description and optional `specifiedByURL`, while Enum types provide their values. Object and Interface types provide the fields they describe. Abstract types, Union and Interface, provide the Object types possible at runtime. List and NonNull types compose other types.
 */
export type __Type = {
  __typename?: '__Type';
  kind: __TypeKind;
  name?: Maybe<Scalars['String']['output']>;
  description?: Maybe<Scalars['String']['output']>;
  specifiedByURL?: Maybe<Scalars['String']['output']>;
  fields?: Maybe<Array<__Field>>;
  interfaces?: Maybe<Array<__Type>>;
  possibleTypes?: Maybe<Array<__Type>>;
  enumValues?: Maybe<Array<__EnumValue>>;
  inputFields?: Maybe<Array<__InputValue>>;
  ofType?: Maybe<__Type>;
  isOneOf?: Maybe<Scalars['Boolean']['output']>;
};


/**
 * The fundamental unit of any GraphQL Schema is the type. There are many kinds of types in GraphQL as represented by the `__TypeKind` enum.
 *
 * Depending on the kind of a type, certain fields describe information about that type. Scalar types provide no information beyond a name, description and optional `specifiedByURL`, while Enum types provide their values. Object and Interface types provide the fields they describe. Abstract types, Union and Interface, provide the Object types possible at runtime. List and NonNull types compose other types.
 */
export type __TypeFieldsArgs = {
  includeDeprecated?: InputMaybe<Scalars['Boolean']['input']>;
};


/**
 * The fundamental unit of any GraphQL Schema is the type. There are many kinds of types in GraphQL as represented by the `__TypeKind` enum.
 *
 * Depending on the kind of a type, certain fields describe information about that type. Scalar types provide no information beyond a name, description and optional `specifiedByURL`, while Enum types provide their values. Object and Interface types provide the fields they describe. Abstract types, Union and Interface, provide the Object types possible at runtime. List and NonNull types compose other types.
 */
export type __TypeEnumValuesArgs = {
  includeDeprecated?: InputMaybe<Scalars['Boolean']['input']>;
};


/**
 * The fundamental unit of any GraphQL Schema is the type. There are many kinds of types in GraphQL as represented by the `__TypeKind` enum.
 *
 * Depending on the kind of a type, certain fields describe information about that type. Scalar types provide no information beyond a name, description and optional `specifiedByURL`, while Enum types provide their values. Object and Interface types provide the fields they describe. Abstract types, Union and Interface, provide the Object types possible at runtime. List and NonNull types compose other types.
 */
export type __TypeInputFieldsArgs = {
  includeDeprecated?: InputMaybe<Scalars['Boolean']['input']>;
};

/** An enum describing what kind of type a given `__Type` is. */
export enum __TypeKind {
  /** Indicates this type is a scalar. */
  Scalar = 'SCALAR',
  /** Indicates this type is an object. `fields` and `interfaces` are valid fields. */
  Object = 'OBJECT',
  /** Indicates this type is an interface. `fields`, `interfaces`, and `possibleTypes` are valid fields. */
  Interface = 'INTERFACE',
  /** Indicates this type is a union. `possibleTypes` is a valid field. */
  Union = 'UNION',
  /** Indicates this type is an enum. `enumValues` is a valid field. */
  Enum = 'ENUM',
  /** Indicates this type is an input object. `inputFields` is a valid field. */
  InputObject = 'INPUT_OBJECT',
  /** Indicates this type is a list. `ofType` is a valid field. */
  List = 'LIST',
  /** Indicates this type is a non-null. `ofType` is a valid field. */
  NonNull = 'NON_NULL'
}

export type CreatePaymentMutationVariables = Exact<{
  input: CreatePaymentInput;
}>;


export type CreatePaymentMutation = { __typename?: 'Mutation', createPayment: { __typename?: 'Payment', id: string, amount: any, currency: any, method: PaymentMethod, status: PaymentStatus, createdAt: string } };

export type CreateClipMutationVariables = Exact<{
  input: CreateClipInput;
}>;


export type CreateClipMutation = { __typename?: 'Mutation', createClip: { __typename?: 'Clip', id: string, stream: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string } };

export type DeleteClipMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type DeleteClipMutation = { __typename?: 'Mutation', deleteClip: boolean };

export type CreateApiTokenMutationVariables = Exact<{
  input: CreateDeveloperTokenInput;
}>;


export type CreateApiTokenMutation = { __typename?: 'Mutation', createDeveloperToken: { __typename?: 'DeveloperToken', id: string, name: string, token?: string | null | undefined, permissions: string, expiresAt?: string | null | undefined, createdAt: string } };

export type RevokeApiTokenMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type RevokeApiTokenMutation = { __typename?: 'Mutation', revokeDeveloperToken: boolean };

export type StartDvrMutationVariables = Exact<{
  internalName: Scalars['String']['input'];
  streamId?: InputMaybe<Scalars['ID']['input']>;
}>;


export type StartDvrMutation = { __typename?: 'Mutation', startDVR: { __typename?: 'DVRRequest', dvrHash: string, internalName: string, storageNodeId?: string | null | undefined, status: string, startedAt?: string | null | undefined, endedAt?: string | null | undefined, durationSeconds?: number | null | undefined, sizeBytes?: number | null | undefined, manifestPath?: string | null | undefined, errorMessage?: string | null | undefined, createdAt: string, updatedAt: string } };

export type StopDvrMutationVariables = Exact<{
  dvrHash: Scalars['ID']['input'];
}>;


export type StopDvrMutation = { __typename?: 'Mutation', stopDVR: boolean };

export type SetStreamRecordingConfigMutationVariables = Exact<{
  internalName: Scalars['String']['input'];
  enabled: Scalars['Boolean']['input'];
  retentionDays?: InputMaybe<Scalars['Int']['input']>;
  format?: InputMaybe<Scalars['String']['input']>;
  segmentDuration?: InputMaybe<Scalars['Int']['input']>;
}>;


export type SetStreamRecordingConfigMutation = { __typename?: 'Mutation', setStreamRecordingConfig: { __typename?: 'RecordingConfig', enabled: boolean, retentionDays: number, format: string, segmentDuration: number } };

export type UpdateTenantMutationVariables = Exact<{
  input: UpdateTenantInput;
}>;


export type UpdateTenantMutation = { __typename?: 'Mutation', updateTenant: { __typename?: 'Tenant', id: string, name: string, settings?: any | null | undefined, cluster?: string | null | undefined, createdAt: string } };

export type CreateStreamMutationVariables = Exact<{
  input: CreateStreamInput;
}>;


export type CreateStreamMutation = { __typename?: 'Mutation', createStream: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string } };

export type UpdateStreamMutationVariables = Exact<{
  id: Scalars['ID']['input'];
  input: UpdateStreamInput;
}>;


export type UpdateStreamMutation = { __typename?: 'Mutation', updateStream: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string } };

export type DeleteStreamMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type DeleteStreamMutation = { __typename?: 'Mutation', deleteStream: boolean };

export type RefreshStreamKeyMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type RefreshStreamKeyMutation = { __typename?: 'Mutation', refreshStreamKey: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string } };

export type CreateStreamKeyMutationVariables = Exact<{
  streamId: Scalars['ID']['input'];
  input: CreateStreamKeyInput;
}>;


export type CreateStreamKeyMutation = { __typename?: 'Mutation', createStreamKey: { __typename?: 'StreamKey', id: string, streamId: string, keyValue: string, keyName?: string | null | undefined, isActive: boolean, lastUsedAt?: string | null | undefined, createdAt: string } };

export type DeleteStreamKeyMutationVariables = Exact<{
  streamId: Scalars['ID']['input'];
  keyId: Scalars['ID']['input'];
}>;


export type DeleteStreamKeyMutation = { __typename?: 'Mutation', deleteStreamKey: boolean };

export type GetStreamAnalyticsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamAnalyticsQuery = { __typename?: 'Query', streamAnalytics?: { __typename?: 'StreamAnalytics', id: string, tenantId: string, streamId?: string | null | undefined, internalName: string, sessionStartTime?: string | null | undefined, sessionEndTime?: string | null | undefined, totalSessionDuration: number, currentViewers: number, peakViewers: number, totalConnections: number, bandwidthIn: number, bandwidthOut: number, totalBandwidthGb: number, upbytes: number, downbytes: number, bitrateKbps?: number | null | undefined, resolution?: string | null | undefined, packetsSent: number, packetsLost: number, packetsRetrans: number, firstMs?: number | null | undefined, lastMs?: number | null | undefined, trackCount: number, inputs: number, outputs: number, nodeId?: string | null | undefined, nodeName?: string | null | undefined, latitude?: number | null | undefined, longitude?: number | null | undefined, location?: string | null | undefined, status?: string | null | undefined, lastUpdated: string, createdAt: string, currentHealthScore?: number | null | undefined, currentBufferState?: string | null | undefined, currentIssues?: string | null | undefined, currentCodec?: string | null | undefined, currentFps?: number | null | undefined, currentResolution?: string | null | undefined, mistStatus?: string | null | undefined, qualityTier?: string | null | undefined, avgViewers?: number | null | undefined, uniqueCountries?: number | null | undefined, uniqueCities?: number | null | undefined, avgBufferHealth?: number | null | undefined, avgBitrate?: number | null | undefined, packetLossRate?: number | null | undefined } | null | undefined };

export type GetViewerMetricsQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetViewerMetricsQuery = { __typename?: 'Query', viewerMetrics: Array<{ __typename?: 'ViewerMetric', timestamp: string, viewerCount: number }> };

export type GetPlatformOverviewQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetPlatformOverviewQuery = { __typename?: 'Query', platformOverview: { __typename?: 'PlatformOverview', totalStreams: number, totalViewers: number, totalBandwidth: number, totalUsers: number, timeRange: { __typename?: 'TimeRange', start: string, end: string } } };

export type GetUsageRecordsQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetUsageRecordsQuery = { __typename?: 'Query', usageRecords: Array<{ __typename?: 'UsageRecord', id: string, resourceType: string, quantity: number, unit: string, cost: number, timestamp: string }> };

export type GetViewerGeographicsQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetViewerGeographicsQuery = { __typename?: 'Query', viewerGeographics: Array<{ __typename?: 'ViewerGeographic', timestamp: string, stream?: string | null | undefined, nodeId?: string | null | undefined, countryCode?: string | null | undefined, city?: string | null | undefined, latitude?: number | null | undefined, longitude?: number | null | undefined, viewerCount?: number | null | undefined, connectionAddr?: string | null | undefined, eventType?: string | null | undefined, source?: string | null | undefined }> };

export type GetGeographicDistributionQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetGeographicDistributionQuery = { __typename?: 'Query', geographicDistribution: { __typename?: 'GeographicDistribution', stream?: string | null | undefined, uniqueCountries: number, uniqueCities: number, totalViewers: number, timeRange: { __typename?: 'TimeRange', start: string, end: string }, topCountries: Array<{ __typename?: 'CountryMetric', countryCode: string, viewerCount: number, percentage: number, cities?: Array<{ __typename?: 'CityMetric', city: string, countryCode?: string | null | undefined, viewerCount: number, percentage: number, latitude?: number | null | undefined, longitude?: number | null | undefined }> | null | undefined }>, topCities: Array<{ __typename?: 'CityMetric', city: string, countryCode?: string | null | undefined, viewerCount: number, percentage: number, latitude?: number | null | undefined, longitude?: number | null | undefined }>, viewersByCountry: Array<{ __typename?: 'CountryTimeSeries', timestamp: string, countryCode: string, viewerCount: number }> } };

export type GetLoadBalancingMetricsQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetLoadBalancingMetricsQuery = { __typename?: 'Query', loadBalancingMetrics: Array<{ __typename?: 'LoadBalancingMetric', timestamp: string, stream: string, selectedNode: string, nodeId?: string | null | undefined, clientIp?: string | null | undefined, clientCountry?: string | null | undefined, clientLatitude?: number | null | undefined, clientLongitude?: number | null | undefined, nodeLatitude?: number | null | undefined, nodeLongitude?: number | null | undefined, score?: number | null | undefined, status: string, details?: string | null | undefined, routingDistance?: number | null | undefined, eventType?: string | null | undefined, source?: string | null | undefined }> };

export type AuthPlaceholderQueryVariables = Exact<{ [key: string]: never; }>;


export type AuthPlaceholderQuery = { __typename: 'Query' };

export type GetBillingTiersQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingTiersQuery = { __typename?: 'Query', billingTiers: Array<{ __typename?: 'BillingTier', id: string, name: string, description?: string | null | undefined, price: number, currency: string, features: Array<string> }> };

export type GetBillingStatusQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingStatusQuery = { __typename?: 'Query', billingStatus: { __typename?: 'BillingStatus', nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

export type GetInvoicesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetInvoicesQuery = { __typename?: 'Query', invoices: Array<{ __typename?: 'Invoice', id: string, amount: any, currency: any, status: InvoiceStatus, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: any, total: any }> }> };

export type GetInvoiceQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetInvoiceQuery = { __typename?: 'Query', invoice?: { __typename?: 'Invoice', id: string, amount: any, currency: any, status: InvoiceStatus, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: any, total: any }> } | null | undefined };

export type ClipInfoFragment = { __typename?: 'Clip', id: string, stream: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string };

export type GetClipsQueryVariables = Exact<{
  streamId?: InputMaybe<Scalars['ID']['input']>;
}>;


export type GetClipsQuery = { __typename?: 'Query', clips: Array<{ __typename?: 'Clip', id: string, stream: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string }> };

export type GetClipQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetClipQuery = { __typename?: 'Query', clip?: { __typename?: 'Clip', id: string, stream: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string } | null | undefined };

export type GetClipViewingUrlsQueryVariables = Exact<{
  clipId: Scalars['ID']['input'];
}>;


export type GetClipViewingUrlsQuery = { __typename?: 'Query', clipViewingUrls: { __typename?: 'ClipViewingUrls', hls?: string | null | undefined, dash?: string | null | undefined, mp4?: string | null | undefined, webm?: string | null | undefined } };

export type GetApiTokensQueryVariables = Exact<{ [key: string]: never; }>;


export type GetApiTokensQuery = { __typename?: 'Query', developerTokens: Array<{ __typename?: 'DeveloperToken', id: string, name: string, permissions: string, status: string, lastUsedAt?: string | null | undefined, expiresAt?: string | null | undefined, createdAt: string }> };

export type DvrRequestInfoFragment = { __typename?: 'DVRRequest', dvrHash: string, internalName: string, storageNodeId?: string | null | undefined, status: string, startedAt?: string | null | undefined, endedAt?: string | null | undefined, durationSeconds?: number | null | undefined, sizeBytes?: number | null | undefined, manifestPath?: string | null | undefined, errorMessage?: string | null | undefined, createdAt: string, updatedAt: string };

export type RecordingConfigInfoFragment = { __typename?: 'RecordingConfig', enabled: boolean, retentionDays: number, format: string, segmentDuration: number };

export type GetDvrRequestsQueryVariables = Exact<{
  internalName?: InputMaybe<Scalars['String']['input']>;
  status?: InputMaybe<Scalars['String']['input']>;
  pagination?: InputMaybe<PaginationInput>;
}>;


export type GetDvrRequestsQuery = { __typename?: 'Query', dvrRequests: { __typename?: 'DVRRequestList', total: number, page: number, limit: number, dvrRecordings: Array<{ __typename?: 'DVRRequest', dvrHash: string, internalName: string, storageNodeId?: string | null | undefined, status: string, startedAt?: string | null | undefined, endedAt?: string | null | undefined, durationSeconds?: number | null | undefined, sizeBytes?: number | null | undefined, manifestPath?: string | null | undefined, errorMessage?: string | null | undefined, createdAt: string, updatedAt: string }> } };

export type GetRecordingConfigQueryVariables = Exact<{
  internalName: Scalars['String']['input'];
}>;


export type GetRecordingConfigQuery = { __typename?: 'Query', recordingConfig: { __typename?: 'RecordingConfig', enabled: boolean, retentionDays: number, format: string, segmentDuration: number } };

export type GetStreamHealthMetricsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamHealthMetricsQuery = { __typename?: 'Query', streamHealthMetrics: Array<{ __typename?: 'StreamHealthMetric', timestamp: string, stream: string, nodeId: string, healthScore: number, frameJitterMs?: number | null | undefined, keyframeStabilityMs?: number | null | undefined, issuesDescription?: string | null | undefined, hasIssues: boolean, bitrate?: number | null | undefined, fps?: number | null | undefined, width?: number | null | undefined, height?: number | null | undefined, codec?: string | null | undefined, qualityTier?: string | null | undefined, packetsSent?: number | null | undefined, packetsLost?: number | null | undefined, packetLossPercentage?: number | null | undefined, bufferState: BufferState, bufferHealth?: number | null | undefined, audioChannels?: number | null | undefined, audioSampleRate?: number | null | undefined, audioCodec?: string | null | undefined, audioBitrate?: number | null | undefined }> };

export type GetCurrentStreamHealthQueryVariables = Exact<{
  stream: Scalars['String']['input'];
}>;


export type GetCurrentStreamHealthQuery = { __typename?: 'Query', currentStreamHealth?: { __typename?: 'StreamHealthMetric', timestamp: string, stream: string, nodeId: string, healthScore: number, frameJitterMs?: number | null | undefined, keyframeStabilityMs?: number | null | undefined, issuesDescription?: string | null | undefined, hasIssues: boolean, bufferState: BufferState, packetLossPercentage?: number | null | undefined, qualityTier?: string | null | undefined } | null | undefined };

export type GetTrackListEventsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetTrackListEventsQuery = { __typename?: 'Query', trackListEvents: Array<{ __typename?: 'TrackListEvent', timestamp: string, stream: string, nodeId?: string | null | undefined, trackList: string, trackCount: number, tracks?: Array<{ __typename?: 'StreamTrack', trackName: string, trackType: string, codec?: string | null | undefined, bitrateKbps?: number | null | undefined, bitrateBps?: number | null | undefined, buffer?: number | null | undefined, jitter?: number | null | undefined, width?: number | null | undefined, height?: number | null | undefined, fps?: number | null | undefined, resolution?: string | null | undefined, hasBFrames?: boolean | null | undefined, channels?: number | null | undefined, sampleRate?: number | null | undefined }> | null | undefined }> };

export type GetStreamHealthAlertsQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamHealthAlertsQuery = { __typename?: 'Query', streamHealthAlerts: Array<{ __typename?: 'StreamHealthAlert', timestamp: string, stream: string, nodeId: string, alertType: AlertType, severity: AlertSeverity, healthScore?: number | null | undefined, frameJitterMs?: number | null | undefined, packetLossPercentage?: number | null | undefined, issuesDescription?: string | null | undefined, bufferState?: BufferState | null | undefined, qualityTier?: string | null | undefined }> };

export type GetRebufferingEventsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetRebufferingEventsQuery = { __typename?: 'Query', rebufferingEvents: Array<{ __typename?: 'RebufferingEvent', timestamp: string, stream: string, nodeId: string, bufferState: BufferState, previousState: BufferState, rebufferStart: boolean, rebufferEnd: boolean, healthScore?: number | null | undefined, frameJitterMs?: number | null | undefined, packetLossPercentage?: number | null | undefined }> };

export type GetTenantQueryVariables = Exact<{ [key: string]: never; }>;


export type GetTenantQuery = { __typename?: 'Query', tenant?: { __typename?: 'Tenant', id: string, name: string, settings?: any | null | undefined, cluster?: string | null | undefined, createdAt: string } | null | undefined };

export type GetClustersQueryVariables = Exact<{ [key: string]: never; }>;


export type GetClustersQuery = { __typename?: 'Query', clusters: Array<{ __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string, latitude?: number | null | undefined, longitude?: number | null | undefined, location?: string | null | undefined }> }> };

export type GetClusterQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetClusterQuery = { __typename?: 'Query', cluster?: { __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string, latitude?: number | null | undefined, longitude?: number | null | undefined, location?: string | null | undefined }> } | null | undefined };

export type GetNodesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetNodesQuery = { __typename?: 'Query', nodes: Array<{ __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string, latitude?: number | null | undefined, longitude?: number | null | undefined, location?: string | null | undefined }> };

export type GetNodeQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetNodeQuery = { __typename?: 'Query', node?: { __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string, latitude?: number | null | undefined, longitude?: number | null | undefined, location?: string | null | undefined } | null | undefined };

export type ServiceInstanceInfoFragment = { __typename?: 'ServiceInstance', id: string, instanceId: string, clusterId: string, nodeId?: string | null | undefined, serviceId: string, version?: string | null | undefined, port?: number | null | undefined, processId?: number | null | undefined, containerId?: string | null | undefined, status: InstanceStatus, healthStatus: NodeStatus, startedAt?: string | null | undefined, stoppedAt?: string | null | undefined, lastHealthCheck?: string | null | undefined };

export type GetServiceInstancesQueryVariables = Exact<{
  clusterId?: InputMaybe<Scalars['String']['input']>;
}>;


export type GetServiceInstancesQuery = { __typename?: 'Query', serviceInstances: Array<{ __typename?: 'ServiceInstance', id: string, instanceId: string, clusterId: string, nodeId?: string | null | undefined, serviceId: string, version?: string | null | undefined, port?: number | null | undefined, processId?: number | null | undefined, containerId?: string | null | undefined, status: InstanceStatus, healthStatus: NodeStatus, startedAt?: string | null | undefined, stoppedAt?: string | null | undefined, lastHealthCheck?: string | null | undefined }> };

export type TenantClusterAssignmentInfoFragment = { __typename?: 'TenantClusterAssignment', id: string, tenantId: string, clusterId: string, deploymentTier?: string | null | undefined, priority: number, isPrimary: boolean, isActive: boolean, maxStreamsOnCluster?: number | null | undefined, maxViewersOnCluster?: number | null | undefined, maxBandwidthMbpsOnCluster?: number | null | undefined, fallbackWhenFull: boolean, createdAt: string, updatedAt: string };

export type GetTenantClusterAssignmentsQueryVariables = Exact<{ [key: string]: never; }>;


export type GetTenantClusterAssignmentsQuery = { __typename?: 'Query', tenantClusterAssignments: Array<{ __typename?: 'TenantClusterAssignment', id: string, tenantId: string, clusterId: string, deploymentTier?: string | null | undefined, priority: number, isPrimary: boolean, isActive: boolean, maxStreamsOnCluster?: number | null | undefined, maxViewersOnCluster?: number | null | undefined, maxBandwidthMbpsOnCluster?: number | null | undefined, fallbackWhenFull: boolean, createdAt: string, updatedAt: string }> };

export type IntrospectSchemaQueryVariables = Exact<{ [key: string]: never; }>;


export type IntrospectSchemaQuery = { __typename?: 'Query', __schema: { __typename?: '__Schema', queryType: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined }, mutationType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined } | null | undefined, subscriptionType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined } | null | undefined, types: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, description?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }>, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, inputFields?: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, interfaces?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined, enumValues?: Array<{ __typename?: '__EnumValue', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined }> | null | undefined, possibleTypes?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined }> } };

export type FullTypeFragment = { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, description?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }>, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, inputFields?: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, interfaces?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined, enumValues?: Array<{ __typename?: '__EnumValue', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined }> | null | undefined, possibleTypes?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined };

export type InputValueFragment = { __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } };

export type TypeRefFragment = { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined };

export type GetRootTypesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetRootTypesQuery = { __typename?: 'Query', __schema: { __typename?: '__Schema', queryType: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined }, mutationType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined } | null | undefined, subscriptionType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined } | null | undefined } };

export type GetViewerMetrics5mQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetViewerMetrics5mQuery = { __typename?: 'Query', viewerMetrics5m: Array<{ __typename?: 'ViewerMetrics5m', timestamp: string, internalName: string, nodeId: string, peakViewers: number, avgViewers: number, uniqueCountries: number, uniqueCities: number, avgConnectionQuality: number, avgBufferHealth: number }> };

export type GetPerformanceServiceInstancesQueryVariables = Exact<{
  clusterId?: InputMaybe<Scalars['String']['input']>;
}>;


export type GetPerformanceServiceInstancesQuery = { __typename?: 'Query', serviceInstances: Array<{ __typename?: 'ServiceInstance', id: string, instanceId: string, clusterId: string, nodeId?: string | null | undefined, serviceId: string, version?: string | null | undefined, port?: number | null | undefined, processId?: number | null | undefined, containerId?: string | null | undefined, status: InstanceStatus, healthStatus: NodeStatus, startedAt?: string | null | undefined, stoppedAt?: string | null | undefined, lastHealthCheck?: string | null | undefined, cpuUsagePercent?: number | null | undefined, memoryUsageMb?: number | null | undefined }> };

export type GetPlatformPerformanceQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetPlatformPerformanceQuery = { __typename?: 'Query', viewerMetrics5m: Array<{ __typename?: 'ViewerMetrics5m', timestamp: string, internalName: string, nodeId: string, peakViewers: number, avgViewers: number, uniqueCountries: number, uniqueCities: number, avgConnectionQuality: number, avgBufferHealth: number }>, nodeMetrics: Array<{ __typename?: 'NodeMetric', timestamp: string, nodeId: string, cpuUsage: number, memoryUsage: number, diskUsage: number, healthScore: number, status: string }> };

export type GetStreamPerformanceQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamPerformanceQuery = { __typename?: 'Query', viewerMetrics5m: Array<{ __typename?: 'ViewerMetrics5m', timestamp: string, internalName: string, nodeId: string, peakViewers: number, avgViewers: number, uniqueCountries: number, uniqueCities: number, avgConnectionQuality: number, avgBufferHealth: number }>, routingEvents: Array<{ __typename?: 'RoutingEvent', timestamp: string, selectedNode: string, score?: number | null | undefined, status: string, clientCountry?: string | null | undefined, nodeLatitude?: number | null | undefined, nodeLongitude?: number | null | undefined }> };

export type GetNodeEfficiencyQueryVariables = Exact<{
  nodeId: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetNodeEfficiencyQuery = { __typename?: 'Query', nodeMetrics: Array<{ __typename?: 'NodeMetric', timestamp: string, nodeId: string, cpuUsage: number, memoryUsage: number, diskUsage: number, networkRx: number, networkTx: number, healthScore: number, status: string }>, routingEvents: Array<{ __typename?: 'RoutingEvent', timestamp: string, streamName: string, selectedNode: string, score?: number | null | undefined, status: string, clientCountry?: string | null | undefined }> };

export type GetRegionalPerformanceQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetRegionalPerformanceQuery = { __typename?: 'Query', viewerMetrics5m: Array<{ __typename?: 'ViewerMetrics5m', timestamp: string, internalName: string, nodeId: string, avgViewers: number, uniqueCountries: number, avgConnectionQuality: number, avgBufferHealth: number }>, connectionEvents: Array<{ __typename?: 'ConnectionEvent', timestamp: string, nodeId: string, countryCode?: string | null | undefined, city?: string | null | undefined, eventType: string }> };

export type GetRoutingEventsQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetRoutingEventsQuery = { __typename?: 'Query', routingEvents: Array<{ __typename?: 'RoutingEvent', timestamp: string, streamName: string, selectedNode: string, status: string, details?: string | null | undefined, score?: number | null | undefined, clientIp?: string | null | undefined, clientCountry?: string | null | undefined, clientLatitude?: number | null | undefined, clientLongitude?: number | null | undefined, nodeLatitude?: number | null | undefined, nodeLongitude?: number | null | undefined, nodeName?: string | null | undefined }> };

export type GetConnectionEventsQueryVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetConnectionEventsQuery = { __typename?: 'Query', connectionEvents: Array<{ __typename?: 'ConnectionEvent', eventId: string, timestamp: string, tenantId: string, internalName: string, sessionId: string, connectionAddr: string, connector: string, nodeId: string, countryCode?: string | null | undefined, city?: string | null | undefined, latitude?: number | null | undefined, longitude?: number | null | undefined, eventType: string }> };

export type GetNodeMetricsQueryVariables = Exact<{
  nodeId?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetNodeMetricsQuery = { __typename?: 'Query', nodeMetrics: Array<{ __typename?: 'NodeMetric', timestamp: string, nodeId: string, cpuUsage: number, memoryUsage: number, diskUsage: number, networkRx: number, networkTx: number, healthScore: number, status: string, latitude?: number | null | undefined, longitude?: number | null | undefined, tags?: Array<string> | null | undefined, metadata?: any | null | undefined }> };

export type GetPlatformRoutingEventsQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetPlatformRoutingEventsQuery = { __typename?: 'Query', routingEvents: Array<{ __typename?: 'RoutingEvent', timestamp: string, streamName: string, selectedNode: string, status: string, score?: number | null | undefined, clientCountry?: string | null | undefined, clientLatitude?: number | null | undefined, clientLongitude?: number | null | undefined, nodeLatitude?: number | null | undefined, nodeLongitude?: number | null | undefined, nodeName?: string | null | undefined, details?: string | null | undefined }> };

export type GetStreamConnectionEventsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamConnectionEventsQuery = { __typename?: 'Query', connectionEvents: Array<{ __typename?: 'ConnectionEvent', timestamp: string, sessionId: string, connectionAddr: string, nodeId: string, countryCode?: string | null | undefined, city?: string | null | undefined, latitude?: number | null | undefined, longitude?: number | null | undefined, eventType: string }> };

export type GetAllNodeMetricsQueryVariables = Exact<{
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetAllNodeMetricsQuery = { __typename?: 'Query', nodeMetrics: Array<{ __typename?: 'NodeMetric', timestamp: string, nodeId: string, cpuUsage: number, memoryUsage: number, diskUsage: number, networkRx: number, networkTx: number, healthScore: number, status: string, latitude?: number | null | undefined, longitude?: number | null | undefined }> };

export type StreamInfoFragment = { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string };

export type GetStreamsQueryVariables = Exact<{ [key: string]: never; }>;


export type GetStreamsQuery = { __typename?: 'Query', streams: Array<{ __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string }> };

export type GetStreamQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetStreamQuery = { __typename?: 'Query', stream?: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, createdAt: string, updatedAt: string } | null | undefined };

export type ValidateStreamKeyQueryVariables = Exact<{
  streamKey: Scalars['String']['input'];
}>;


export type ValidateStreamKeyQuery = { __typename?: 'Query', validateStreamKey: { __typename?: 'StreamValidation', valid: boolean, streamKey: string, error?: string | null | undefined } };

export type StreamKeyInfoFragment = { __typename?: 'StreamKey', id: string, streamId: string, keyValue: string, keyName?: string | null | undefined, isActive: boolean, lastUsedAt?: string | null | undefined, createdAt: string };

export type GetStreamKeysQueryVariables = Exact<{
  streamId: Scalars['ID']['input'];
}>;


export type GetStreamKeysQuery = { __typename?: 'Query', streamKeys: Array<{ __typename?: 'StreamKey', id: string, streamId: string, keyValue: string, keyName?: string | null | undefined, isActive: boolean, lastUsedAt?: string | null | undefined, createdAt: string }> };

export type RecordingInfoFragment = { __typename?: 'Recording', id: string, streamId: string, title?: string | null | undefined, duration?: number | null | undefined, fileSizeBytes?: number | null | undefined, playbackId?: string | null | undefined, thumbnailUrl?: string | null | undefined, startTime?: string | null | undefined, endTime?: string | null | undefined, status: string, createdAt: string, updatedAt: string };

export type GetRecordingsQueryVariables = Exact<{
  streamId?: InputMaybe<Scalars['ID']['input']>;
}>;


export type GetRecordingsQuery = { __typename?: 'Query', recordings: Array<{ __typename?: 'Recording', id: string, streamId: string, title?: string | null | undefined, duration?: number | null | undefined, fileSizeBytes?: number | null | undefined, playbackId?: string | null | undefined, thumbnailUrl?: string | null | undefined, startTime?: string | null | undefined, endTime?: string | null | undefined, status: string, createdAt: string, updatedAt: string }> };

export type GetStreamRecordingsQueryVariables = Exact<{
  streamId: Scalars['ID']['input'];
}>;


export type GetStreamRecordingsQuery = { __typename?: 'Query', recordings: Array<{ __typename?: 'Recording', id: string, streamId: string, title?: string | null | undefined, duration?: number | null | undefined, fileSizeBytes?: number | null | undefined, playbackId?: string | null | undefined, thumbnailUrl?: string | null | undefined, startTime?: string | null | undefined, endTime?: string | null | undefined, status: string, createdAt: string, updatedAt: string }> };

export type StreamEventsSubscriptionVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
}>;


export type StreamEventsSubscription = { __typename?: 'Subscription', streamEvents: { __typename?: 'StreamEvent', type: StreamEventType, stream: string, status: StreamStatus, timestamp: string, details?: any | null | undefined } };

export type ViewerMetricsStreamSubscriptionVariables = Exact<{
  stream: Scalars['String']['input'];
}>;


export type ViewerMetricsStreamSubscription = { __typename?: 'Subscription', viewerMetrics: { __typename?: 'ViewerMetrics', stream: string, currentViewers: number, viewerCount: number, peakViewers: number, bandwidth: number, connectionQuality?: number | null | undefined, bufferHealth?: number | null | undefined, timestamp: string } };

export type TrackListUpdatesSubscriptionVariables = Exact<{
  stream: Scalars['String']['input'];
}>;


export type TrackListUpdatesSubscription = { __typename?: 'Subscription', trackListUpdates: { __typename?: 'TrackListEvent', stream: string, trackList: string, trackCount: number, timestamp: string } };

export type SystemHealthSubscriptionVariables = Exact<{ [key: string]: never; }>;


export type SystemHealthSubscription = { __typename?: 'Subscription', systemHealth: { __typename?: 'SystemHealthEvent', node: string, cluster: string, status: NodeStatus, cpuUsage: number, memoryUsage: number, diskUsage: number, healthScore: number, timestamp: string } };
