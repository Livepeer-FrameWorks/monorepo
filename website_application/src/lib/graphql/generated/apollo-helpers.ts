import type { FieldPolicy, FieldReadFunction, TypePolicies, TypePolicy } from '@apollo/client/cache';
import { gql } from '@apollo/client';
import * as Apollo from '@apollo/client';
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
  durationSec?: Maybe<Scalars['Int']['output']>;
  filePath?: Maybe<Scalars['String']['output']>;
  format?: Maybe<Scalars['String']['output']>;
  ingestNodeId?: Maybe<Scalars['String']['output']>;
  internalName: Scalars['String']['output'];
  message?: Maybe<Scalars['String']['output']>;
  percent?: Maybe<Scalars['Int']['output']>;
  requestId: Scalars['String']['output'];
  routingDistanceKm?: Maybe<Scalars['Float']['output']>;
  s3Url?: Maybe<Scalars['String']['output']>;
  sizeBytes?: Maybe<Scalars['Int']['output']>;
  stage: Scalars['String']['output'];
  startMs?: Maybe<Scalars['Int']['output']>;
  startUnix?: Maybe<Scalars['Int']['output']>;
  stopMs?: Maybe<Scalars['Int']['output']>;
  stopUnix?: Maybe<Scalars['Int']['output']>;
  storageNodeId?: Maybe<Scalars['String']['output']>;
  timestamp: Scalars['Time']['output'];
  title?: Maybe<Scalars['String']['output']>;
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
  method: PaymentMethod;
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
  createClip: Clip;
  createDeveloperToken: DeveloperToken;
  createPayment: Payment;
  createStream: Stream;
  createStreamKey: StreamKey;
  deleteClip: Scalars['Boolean']['output'];
  deleteStream: Scalars['Boolean']['output'];
  deleteStreamKey: Scalars['Boolean']['output'];
  refreshStreamKey: Stream;
  revokeDeveloperToken: Scalars['Boolean']['output'];
  setStreamRecordingConfig: RecordingConfig;
  startDVR: DvrRequest;
  stopDVR: Scalars['Boolean']['output'];
  updateBillingTier: BillingStatus;
  updateStream: Stream;
  updateTenant: Tenant;
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


export type MutationUpdateBillingTierArgs = {
  tierId: Scalars['ID']['input'];
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

export enum QualityChangeType {
  BitrateChange = 'BITRATE_CHANGE',
  CodecChange = 'CODEC_CHANGE',
  ResolutionChange = 'RESOLUTION_CHANGE',
  TrackAdded = 'TRACK_ADDED',
  TrackRemoved = 'TRACK_REMOVED',
  TrackUpdate = 'TRACK_UPDATE'
}

export type Query = {
  __typename?: 'Query';
  billingStatus: BillingStatus;
  billingTiers: Array<BillingTier>;
  clip?: Maybe<Clip>;
  clipEvents: Array<ClipEvent>;
  clipViewingUrls: ClipViewingUrls;
  clips: Array<Clip>;
  cluster?: Maybe<Cluster>;
  clusters: Array<Cluster>;
  connectionEvents: Array<ConnectionEvent>;
  currentStreamHealth?: Maybe<StreamHealthMetric>;
  developerTokens: Array<DeveloperToken>;
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
  routingEvents: Array<RoutingEvent>;
  serviceInstances: Array<ServiceInstance>;
  stream?: Maybe<Stream>;
  streamAnalytics?: Maybe<StreamAnalytics>;
  streamHealthAlerts: Array<StreamHealthAlert>;
  streamHealthMetrics: Array<StreamHealthMetric>;
  streamKeys: Array<StreamKey>;
  streamQualityChanges: Array<StreamQualityChange>;
  streams: Array<Stream>;
  tenant?: Maybe<Tenant>;
  tenantClusterAssignments: Array<TenantClusterAssignment>;
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


export type QueryStreamQualityChangesArgs = {
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

export type StreamQualityChange = {
  __typename?: 'StreamQualityChange';
  changeType: QualityChangeType;
  newCodec?: Maybe<Scalars['String']['output']>;
  newQualityTier?: Maybe<Scalars['String']['output']>;
  newResolution?: Maybe<Scalars['String']['output']>;
  newTracks?: Maybe<Scalars['String']['output']>;
  nodeId: Scalars['String']['output'];
  previousCodec?: Maybe<Scalars['String']['output']>;
  previousQualityTier?: Maybe<Scalars['String']['output']>;
  previousResolution?: Maybe<Scalars['String']['output']>;
  previousTracks?: Maybe<Scalars['String']['output']>;
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
};

export enum StreamStatus {
  Ended = 'ENDED',
  Live = 'LIVE',
  Offline = 'OFFLINE',
  Recording = 'RECORDING'
}

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
  stream: Scalars['String']['output'];
  timestamp: Scalars['Time']['output'];
  trackCount: Scalars['Int']['output'];
  trackList: Scalars['String']['output'];
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

export type UpdateBillingTierMutationVariables = Exact<{
  tierId: Scalars['ID']['input'];
}>;


export type UpdateBillingTierMutation = { __typename?: 'Mutation', updateBillingTier: { __typename?: 'BillingStatus', nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

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

export type GetStreamQualityChangesQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamQualityChangesQuery = { __typename?: 'Query', streamQualityChanges: Array<{ __typename?: 'StreamQualityChange', timestamp: string, stream: string, nodeId: string, changeType: QualityChangeType, previousQualityTier?: string | null | undefined, newQualityTier?: string | null | undefined, previousResolution?: string | null | undefined, newResolution?: string | null | undefined, previousCodec?: string | null | undefined, newCodec?: string | null | undefined, previousTracks?: string | null | undefined, newTracks?: string | null | undefined }> };

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

export type BillingStatusKeySpecifier = ('currentTier' | 'nextBillingDate' | 'outstandingAmount' | 'status' | BillingStatusKeySpecifier)[];
export type BillingStatusFieldPolicy = {
	currentTier?: FieldPolicy<any> | FieldReadFunction<any>,
	nextBillingDate?: FieldPolicy<any> | FieldReadFunction<any>,
	outstandingAmount?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>
};
export type BillingTierKeySpecifier = ('currency' | 'description' | 'features' | 'id' | 'name' | 'price' | BillingTierKeySpecifier)[];
export type BillingTierFieldPolicy = {
	currency?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	features?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	price?: FieldPolicy<any> | FieldReadFunction<any>
};
export type CityMetricKeySpecifier = ('city' | 'countryCode' | 'latitude' | 'longitude' | 'percentage' | 'viewerCount' | CityMetricKeySpecifier)[];
export type CityMetricFieldPolicy = {
	city?: FieldPolicy<any> | FieldReadFunction<any>,
	countryCode?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	percentage?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ClipKeySpecifier = ('createdAt' | 'description' | 'duration' | 'endTime' | 'id' | 'playbackId' | 'startTime' | 'status' | 'stream' | 'title' | 'updatedAt' | 'viewingUrls' | ClipKeySpecifier)[];
export type ClipFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	duration?: FieldPolicy<any> | FieldReadFunction<any>,
	endTime?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	startTime?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	title?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	viewingUrls?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ClipEventKeySpecifier = ('contentType' | 'durationSec' | 'filePath' | 'format' | 'ingestNodeId' | 'internalName' | 'message' | 'percent' | 'requestId' | 'routingDistanceKm' | 's3Url' | 'sizeBytes' | 'stage' | 'startMs' | 'startUnix' | 'stopMs' | 'stopUnix' | 'storageNodeId' | 'timestamp' | 'title' | ClipEventKeySpecifier)[];
export type ClipEventFieldPolicy = {
	contentType?: FieldPolicy<any> | FieldReadFunction<any>,
	durationSec?: FieldPolicy<any> | FieldReadFunction<any>,
	filePath?: FieldPolicy<any> | FieldReadFunction<any>,
	format?: FieldPolicy<any> | FieldReadFunction<any>,
	ingestNodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	internalName?: FieldPolicy<any> | FieldReadFunction<any>,
	message?: FieldPolicy<any> | FieldReadFunction<any>,
	percent?: FieldPolicy<any> | FieldReadFunction<any>,
	requestId?: FieldPolicy<any> | FieldReadFunction<any>,
	routingDistanceKm?: FieldPolicy<any> | FieldReadFunction<any>,
	s3Url?: FieldPolicy<any> | FieldReadFunction<any>,
	sizeBytes?: FieldPolicy<any> | FieldReadFunction<any>,
	stage?: FieldPolicy<any> | FieldReadFunction<any>,
	startMs?: FieldPolicy<any> | FieldReadFunction<any>,
	startUnix?: FieldPolicy<any> | FieldReadFunction<any>,
	stopMs?: FieldPolicy<any> | FieldReadFunction<any>,
	stopUnix?: FieldPolicy<any> | FieldReadFunction<any>,
	storageNodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	title?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ClipViewingUrlsKeySpecifier = ('dash' | 'hls' | 'mp4' | 'webm' | ClipViewingUrlsKeySpecifier)[];
export type ClipViewingUrlsFieldPolicy = {
	dash?: FieldPolicy<any> | FieldReadFunction<any>,
	hls?: FieldPolicy<any> | FieldReadFunction<any>,
	mp4?: FieldPolicy<any> | FieldReadFunction<any>,
	webm?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ClusterKeySpecifier = ('createdAt' | 'id' | 'name' | 'nodes' | 'region' | 'serviceInstances' | 'status' | ClusterKeySpecifier)[];
export type ClusterFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	nodes?: FieldPolicy<any> | FieldReadFunction<any>,
	region?: FieldPolicy<any> | FieldReadFunction<any>,
	serviceInstances?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ConnectionEventKeySpecifier = ('city' | 'connectionAddr' | 'connector' | 'countryCode' | 'eventId' | 'eventType' | 'internalName' | 'latitude' | 'longitude' | 'nodeId' | 'sessionId' | 'tenantId' | 'timestamp' | ConnectionEventKeySpecifier)[];
export type ConnectionEventFieldPolicy = {
	city?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionAddr?: FieldPolicy<any> | FieldReadFunction<any>,
	connector?: FieldPolicy<any> | FieldReadFunction<any>,
	countryCode?: FieldPolicy<any> | FieldReadFunction<any>,
	eventId?: FieldPolicy<any> | FieldReadFunction<any>,
	eventType?: FieldPolicy<any> | FieldReadFunction<any>,
	internalName?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	sessionId?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type CountryMetricKeySpecifier = ('cities' | 'countryCode' | 'percentage' | 'viewerCount' | CountryMetricKeySpecifier)[];
export type CountryMetricFieldPolicy = {
	cities?: FieldPolicy<any> | FieldReadFunction<any>,
	countryCode?: FieldPolicy<any> | FieldReadFunction<any>,
	percentage?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type CountryTimeSeriesKeySpecifier = ('countryCode' | 'timestamp' | 'viewerCount' | CountryTimeSeriesKeySpecifier)[];
export type CountryTimeSeriesFieldPolicy = {
	countryCode?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type DVRRequestKeySpecifier = ('createdAt' | 'durationSeconds' | 'dvrHash' | 'endedAt' | 'errorMessage' | 'internalName' | 'manifestPath' | 'sizeBytes' | 'startedAt' | 'status' | 'storageNodeId' | 'updatedAt' | DVRRequestKeySpecifier)[];
export type DVRRequestFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	durationSeconds?: FieldPolicy<any> | FieldReadFunction<any>,
	dvrHash?: FieldPolicy<any> | FieldReadFunction<any>,
	endedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	errorMessage?: FieldPolicy<any> | FieldReadFunction<any>,
	internalName?: FieldPolicy<any> | FieldReadFunction<any>,
	manifestPath?: FieldPolicy<any> | FieldReadFunction<any>,
	sizeBytes?: FieldPolicy<any> | FieldReadFunction<any>,
	startedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	storageNodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type DVRRequestListKeySpecifier = ('dvrRecordings' | 'limit' | 'page' | 'total' | DVRRequestListKeySpecifier)[];
export type DVRRequestListFieldPolicy = {
	dvrRecordings?: FieldPolicy<any> | FieldReadFunction<any>,
	limit?: FieldPolicy<any> | FieldReadFunction<any>,
	page?: FieldPolicy<any> | FieldReadFunction<any>,
	total?: FieldPolicy<any> | FieldReadFunction<any>
};
export type DeveloperTokenKeySpecifier = ('createdAt' | 'expiresAt' | 'id' | 'lastUsedAt' | 'name' | 'permissions' | 'status' | 'token' | DeveloperTokenKeySpecifier)[];
export type DeveloperTokenFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	expiresAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	lastUsedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	permissions?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	token?: FieldPolicy<any> | FieldReadFunction<any>
};
export type GeographicDistributionKeySpecifier = ('stream' | 'timeRange' | 'topCities' | 'topCountries' | 'totalViewers' | 'uniqueCities' | 'uniqueCountries' | 'viewersByCountry' | GeographicDistributionKeySpecifier)[];
export type GeographicDistributionFieldPolicy = {
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timeRange?: FieldPolicy<any> | FieldReadFunction<any>,
	topCities?: FieldPolicy<any> | FieldReadFunction<any>,
	topCountries?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCities?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCountries?: FieldPolicy<any> | FieldReadFunction<any>,
	viewersByCountry?: FieldPolicy<any> | FieldReadFunction<any>
};
export type InvoiceKeySpecifier = ('amount' | 'createdAt' | 'currency' | 'dueDate' | 'id' | 'lineItems' | 'status' | InvoiceKeySpecifier)[];
export type InvoiceFieldPolicy = {
	amount?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	currency?: FieldPolicy<any> | FieldReadFunction<any>,
	dueDate?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	lineItems?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>
};
export type LineItemKeySpecifier = ('description' | 'quantity' | 'total' | 'unitPrice' | LineItemKeySpecifier)[];
export type LineItemFieldPolicy = {
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	quantity?: FieldPolicy<any> | FieldReadFunction<any>,
	total?: FieldPolicy<any> | FieldReadFunction<any>,
	unitPrice?: FieldPolicy<any> | FieldReadFunction<any>
};
export type LoadBalancingMetricKeySpecifier = ('clientCountry' | 'clientIp' | 'clientLatitude' | 'clientLongitude' | 'details' | 'eventType' | 'nodeId' | 'nodeLatitude' | 'nodeLongitude' | 'nodeName' | 'routingDistance' | 'score' | 'selectedNode' | 'source' | 'status' | 'stream' | 'timestamp' | LoadBalancingMetricKeySpecifier)[];
export type LoadBalancingMetricFieldPolicy = {
	clientCountry?: FieldPolicy<any> | FieldReadFunction<any>,
	clientIp?: FieldPolicy<any> | FieldReadFunction<any>,
	clientLatitude?: FieldPolicy<any> | FieldReadFunction<any>,
	clientLongitude?: FieldPolicy<any> | FieldReadFunction<any>,
	details?: FieldPolicy<any> | FieldReadFunction<any>,
	eventType?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeLatitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeLongitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeName?: FieldPolicy<any> | FieldReadFunction<any>,
	routingDistance?: FieldPolicy<any> | FieldReadFunction<any>,
	score?: FieldPolicy<any> | FieldReadFunction<any>,
	selectedNode?: FieldPolicy<any> | FieldReadFunction<any>,
	source?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type MutationKeySpecifier = ('createClip' | 'createDeveloperToken' | 'createPayment' | 'createStream' | 'createStreamKey' | 'deleteClip' | 'deleteStream' | 'deleteStreamKey' | 'refreshStreamKey' | 'revokeDeveloperToken' | 'setStreamRecordingConfig' | 'startDVR' | 'stopDVR' | 'updateBillingTier' | 'updateStream' | 'updateTenant' | MutationKeySpecifier)[];
export type MutationFieldPolicy = {
	createClip?: FieldPolicy<any> | FieldReadFunction<any>,
	createDeveloperToken?: FieldPolicy<any> | FieldReadFunction<any>,
	createPayment?: FieldPolicy<any> | FieldReadFunction<any>,
	createStream?: FieldPolicy<any> | FieldReadFunction<any>,
	createStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	deleteClip?: FieldPolicy<any> | FieldReadFunction<any>,
	deleteStream?: FieldPolicy<any> | FieldReadFunction<any>,
	deleteStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	refreshStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	revokeDeveloperToken?: FieldPolicy<any> | FieldReadFunction<any>,
	setStreamRecordingConfig?: FieldPolicy<any> | FieldReadFunction<any>,
	startDVR?: FieldPolicy<any> | FieldReadFunction<any>,
	stopDVR?: FieldPolicy<any> | FieldReadFunction<any>,
	updateBillingTier?: FieldPolicy<any> | FieldReadFunction<any>,
	updateStream?: FieldPolicy<any> | FieldReadFunction<any>,
	updateTenant?: FieldPolicy<any> | FieldReadFunction<any>
};
export type NodeKeySpecifier = ('cluster' | 'clusterInfo' | 'createdAt' | 'id' | 'ipAddress' | 'lastSeen' | 'latitude' | 'location' | 'longitude' | 'metrics' | 'metrics1h' | 'name' | 'region' | 'serviceInstances' | 'status' | 'type' | NodeKeySpecifier)[];
export type NodeFieldPolicy = {
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	clusterInfo?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	ipAddress?: FieldPolicy<any> | FieldReadFunction<any>,
	lastSeen?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	location?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	metrics?: FieldPolicy<any> | FieldReadFunction<any>,
	metrics1h?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	region?: FieldPolicy<any> | FieldReadFunction<any>,
	serviceInstances?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	type?: FieldPolicy<any> | FieldReadFunction<any>
};
export type NodeMetricKeySpecifier = ('cpuUsage' | 'diskUsage' | 'healthScore' | 'latitude' | 'longitude' | 'memoryUsage' | 'metadata' | 'networkRx' | 'networkTx' | 'nodeId' | 'status' | 'tags' | 'timestamp' | NodeMetricKeySpecifier)[];
export type NodeMetricFieldPolicy = {
	cpuUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	diskUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	memoryUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	metadata?: FieldPolicy<any> | FieldReadFunction<any>,
	networkRx?: FieldPolicy<any> | FieldReadFunction<any>,
	networkTx?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	tags?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type NodeMetricHourlyKeySpecifier = ('avgCpu' | 'avgHealthScore' | 'avgMemory' | 'nodeId' | 'peakCpu' | 'peakMemory' | 'timestamp' | 'totalBandwidthIn' | 'totalBandwidthOut' | 'wasHealthy' | NodeMetricHourlyKeySpecifier)[];
export type NodeMetricHourlyFieldPolicy = {
	avgCpu?: FieldPolicy<any> | FieldReadFunction<any>,
	avgHealthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	avgMemory?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	peakCpu?: FieldPolicy<any> | FieldReadFunction<any>,
	peakMemory?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	totalBandwidthIn?: FieldPolicy<any> | FieldReadFunction<any>,
	totalBandwidthOut?: FieldPolicy<any> | FieldReadFunction<any>,
	wasHealthy?: FieldPolicy<any> | FieldReadFunction<any>
};
export type PaymentKeySpecifier = ('amount' | 'createdAt' | 'currency' | 'id' | 'method' | 'status' | PaymentKeySpecifier)[];
export type PaymentFieldPolicy = {
	amount?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	currency?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	method?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>
};
export type PlatformOverviewKeySpecifier = ('timeRange' | 'totalBandwidth' | 'totalStreams' | 'totalUsers' | 'totalViewers' | PlatformOverviewKeySpecifier)[];
export type PlatformOverviewFieldPolicy = {
	timeRange?: FieldPolicy<any> | FieldReadFunction<any>,
	totalBandwidth?: FieldPolicy<any> | FieldReadFunction<any>,
	totalStreams?: FieldPolicy<any> | FieldReadFunction<any>,
	totalUsers?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViewers?: FieldPolicy<any> | FieldReadFunction<any>
};
export type QueryKeySpecifier = ('billingStatus' | 'billingTiers' | 'clip' | 'clipEvents' | 'clipViewingUrls' | 'clips' | 'cluster' | 'clusters' | 'connectionEvents' | 'currentStreamHealth' | 'developerTokens' | 'dvrRequests' | 'geographicDistribution' | 'invoice' | 'invoices' | 'loadBalancingMetrics' | 'node' | 'nodeMetrics' | 'nodeMetrics1h' | 'nodes' | 'platformOverview' | 'rebufferingEvents' | 'recordingConfig' | 'recordings' | 'routingEvents' | 'serviceInstances' | 'stream' | 'streamAnalytics' | 'streamHealthAlerts' | 'streamHealthMetrics' | 'streamKeys' | 'streamQualityChanges' | 'streams' | 'tenant' | 'tenantClusterAssignments' | 'usageRecords' | 'validateStreamKey' | 'viewerGeographics' | 'viewerMetrics' | 'viewerMetrics5m' | QueryKeySpecifier)[];
export type QueryFieldPolicy = {
	billingStatus?: FieldPolicy<any> | FieldReadFunction<any>,
	billingTiers?: FieldPolicy<any> | FieldReadFunction<any>,
	clip?: FieldPolicy<any> | FieldReadFunction<any>,
	clipEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	clipViewingUrls?: FieldPolicy<any> | FieldReadFunction<any>,
	clips?: FieldPolicy<any> | FieldReadFunction<any>,
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	clusters?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	currentStreamHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	developerTokens?: FieldPolicy<any> | FieldReadFunction<any>,
	dvrRequests?: FieldPolicy<any> | FieldReadFunction<any>,
	geographicDistribution?: FieldPolicy<any> | FieldReadFunction<any>,
	invoice?: FieldPolicy<any> | FieldReadFunction<any>,
	invoices?: FieldPolicy<any> | FieldReadFunction<any>,
	loadBalancingMetrics?: FieldPolicy<any> | FieldReadFunction<any>,
	node?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeMetrics?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeMetrics1h?: FieldPolicy<any> | FieldReadFunction<any>,
	nodes?: FieldPolicy<any> | FieldReadFunction<any>,
	platformOverview?: FieldPolicy<any> | FieldReadFunction<any>,
	rebufferingEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	recordingConfig?: FieldPolicy<any> | FieldReadFunction<any>,
	recordings?: FieldPolicy<any> | FieldReadFunction<any>,
	routingEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	serviceInstances?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	streamAnalytics?: FieldPolicy<any> | FieldReadFunction<any>,
	streamHealthAlerts?: FieldPolicy<any> | FieldReadFunction<any>,
	streamHealthMetrics?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKeys?: FieldPolicy<any> | FieldReadFunction<any>,
	streamQualityChanges?: FieldPolicy<any> | FieldReadFunction<any>,
	streams?: FieldPolicy<any> | FieldReadFunction<any>,
	tenant?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantClusterAssignments?: FieldPolicy<any> | FieldReadFunction<any>,
	usageRecords?: FieldPolicy<any> | FieldReadFunction<any>,
	validateStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerGeographics?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics5m?: FieldPolicy<any> | FieldReadFunction<any>
};
export type RebufferingEventKeySpecifier = ('bufferState' | 'frameJitterMs' | 'healthScore' | 'nodeId' | 'packetLossPercentage' | 'previousState' | 'rebufferEnd' | 'rebufferStart' | 'stream' | 'timestamp' | RebufferingEventKeySpecifier)[];
export type RebufferingEventFieldPolicy = {
	bufferState?: FieldPolicy<any> | FieldReadFunction<any>,
	frameJitterMs?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	packetLossPercentage?: FieldPolicy<any> | FieldReadFunction<any>,
	previousState?: FieldPolicy<any> | FieldReadFunction<any>,
	rebufferEnd?: FieldPolicy<any> | FieldReadFunction<any>,
	rebufferStart?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type RecordingKeySpecifier = ('createdAt' | 'duration' | 'endTime' | 'fileSizeBytes' | 'id' | 'playbackId' | 'startTime' | 'status' | 'streamId' | 'thumbnailUrl' | 'title' | 'updatedAt' | RecordingKeySpecifier)[];
export type RecordingFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	duration?: FieldPolicy<any> | FieldReadFunction<any>,
	endTime?: FieldPolicy<any> | FieldReadFunction<any>,
	fileSizeBytes?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	startTime?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	thumbnailUrl?: FieldPolicy<any> | FieldReadFunction<any>,
	title?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type RecordingConfigKeySpecifier = ('enabled' | 'format' | 'retentionDays' | 'segmentDuration' | RecordingConfigKeySpecifier)[];
export type RecordingConfigFieldPolicy = {
	enabled?: FieldPolicy<any> | FieldReadFunction<any>,
	format?: FieldPolicy<any> | FieldReadFunction<any>,
	retentionDays?: FieldPolicy<any> | FieldReadFunction<any>,
	segmentDuration?: FieldPolicy<any> | FieldReadFunction<any>
};
export type RoutingEventKeySpecifier = ('clientCountry' | 'clientIp' | 'clientLatitude' | 'clientLongitude' | 'details' | 'nodeLatitude' | 'nodeLongitude' | 'nodeName' | 'score' | 'selectedNode' | 'status' | 'streamName' | 'timestamp' | RoutingEventKeySpecifier)[];
export type RoutingEventFieldPolicy = {
	clientCountry?: FieldPolicy<any> | FieldReadFunction<any>,
	clientIp?: FieldPolicy<any> | FieldReadFunction<any>,
	clientLatitude?: FieldPolicy<any> | FieldReadFunction<any>,
	clientLongitude?: FieldPolicy<any> | FieldReadFunction<any>,
	details?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeLatitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeLongitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeName?: FieldPolicy<any> | FieldReadFunction<any>,
	score?: FieldPolicy<any> | FieldReadFunction<any>,
	selectedNode?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamName?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ServiceInstanceKeySpecifier = ('cluster' | 'clusterId' | 'containerId' | 'cpuUsagePercent' | 'healthStatus' | 'id' | 'instanceId' | 'lastHealthCheck' | 'memoryUsageMb' | 'node' | 'nodeId' | 'port' | 'processId' | 'serviceId' | 'startedAt' | 'status' | 'stoppedAt' | 'version' | ServiceInstanceKeySpecifier)[];
export type ServiceInstanceFieldPolicy = {
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	clusterId?: FieldPolicy<any> | FieldReadFunction<any>,
	containerId?: FieldPolicy<any> | FieldReadFunction<any>,
	cpuUsagePercent?: FieldPolicy<any> | FieldReadFunction<any>,
	healthStatus?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	instanceId?: FieldPolicy<any> | FieldReadFunction<any>,
	lastHealthCheck?: FieldPolicy<any> | FieldReadFunction<any>,
	memoryUsageMb?: FieldPolicy<any> | FieldReadFunction<any>,
	node?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	port?: FieldPolicy<any> | FieldReadFunction<any>,
	processId?: FieldPolicy<any> | FieldReadFunction<any>,
	serviceId?: FieldPolicy<any> | FieldReadFunction<any>,
	startedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	stoppedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	version?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamKeySpecifier = ('createdAt' | 'description' | 'events' | 'health' | 'id' | 'name' | 'playbackId' | 'record' | 'recordings' | 'status' | 'streamKey' | 'updatedAt' | 'viewerMetrics5m' | StreamKeySpecifier)[];
export type StreamFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	events?: FieldPolicy<any> | FieldReadFunction<any>,
	health?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	record?: FieldPolicy<any> | FieldReadFunction<any>,
	recordings?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics5m?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamAnalyticsKeySpecifier = ('avgBitrate' | 'avgBufferHealth' | 'avgViewers' | 'bandwidthIn' | 'bandwidthOut' | 'bitrateKbps' | 'createdAt' | 'currentBufferState' | 'currentCodec' | 'currentFps' | 'currentHealthScore' | 'currentIssues' | 'currentResolution' | 'currentViewers' | 'downbytes' | 'firstMs' | 'id' | 'inputs' | 'internalName' | 'lastMs' | 'lastUpdated' | 'latitude' | 'location' | 'longitude' | 'mistStatus' | 'nodeId' | 'nodeName' | 'outputs' | 'packetLossRate' | 'packetsLost' | 'packetsRetrans' | 'packetsSent' | 'peakViewers' | 'qualityTier' | 'resolution' | 'sessionEndTime' | 'sessionStartTime' | 'status' | 'streamId' | 'tenantId' | 'totalBandwidthGb' | 'totalConnections' | 'totalSessionDuration' | 'trackCount' | 'uniqueCities' | 'uniqueCountries' | 'upbytes' | StreamAnalyticsKeySpecifier)[];
export type StreamAnalyticsFieldPolicy = {
	avgBitrate?: FieldPolicy<any> | FieldReadFunction<any>,
	avgBufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	avgViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	bandwidthIn?: FieldPolicy<any> | FieldReadFunction<any>,
	bandwidthOut?: FieldPolicy<any> | FieldReadFunction<any>,
	bitrateKbps?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	currentBufferState?: FieldPolicy<any> | FieldReadFunction<any>,
	currentCodec?: FieldPolicy<any> | FieldReadFunction<any>,
	currentFps?: FieldPolicy<any> | FieldReadFunction<any>,
	currentHealthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	currentIssues?: FieldPolicy<any> | FieldReadFunction<any>,
	currentResolution?: FieldPolicy<any> | FieldReadFunction<any>,
	currentViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	downbytes?: FieldPolicy<any> | FieldReadFunction<any>,
	firstMs?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	inputs?: FieldPolicy<any> | FieldReadFunction<any>,
	internalName?: FieldPolicy<any> | FieldReadFunction<any>,
	lastMs?: FieldPolicy<any> | FieldReadFunction<any>,
	lastUpdated?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	location?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	mistStatus?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeName?: FieldPolicy<any> | FieldReadFunction<any>,
	outputs?: FieldPolicy<any> | FieldReadFunction<any>,
	packetLossRate?: FieldPolicy<any> | FieldReadFunction<any>,
	packetsLost?: FieldPolicy<any> | FieldReadFunction<any>,
	packetsRetrans?: FieldPolicy<any> | FieldReadFunction<any>,
	packetsSent?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	qualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	resolution?: FieldPolicy<any> | FieldReadFunction<any>,
	sessionEndTime?: FieldPolicy<any> | FieldReadFunction<any>,
	sessionStartTime?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	totalBandwidthGb?: FieldPolicy<any> | FieldReadFunction<any>,
	totalConnections?: FieldPolicy<any> | FieldReadFunction<any>,
	totalSessionDuration?: FieldPolicy<any> | FieldReadFunction<any>,
	trackCount?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCities?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCountries?: FieldPolicy<any> | FieldReadFunction<any>,
	upbytes?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamEventKeySpecifier = ('details' | 'status' | 'stream' | 'timestamp' | 'type' | StreamEventKeySpecifier)[];
export type StreamEventFieldPolicy = {
	details?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	type?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamHealthAlertKeySpecifier = ('alertType' | 'bufferState' | 'frameJitterMs' | 'healthScore' | 'issuesDescription' | 'nodeId' | 'packetLossPercentage' | 'qualityTier' | 'severity' | 'stream' | 'timestamp' | StreamHealthAlertKeySpecifier)[];
export type StreamHealthAlertFieldPolicy = {
	alertType?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferState?: FieldPolicy<any> | FieldReadFunction<any>,
	frameJitterMs?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	issuesDescription?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	packetLossPercentage?: FieldPolicy<any> | FieldReadFunction<any>,
	qualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	severity?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamHealthMetricKeySpecifier = ('audioBitrate' | 'audioChannels' | 'audioCodec' | 'audioSampleRate' | 'bitrate' | 'bufferHealth' | 'bufferState' | 'codec' | 'fps' | 'frameJitterMs' | 'hasIssues' | 'healthScore' | 'height' | 'issuesDescription' | 'keyframeStabilityMs' | 'nodeId' | 'packetLossPercentage' | 'packetsLost' | 'packetsSent' | 'qualityTier' | 'stream' | 'timestamp' | 'trackMetadata' | 'width' | StreamHealthMetricKeySpecifier)[];
export type StreamHealthMetricFieldPolicy = {
	audioBitrate?: FieldPolicy<any> | FieldReadFunction<any>,
	audioChannels?: FieldPolicy<any> | FieldReadFunction<any>,
	audioCodec?: FieldPolicy<any> | FieldReadFunction<any>,
	audioSampleRate?: FieldPolicy<any> | FieldReadFunction<any>,
	bitrate?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferState?: FieldPolicy<any> | FieldReadFunction<any>,
	codec?: FieldPolicy<any> | FieldReadFunction<any>,
	fps?: FieldPolicy<any> | FieldReadFunction<any>,
	frameJitterMs?: FieldPolicy<any> | FieldReadFunction<any>,
	hasIssues?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	height?: FieldPolicy<any> | FieldReadFunction<any>,
	issuesDescription?: FieldPolicy<any> | FieldReadFunction<any>,
	keyframeStabilityMs?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	packetLossPercentage?: FieldPolicy<any> | FieldReadFunction<any>,
	packetsLost?: FieldPolicy<any> | FieldReadFunction<any>,
	packetsSent?: FieldPolicy<any> | FieldReadFunction<any>,
	qualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	trackMetadata?: FieldPolicy<any> | FieldReadFunction<any>,
	width?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamKeyKeySpecifier = ('createdAt' | 'id' | 'isActive' | 'keyName' | 'keyValue' | 'lastUsedAt' | 'streamId' | StreamKeyKeySpecifier)[];
export type StreamKeyFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	isActive?: FieldPolicy<any> | FieldReadFunction<any>,
	keyName?: FieldPolicy<any> | FieldReadFunction<any>,
	keyValue?: FieldPolicy<any> | FieldReadFunction<any>,
	lastUsedAt?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamQualityChangeKeySpecifier = ('changeType' | 'newCodec' | 'newQualityTier' | 'newResolution' | 'newTracks' | 'nodeId' | 'previousCodec' | 'previousQualityTier' | 'previousResolution' | 'previousTracks' | 'stream' | 'timestamp' | StreamQualityChangeKeySpecifier)[];
export type StreamQualityChangeFieldPolicy = {
	changeType?: FieldPolicy<any> | FieldReadFunction<any>,
	newCodec?: FieldPolicy<any> | FieldReadFunction<any>,
	newQualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	newResolution?: FieldPolicy<any> | FieldReadFunction<any>,
	newTracks?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	previousCodec?: FieldPolicy<any> | FieldReadFunction<any>,
	previousQualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	previousResolution?: FieldPolicy<any> | FieldReadFunction<any>,
	previousTracks?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamValidationKeySpecifier = ('error' | 'streamKey' | 'valid' | StreamValidationKeySpecifier)[];
export type StreamValidationFieldPolicy = {
	error?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	valid?: FieldPolicy<any> | FieldReadFunction<any>
};
export type SubscriptionKeySpecifier = ('clipLifecycle' | 'dvrLifecycle' | 'streamEvents' | 'systemHealth' | 'trackListUpdates' | 'viewerMetrics' | SubscriptionKeySpecifier)[];
export type SubscriptionFieldPolicy = {
	clipLifecycle?: FieldPolicy<any> | FieldReadFunction<any>,
	dvrLifecycle?: FieldPolicy<any> | FieldReadFunction<any>,
	streamEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	systemHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	trackListUpdates?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics?: FieldPolicy<any> | FieldReadFunction<any>
};
export type SystemHealthEventKeySpecifier = ('cluster' | 'cpuUsage' | 'diskUsage' | 'healthScore' | 'memoryUsage' | 'node' | 'status' | 'timestamp' | SystemHealthEventKeySpecifier)[];
export type SystemHealthEventFieldPolicy = {
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	cpuUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	diskUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	memoryUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	node?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TenantKeySpecifier = ('cluster' | 'createdAt' | 'id' | 'name' | 'settings' | TenantKeySpecifier)[];
export type TenantFieldPolicy = {
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	settings?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TenantClusterAssignmentKeySpecifier = ('clusterId' | 'createdAt' | 'deploymentTier' | 'fallbackWhenFull' | 'id' | 'isActive' | 'isPrimary' | 'maxBandwidthMbpsOnCluster' | 'maxStreamsOnCluster' | 'maxViewersOnCluster' | 'priority' | 'tenantId' | 'updatedAt' | TenantClusterAssignmentKeySpecifier)[];
export type TenantClusterAssignmentFieldPolicy = {
	clusterId?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	deploymentTier?: FieldPolicy<any> | FieldReadFunction<any>,
	fallbackWhenFull?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	isActive?: FieldPolicy<any> | FieldReadFunction<any>,
	isPrimary?: FieldPolicy<any> | FieldReadFunction<any>,
	maxBandwidthMbpsOnCluster?: FieldPolicy<any> | FieldReadFunction<any>,
	maxStreamsOnCluster?: FieldPolicy<any> | FieldReadFunction<any>,
	maxViewersOnCluster?: FieldPolicy<any> | FieldReadFunction<any>,
	priority?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TimeRangeKeySpecifier = ('end' | 'start' | TimeRangeKeySpecifier)[];
export type TimeRangeFieldPolicy = {
	end?: FieldPolicy<any> | FieldReadFunction<any>,
	start?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TrackListEventKeySpecifier = ('stream' | 'timestamp' | 'trackCount' | 'trackList' | TrackListEventKeySpecifier)[];
export type TrackListEventFieldPolicy = {
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	trackCount?: FieldPolicy<any> | FieldReadFunction<any>,
	trackList?: FieldPolicy<any> | FieldReadFunction<any>
};
export type UsageRecordKeySpecifier = ('cost' | 'id' | 'quantity' | 'resourceType' | 'timestamp' | 'unit' | UsageRecordKeySpecifier)[];
export type UsageRecordFieldPolicy = {
	cost?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	quantity?: FieldPolicy<any> | FieldReadFunction<any>,
	resourceType?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	unit?: FieldPolicy<any> | FieldReadFunction<any>
};
export type UserKeySpecifier = ('createdAt' | 'email' | 'id' | 'name' | 'role' | UserKeySpecifier)[];
export type UserFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	email?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	role?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerGeographicKeySpecifier = ('city' | 'connectionAddr' | 'countryCode' | 'eventType' | 'latitude' | 'longitude' | 'nodeId' | 'source' | 'stream' | 'timestamp' | 'viewerCount' | ViewerGeographicKeySpecifier)[];
export type ViewerGeographicFieldPolicy = {
	city?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionAddr?: FieldPolicy<any> | FieldReadFunction<any>,
	countryCode?: FieldPolicy<any> | FieldReadFunction<any>,
	eventType?: FieldPolicy<any> | FieldReadFunction<any>,
	latitude?: FieldPolicy<any> | FieldReadFunction<any>,
	longitude?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	source?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetricKeySpecifier = ('timestamp' | 'viewerCount' | ViewerMetricKeySpecifier)[];
export type ViewerMetricFieldPolicy = {
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetricsKeySpecifier = ('bandwidth' | 'bufferHealth' | 'connectionQuality' | 'currentViewers' | 'peakViewers' | 'stream' | 'timestamp' | 'viewerCount' | ViewerMetricsKeySpecifier)[];
export type ViewerMetricsFieldPolicy = {
	bandwidth?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionQuality?: FieldPolicy<any> | FieldReadFunction<any>,
	currentViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetrics5mKeySpecifier = ('avgBufferHealth' | 'avgConnectionQuality' | 'avgViewers' | 'internalName' | 'nodeId' | 'peakViewers' | 'timestamp' | 'uniqueCities' | 'uniqueCountries' | ViewerMetrics5mKeySpecifier)[];
export type ViewerMetrics5mFieldPolicy = {
	avgBufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	avgConnectionQuality?: FieldPolicy<any> | FieldReadFunction<any>,
	avgViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	internalName?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCities?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueCountries?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StrictTypedTypePolicies = {
	BillingStatus?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | BillingStatusKeySpecifier | (() => undefined | BillingStatusKeySpecifier),
		fields?: BillingStatusFieldPolicy,
	},
	BillingTier?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | BillingTierKeySpecifier | (() => undefined | BillingTierKeySpecifier),
		fields?: BillingTierFieldPolicy,
	},
	CityMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | CityMetricKeySpecifier | (() => undefined | CityMetricKeySpecifier),
		fields?: CityMetricFieldPolicy,
	},
	Clip?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClipKeySpecifier | (() => undefined | ClipKeySpecifier),
		fields?: ClipFieldPolicy,
	},
	ClipEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClipEventKeySpecifier | (() => undefined | ClipEventKeySpecifier),
		fields?: ClipEventFieldPolicy,
	},
	ClipViewingUrls?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClipViewingUrlsKeySpecifier | (() => undefined | ClipViewingUrlsKeySpecifier),
		fields?: ClipViewingUrlsFieldPolicy,
	},
	Cluster?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClusterKeySpecifier | (() => undefined | ClusterKeySpecifier),
		fields?: ClusterFieldPolicy,
	},
	ConnectionEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ConnectionEventKeySpecifier | (() => undefined | ConnectionEventKeySpecifier),
		fields?: ConnectionEventFieldPolicy,
	},
	CountryMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | CountryMetricKeySpecifier | (() => undefined | CountryMetricKeySpecifier),
		fields?: CountryMetricFieldPolicy,
	},
	CountryTimeSeries?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | CountryTimeSeriesKeySpecifier | (() => undefined | CountryTimeSeriesKeySpecifier),
		fields?: CountryTimeSeriesFieldPolicy,
	},
	DVRRequest?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | DVRRequestKeySpecifier | (() => undefined | DVRRequestKeySpecifier),
		fields?: DVRRequestFieldPolicy,
	},
	DVRRequestList?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | DVRRequestListKeySpecifier | (() => undefined | DVRRequestListKeySpecifier),
		fields?: DVRRequestListFieldPolicy,
	},
	DeveloperToken?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | DeveloperTokenKeySpecifier | (() => undefined | DeveloperTokenKeySpecifier),
		fields?: DeveloperTokenFieldPolicy,
	},
	GeographicDistribution?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | GeographicDistributionKeySpecifier | (() => undefined | GeographicDistributionKeySpecifier),
		fields?: GeographicDistributionFieldPolicy,
	},
	Invoice?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | InvoiceKeySpecifier | (() => undefined | InvoiceKeySpecifier),
		fields?: InvoiceFieldPolicy,
	},
	LineItem?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | LineItemKeySpecifier | (() => undefined | LineItemKeySpecifier),
		fields?: LineItemFieldPolicy,
	},
	LoadBalancingMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | LoadBalancingMetricKeySpecifier | (() => undefined | LoadBalancingMetricKeySpecifier),
		fields?: LoadBalancingMetricFieldPolicy,
	},
	Mutation?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | MutationKeySpecifier | (() => undefined | MutationKeySpecifier),
		fields?: MutationFieldPolicy,
	},
	Node?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | NodeKeySpecifier | (() => undefined | NodeKeySpecifier),
		fields?: NodeFieldPolicy,
	},
	NodeMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | NodeMetricKeySpecifier | (() => undefined | NodeMetricKeySpecifier),
		fields?: NodeMetricFieldPolicy,
	},
	NodeMetricHourly?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | NodeMetricHourlyKeySpecifier | (() => undefined | NodeMetricHourlyKeySpecifier),
		fields?: NodeMetricHourlyFieldPolicy,
	},
	Payment?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | PaymentKeySpecifier | (() => undefined | PaymentKeySpecifier),
		fields?: PaymentFieldPolicy,
	},
	PlatformOverview?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | PlatformOverviewKeySpecifier | (() => undefined | PlatformOverviewKeySpecifier),
		fields?: PlatformOverviewFieldPolicy,
	},
	Query?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | QueryKeySpecifier | (() => undefined | QueryKeySpecifier),
		fields?: QueryFieldPolicy,
	},
	RebufferingEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | RebufferingEventKeySpecifier | (() => undefined | RebufferingEventKeySpecifier),
		fields?: RebufferingEventFieldPolicy,
	},
	Recording?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | RecordingKeySpecifier | (() => undefined | RecordingKeySpecifier),
		fields?: RecordingFieldPolicy,
	},
	RecordingConfig?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | RecordingConfigKeySpecifier | (() => undefined | RecordingConfigKeySpecifier),
		fields?: RecordingConfigFieldPolicy,
	},
	RoutingEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | RoutingEventKeySpecifier | (() => undefined | RoutingEventKeySpecifier),
		fields?: RoutingEventFieldPolicy,
	},
	ServiceInstance?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ServiceInstanceKeySpecifier | (() => undefined | ServiceInstanceKeySpecifier),
		fields?: ServiceInstanceFieldPolicy,
	},
	Stream?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamKeySpecifier | (() => undefined | StreamKeySpecifier),
		fields?: StreamFieldPolicy,
	},
	StreamAnalytics?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamAnalyticsKeySpecifier | (() => undefined | StreamAnalyticsKeySpecifier),
		fields?: StreamAnalyticsFieldPolicy,
	},
	StreamEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamEventKeySpecifier | (() => undefined | StreamEventKeySpecifier),
		fields?: StreamEventFieldPolicy,
	},
	StreamHealthAlert?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamHealthAlertKeySpecifier | (() => undefined | StreamHealthAlertKeySpecifier),
		fields?: StreamHealthAlertFieldPolicy,
	},
	StreamHealthMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamHealthMetricKeySpecifier | (() => undefined | StreamHealthMetricKeySpecifier),
		fields?: StreamHealthMetricFieldPolicy,
	},
	StreamKey?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamKeyKeySpecifier | (() => undefined | StreamKeyKeySpecifier),
		fields?: StreamKeyFieldPolicy,
	},
	StreamQualityChange?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamQualityChangeKeySpecifier | (() => undefined | StreamQualityChangeKeySpecifier),
		fields?: StreamQualityChangeFieldPolicy,
	},
	StreamValidation?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamValidationKeySpecifier | (() => undefined | StreamValidationKeySpecifier),
		fields?: StreamValidationFieldPolicy,
	},
	Subscription?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | SubscriptionKeySpecifier | (() => undefined | SubscriptionKeySpecifier),
		fields?: SubscriptionFieldPolicy,
	},
	SystemHealthEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | SystemHealthEventKeySpecifier | (() => undefined | SystemHealthEventKeySpecifier),
		fields?: SystemHealthEventFieldPolicy,
	},
	Tenant?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | TenantKeySpecifier | (() => undefined | TenantKeySpecifier),
		fields?: TenantFieldPolicy,
	},
	TenantClusterAssignment?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | TenantClusterAssignmentKeySpecifier | (() => undefined | TenantClusterAssignmentKeySpecifier),
		fields?: TenantClusterAssignmentFieldPolicy,
	},
	TimeRange?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | TimeRangeKeySpecifier | (() => undefined | TimeRangeKeySpecifier),
		fields?: TimeRangeFieldPolicy,
	},
	TrackListEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | TrackListEventKeySpecifier | (() => undefined | TrackListEventKeySpecifier),
		fields?: TrackListEventFieldPolicy,
	},
	UsageRecord?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | UsageRecordKeySpecifier | (() => undefined | UsageRecordKeySpecifier),
		fields?: UsageRecordFieldPolicy,
	},
	User?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | UserKeySpecifier | (() => undefined | UserKeySpecifier),
		fields?: UserFieldPolicy,
	},
	ViewerGeographic?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerGeographicKeySpecifier | (() => undefined | ViewerGeographicKeySpecifier),
		fields?: ViewerGeographicFieldPolicy,
	},
	ViewerMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerMetricKeySpecifier | (() => undefined | ViewerMetricKeySpecifier),
		fields?: ViewerMetricFieldPolicy,
	},
	ViewerMetrics?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerMetricsKeySpecifier | (() => undefined | ViewerMetricsKeySpecifier),
		fields?: ViewerMetricsFieldPolicy,
	},
	ViewerMetrics5m?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerMetrics5mKeySpecifier | (() => undefined | ViewerMetrics5mKeySpecifier),
		fields?: ViewerMetrics5mFieldPolicy,
	}
};
export type TypedTypePolicies = StrictTypedTypePolicies & TypePolicies;
export const ClipInfoFragmentDoc = gql`
    fragment ClipInfo on Clip {
  id
  stream
  title
  description
  startTime
  endTime
  duration
  playbackId
  status
  createdAt
  updatedAt
}
    `;
export const DvrRequestInfoFragmentDoc = gql`
    fragment DVRRequestInfo on DVRRequest {
  dvrHash
  internalName
  storageNodeId
  status
  startedAt
  endedAt
  durationSeconds
  sizeBytes
  manifestPath
  errorMessage
  createdAt
  updatedAt
}
    `;
export const RecordingConfigInfoFragmentDoc = gql`
    fragment RecordingConfigInfo on RecordingConfig {
  enabled
  retentionDays
  format
  segmentDuration
}
    `;
export const ServiceInstanceInfoFragmentDoc = gql`
    fragment ServiceInstanceInfo on ServiceInstance {
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
    `;
export const TenantClusterAssignmentInfoFragmentDoc = gql`
    fragment TenantClusterAssignmentInfo on TenantClusterAssignment {
  id
  tenantId
  clusterId
  deploymentTier
  priority
  isPrimary
  isActive
  maxStreamsOnCluster
  maxViewersOnCluster
  maxBandwidthMbpsOnCluster
  fallbackWhenFull
  createdAt
  updatedAt
}
    `;
export const TypeRefFragmentDoc = gql`
    fragment TypeRef on __Type {
  kind
  name
  ofType {
    kind
    name
    ofType {
      kind
      name
      ofType {
        kind
        name
        ofType {
          kind
          name
          ofType {
            kind
            name
            ofType {
              kind
              name
              ofType {
                kind
                name
              }
            }
          }
        }
      }
    }
  }
}
    `;
export const InputValueFragmentDoc = gql`
    fragment InputValue on __InputValue {
  name
  description
  type {
    ...TypeRef
  }
  defaultValue
}
    ${TypeRefFragmentDoc}`;
export const FullTypeFragmentDoc = gql`
    fragment FullType on __Type {
  kind
  name
  description
  fields(includeDeprecated: true) {
    name
    description
    args {
      ...InputValue
    }
    type {
      ...TypeRef
    }
    isDeprecated
    deprecationReason
  }
  inputFields {
    ...InputValue
  }
  interfaces {
    ...TypeRef
  }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes {
    ...TypeRef
  }
}
    ${InputValueFragmentDoc}
${TypeRefFragmentDoc}`;
export const StreamInfoFragmentDoc = gql`
    fragment StreamInfo on Stream {
  id
  name
  description
  streamKey
  playbackId
  status
  record
  createdAt
  updatedAt
}
    `;
export const StreamKeyInfoFragmentDoc = gql`
    fragment StreamKeyInfo on StreamKey {
  id
  streamId
  keyValue
  keyName
  isActive
  lastUsedAt
  createdAt
}
    `;
export const RecordingInfoFragmentDoc = gql`
    fragment RecordingInfo on Recording {
  id
  streamId
  title
  duration
  fileSizeBytes
  playbackId
  thumbnailUrl
  startTime
  endTime
  status
  createdAt
  updatedAt
}
    `;
export const CreatePaymentDocument = gql`
    mutation CreatePayment($input: CreatePaymentInput!) {
  createPayment(input: $input) {
    id
    amount
    currency
    method
    status
    createdAt
  }
}
    `;
export type CreatePaymentMutationFn = Apollo.MutationFunction<CreatePaymentMutation, CreatePaymentMutationVariables>;
export type CreatePaymentMutationResult = Apollo.MutationResult<CreatePaymentMutation>;
export type CreatePaymentMutationOptions = Apollo.BaseMutationOptions<CreatePaymentMutation, CreatePaymentMutationVariables>;
export const UpdateBillingTierDocument = gql`
    mutation UpdateBillingTier($tierId: ID!) {
  updateBillingTier(tierId: $tierId) {
    currentTier {
      id
      name
      price
      currency
      features
    }
    nextBillingDate
    outstandingAmount
    status
  }
}
    `;
export type UpdateBillingTierMutationFn = Apollo.MutationFunction<UpdateBillingTierMutation, UpdateBillingTierMutationVariables>;
export type UpdateBillingTierMutationResult = Apollo.MutationResult<UpdateBillingTierMutation>;
export type UpdateBillingTierMutationOptions = Apollo.BaseMutationOptions<UpdateBillingTierMutation, UpdateBillingTierMutationVariables>;
export const CreateClipDocument = gql`
    mutation CreateClip($input: CreateClipInput!) {
  createClip(input: $input) {
    id
    stream
    title
    description
    startTime
    endTime
    duration
    playbackId
    status
    createdAt
    updatedAt
  }
}
    `;
export type CreateClipMutationFn = Apollo.MutationFunction<CreateClipMutation, CreateClipMutationVariables>;
export type CreateClipMutationResult = Apollo.MutationResult<CreateClipMutation>;
export type CreateClipMutationOptions = Apollo.BaseMutationOptions<CreateClipMutation, CreateClipMutationVariables>;
export const DeleteClipDocument = gql`
    mutation DeleteClip($id: ID!) {
  deleteClip(id: $id)
}
    `;
export type DeleteClipMutationFn = Apollo.MutationFunction<DeleteClipMutation, DeleteClipMutationVariables>;
export type DeleteClipMutationResult = Apollo.MutationResult<DeleteClipMutation>;
export type DeleteClipMutationOptions = Apollo.BaseMutationOptions<DeleteClipMutation, DeleteClipMutationVariables>;
export const CreateApiTokenDocument = gql`
    mutation CreateAPIToken($input: CreateDeveloperTokenInput!) {
  createDeveloperToken(input: $input) {
    id
    name
    token
    permissions
    expiresAt
    createdAt
  }
}
    `;
export type CreateApiTokenMutationFn = Apollo.MutationFunction<CreateApiTokenMutation, CreateApiTokenMutationVariables>;
export type CreateApiTokenMutationResult = Apollo.MutationResult<CreateApiTokenMutation>;
export type CreateApiTokenMutationOptions = Apollo.BaseMutationOptions<CreateApiTokenMutation, CreateApiTokenMutationVariables>;
export const RevokeApiTokenDocument = gql`
    mutation RevokeAPIToken($id: ID!) {
  revokeDeveloperToken(id: $id)
}
    `;
export type RevokeApiTokenMutationFn = Apollo.MutationFunction<RevokeApiTokenMutation, RevokeApiTokenMutationVariables>;
export type RevokeApiTokenMutationResult = Apollo.MutationResult<RevokeApiTokenMutation>;
export type RevokeApiTokenMutationOptions = Apollo.BaseMutationOptions<RevokeApiTokenMutation, RevokeApiTokenMutationVariables>;
export const StartDvrDocument = gql`
    mutation StartDVR($internalName: String!, $streamId: ID) {
  startDVR(internalName: $internalName, streamId: $streamId) {
    dvrHash
    internalName
    storageNodeId
    status
    startedAt
    endedAt
    durationSeconds
    sizeBytes
    manifestPath
    errorMessage
    createdAt
    updatedAt
  }
}
    `;
export type StartDvrMutationFn = Apollo.MutationFunction<StartDvrMutation, StartDvrMutationVariables>;
export type StartDvrMutationResult = Apollo.MutationResult<StartDvrMutation>;
export type StartDvrMutationOptions = Apollo.BaseMutationOptions<StartDvrMutation, StartDvrMutationVariables>;
export const StopDvrDocument = gql`
    mutation StopDVR($dvrHash: ID!) {
  stopDVR(dvrHash: $dvrHash)
}
    `;
export type StopDvrMutationFn = Apollo.MutationFunction<StopDvrMutation, StopDvrMutationVariables>;
export type StopDvrMutationResult = Apollo.MutationResult<StopDvrMutation>;
export type StopDvrMutationOptions = Apollo.BaseMutationOptions<StopDvrMutation, StopDvrMutationVariables>;
export const SetStreamRecordingConfigDocument = gql`
    mutation SetStreamRecordingConfig($internalName: String!, $enabled: Boolean!, $retentionDays: Int, $format: String, $segmentDuration: Int) {
  setStreamRecordingConfig(
    internalName: $internalName
    enabled: $enabled
    retentionDays: $retentionDays
    format: $format
    segmentDuration: $segmentDuration
  ) {
    enabled
    retentionDays
    format
    segmentDuration
  }
}
    `;
export type SetStreamRecordingConfigMutationFn = Apollo.MutationFunction<SetStreamRecordingConfigMutation, SetStreamRecordingConfigMutationVariables>;
export type SetStreamRecordingConfigMutationResult = Apollo.MutationResult<SetStreamRecordingConfigMutation>;
export type SetStreamRecordingConfigMutationOptions = Apollo.BaseMutationOptions<SetStreamRecordingConfigMutation, SetStreamRecordingConfigMutationVariables>;
export const UpdateTenantDocument = gql`
    mutation UpdateTenant($input: UpdateTenantInput!) {
  updateTenant(input: $input) {
    id
    name
    settings
    cluster
    createdAt
  }
}
    `;
export type UpdateTenantMutationFn = Apollo.MutationFunction<UpdateTenantMutation, UpdateTenantMutationVariables>;
export type UpdateTenantMutationResult = Apollo.MutationResult<UpdateTenantMutation>;
export type UpdateTenantMutationOptions = Apollo.BaseMutationOptions<UpdateTenantMutation, UpdateTenantMutationVariables>;
export const CreateStreamDocument = gql`
    mutation CreateStream($input: CreateStreamInput!) {
  createStream(input: $input) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}
    `;
export type CreateStreamMutationFn = Apollo.MutationFunction<CreateStreamMutation, CreateStreamMutationVariables>;
export type CreateStreamMutationResult = Apollo.MutationResult<CreateStreamMutation>;
export type CreateStreamMutationOptions = Apollo.BaseMutationOptions<CreateStreamMutation, CreateStreamMutationVariables>;
export const UpdateStreamDocument = gql`
    mutation UpdateStream($id: ID!, $input: UpdateStreamInput!) {
  updateStream(id: $id, input: $input) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}
    `;
export type UpdateStreamMutationFn = Apollo.MutationFunction<UpdateStreamMutation, UpdateStreamMutationVariables>;
export type UpdateStreamMutationResult = Apollo.MutationResult<UpdateStreamMutation>;
export type UpdateStreamMutationOptions = Apollo.BaseMutationOptions<UpdateStreamMutation, UpdateStreamMutationVariables>;
export const DeleteStreamDocument = gql`
    mutation DeleteStream($id: ID!) {
  deleteStream(id: $id)
}
    `;
export type DeleteStreamMutationFn = Apollo.MutationFunction<DeleteStreamMutation, DeleteStreamMutationVariables>;
export type DeleteStreamMutationResult = Apollo.MutationResult<DeleteStreamMutation>;
export type DeleteStreamMutationOptions = Apollo.BaseMutationOptions<DeleteStreamMutation, DeleteStreamMutationVariables>;
export const RefreshStreamKeyDocument = gql`
    mutation RefreshStreamKey($id: ID!) {
  refreshStreamKey(id: $id) {
    id
    name
    description
    streamKey
    playbackId
    status
    record
    createdAt
    updatedAt
  }
}
    `;
export type RefreshStreamKeyMutationFn = Apollo.MutationFunction<RefreshStreamKeyMutation, RefreshStreamKeyMutationVariables>;
export type RefreshStreamKeyMutationResult = Apollo.MutationResult<RefreshStreamKeyMutation>;
export type RefreshStreamKeyMutationOptions = Apollo.BaseMutationOptions<RefreshStreamKeyMutation, RefreshStreamKeyMutationVariables>;
export const CreateStreamKeyDocument = gql`
    mutation CreateStreamKey($streamId: ID!, $input: CreateStreamKeyInput!) {
  createStreamKey(streamId: $streamId, input: $input) {
    id
    streamId
    keyValue
    keyName
    isActive
    lastUsedAt
    createdAt
  }
}
    `;
export type CreateStreamKeyMutationFn = Apollo.MutationFunction<CreateStreamKeyMutation, CreateStreamKeyMutationVariables>;
export type CreateStreamKeyMutationResult = Apollo.MutationResult<CreateStreamKeyMutation>;
export type CreateStreamKeyMutationOptions = Apollo.BaseMutationOptions<CreateStreamKeyMutation, CreateStreamKeyMutationVariables>;
export const DeleteStreamKeyDocument = gql`
    mutation DeleteStreamKey($streamId: ID!, $keyId: ID!) {
  deleteStreamKey(streamId: $streamId, keyId: $keyId)
}
    `;
export type DeleteStreamKeyMutationFn = Apollo.MutationFunction<DeleteStreamKeyMutation, DeleteStreamKeyMutationVariables>;
export type DeleteStreamKeyMutationResult = Apollo.MutationResult<DeleteStreamKeyMutation>;
export type DeleteStreamKeyMutationOptions = Apollo.BaseMutationOptions<DeleteStreamKeyMutation, DeleteStreamKeyMutationVariables>;
export const GetStreamAnalyticsDocument = gql`
    query GetStreamAnalytics($stream: String!, $timeRange: TimeRangeInput) {
  streamAnalytics(stream: $stream, timeRange: $timeRange) {
    id
    tenantId
    streamId
    internalName
    sessionStartTime
    sessionEndTime
    totalSessionDuration
    currentViewers
    peakViewers
    totalConnections
    bandwidthIn
    bandwidthOut
    totalBandwidthGb
    upbytes
    downbytes
    bitrateKbps
    resolution
    packetsSent
    packetsLost
    packetsRetrans
    firstMs
    lastMs
    trackCount
    inputs
    outputs
    nodeId
    nodeName
    latitude
    longitude
    location
    status
    lastUpdated
    createdAt
    currentHealthScore
    currentBufferState
    currentIssues
    currentCodec
    currentFps
    currentResolution
    mistStatus
    qualityTier
    avgViewers
    uniqueCountries
    uniqueCities
    avgBufferHealth
    avgBitrate
    packetLossRate
  }
}
    `;
export type GetStreamAnalyticsQueryResult = Apollo.QueryResult<GetStreamAnalyticsQuery, GetStreamAnalyticsQueryVariables>;
export const GetViewerMetricsDocument = gql`
    query GetViewerMetrics($stream: String, $timeRange: TimeRangeInput) {
  viewerMetrics(stream: $stream, timeRange: $timeRange) {
    timestamp
    viewerCount
  }
}
    `;
export type GetViewerMetricsQueryResult = Apollo.QueryResult<GetViewerMetricsQuery, GetViewerMetricsQueryVariables>;
export const GetPlatformOverviewDocument = gql`
    query GetPlatformOverview($timeRange: TimeRangeInput) {
  platformOverview(timeRange: $timeRange) {
    totalStreams
    totalViewers
    totalBandwidth
    totalUsers
    timeRange {
      start
      end
    }
  }
}
    `;
export type GetPlatformOverviewQueryResult = Apollo.QueryResult<GetPlatformOverviewQuery, GetPlatformOverviewQueryVariables>;
export const GetUsageRecordsDocument = gql`
    query GetUsageRecords($timeRange: TimeRangeInput) {
  usageRecords(timeRange: $timeRange) {
    id
    resourceType
    quantity
    unit
    cost
    timestamp
  }
}
    `;
export type GetUsageRecordsQueryResult = Apollo.QueryResult<GetUsageRecordsQuery, GetUsageRecordsQueryVariables>;
export const GetViewerGeographicsDocument = gql`
    query GetViewerGeographics($stream: String, $timeRange: TimeRangeInput) {
  viewerGeographics(stream: $stream, timeRange: $timeRange) {
    timestamp
    stream
    nodeId
    countryCode
    city
    latitude
    longitude
    viewerCount
    connectionAddr
    eventType
    source
  }
}
    `;
export type GetViewerGeographicsQueryResult = Apollo.QueryResult<GetViewerGeographicsQuery, GetViewerGeographicsQueryVariables>;
export const GetGeographicDistributionDocument = gql`
    query GetGeographicDistribution($stream: String, $timeRange: TimeRangeInput) {
  geographicDistribution(stream: $stream, timeRange: $timeRange) {
    timeRange {
      start
      end
    }
    stream
    topCountries {
      countryCode
      viewerCount
      percentage
      cities {
        city
        countryCode
        viewerCount
        percentage
        latitude
        longitude
      }
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
    `;
export type GetGeographicDistributionQueryResult = Apollo.QueryResult<GetGeographicDistributionQuery, GetGeographicDistributionQueryVariables>;
export const GetLoadBalancingMetricsDocument = gql`
    query GetLoadBalancingMetrics($timeRange: TimeRangeInput) {
  loadBalancingMetrics(timeRange: $timeRange) {
    timestamp
    stream
    selectedNode
    nodeId
    clientIp
    clientCountry
    clientLatitude
    clientLongitude
    nodeLatitude
    nodeLongitude
    score
    status
    details
    routingDistance
    eventType
    source
  }
}
    `;
export type GetLoadBalancingMetricsQueryResult = Apollo.QueryResult<GetLoadBalancingMetricsQuery, GetLoadBalancingMetricsQueryVariables>;
export const GetBillingTiersDocument = gql`
    query GetBillingTiers {
  billingTiers {
    id
    name
    description
    price
    currency
    features
  }
}
    `;
export type GetBillingTiersQueryResult = Apollo.QueryResult<GetBillingTiersQuery, GetBillingTiersQueryVariables>;
export const GetBillingStatusDocument = gql`
    query GetBillingStatus {
  billingStatus {
    currentTier {
      id
      name
      price
      currency
      features
    }
    nextBillingDate
    outstandingAmount
    status
  }
}
    `;
export type GetBillingStatusQueryResult = Apollo.QueryResult<GetBillingStatusQuery, GetBillingStatusQueryVariables>;
export const GetInvoicesDocument = gql`
    query GetInvoices {
  invoices {
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
    `;
export type GetInvoicesQueryResult = Apollo.QueryResult<GetInvoicesQuery, GetInvoicesQueryVariables>;
export const GetInvoiceDocument = gql`
    query GetInvoice($id: ID!) {
  invoice(id: $id) {
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
    `;
export type GetInvoiceQueryResult = Apollo.QueryResult<GetInvoiceQuery, GetInvoiceQueryVariables>;
export const GetClipsDocument = gql`
    query GetClips($streamId: ID) {
  clips(streamId: $streamId) {
    ...ClipInfo
  }
}
    ${ClipInfoFragmentDoc}`;
export type GetClipsQueryResult = Apollo.QueryResult<GetClipsQuery, GetClipsQueryVariables>;
export const GetClipDocument = gql`
    query GetClip($id: ID!) {
  clip(id: $id) {
    ...ClipInfo
  }
}
    ${ClipInfoFragmentDoc}`;
export type GetClipQueryResult = Apollo.QueryResult<GetClipQuery, GetClipQueryVariables>;
export const GetClipViewingUrlsDocument = gql`
    query GetClipViewingUrls($clipId: ID!) {
  clipViewingUrls(clipId: $clipId) {
    hls
    dash
    mp4
    webm
  }
}
    `;
export type GetClipViewingUrlsQueryResult = Apollo.QueryResult<GetClipViewingUrlsQuery, GetClipViewingUrlsQueryVariables>;
export const GetApiTokensDocument = gql`
    query GetAPITokens {
  developerTokens {
    id
    name
    permissions
    status
    lastUsedAt
    expiresAt
    createdAt
  }
}
    `;
export type GetApiTokensQueryResult = Apollo.QueryResult<GetApiTokensQuery, GetApiTokensQueryVariables>;
export const GetDvrRequestsDocument = gql`
    query GetDVRRequests($internalName: String, $status: String, $pagination: PaginationInput) {
  dvrRequests(
    internalName: $internalName
    status: $status
    pagination: $pagination
  ) {
    dvrRecordings {
      ...DVRRequestInfo
    }
    total
    page
    limit
  }
}
    ${DvrRequestInfoFragmentDoc}`;
export type GetDvrRequestsQueryResult = Apollo.QueryResult<GetDvrRequestsQuery, GetDvrRequestsQueryVariables>;
export const GetRecordingConfigDocument = gql`
    query GetRecordingConfig($internalName: String!) {
  recordingConfig(internalName: $internalName) {
    ...RecordingConfigInfo
  }
}
    ${RecordingConfigInfoFragmentDoc}`;
export type GetRecordingConfigQueryResult = Apollo.QueryResult<GetRecordingConfigQuery, GetRecordingConfigQueryVariables>;
export const GetStreamHealthMetricsDocument = gql`
    query GetStreamHealthMetrics($stream: String!, $timeRange: TimeRangeInput) {
  streamHealthMetrics(stream: $stream, timeRange: $timeRange) {
    timestamp
    stream
    nodeId
    healthScore
    frameJitterMs
    keyframeStabilityMs
    issuesDescription
    hasIssues
    bitrate
    fps
    width
    height
    codec
    qualityTier
    packetsSent
    packetsLost
    packetLossPercentage
    bufferState
    bufferHealth
    audioChannels
    audioSampleRate
    audioCodec
    audioBitrate
  }
}
    `;
export type GetStreamHealthMetricsQueryResult = Apollo.QueryResult<GetStreamHealthMetricsQuery, GetStreamHealthMetricsQueryVariables>;
export const GetCurrentStreamHealthDocument = gql`
    query GetCurrentStreamHealth($stream: String!) {
  currentStreamHealth(stream: $stream) {
    timestamp
    stream
    nodeId
    healthScore
    frameJitterMs
    keyframeStabilityMs
    issuesDescription
    hasIssues
    bufferState
    packetLossPercentage
    qualityTier
  }
}
    `;
export type GetCurrentStreamHealthQueryResult = Apollo.QueryResult<GetCurrentStreamHealthQuery, GetCurrentStreamHealthQueryVariables>;
export const GetStreamQualityChangesDocument = gql`
    query GetStreamQualityChanges($stream: String!, $timeRange: TimeRangeInput) {
  streamQualityChanges(stream: $stream, timeRange: $timeRange) {
    timestamp
    stream
    nodeId
    changeType
    previousQualityTier
    newQualityTier
    previousResolution
    newResolution
    previousCodec
    newCodec
    previousTracks
    newTracks
  }
}
    `;
export type GetStreamQualityChangesQueryResult = Apollo.QueryResult<GetStreamQualityChangesQuery, GetStreamQualityChangesQueryVariables>;
export const GetStreamHealthAlertsDocument = gql`
    query GetStreamHealthAlerts($stream: String, $timeRange: TimeRangeInput) {
  streamHealthAlerts(stream: $stream, timeRange: $timeRange) {
    timestamp
    stream
    nodeId
    alertType
    severity
    healthScore
    frameJitterMs
    packetLossPercentage
    issuesDescription
    bufferState
    qualityTier
  }
}
    `;
export type GetStreamHealthAlertsQueryResult = Apollo.QueryResult<GetStreamHealthAlertsQuery, GetStreamHealthAlertsQueryVariables>;
export const GetRebufferingEventsDocument = gql`
    query GetRebufferingEvents($stream: String!, $timeRange: TimeRangeInput) {
  rebufferingEvents(stream: $stream, timeRange: $timeRange) {
    timestamp
    stream
    nodeId
    bufferState
    previousState
    rebufferStart
    rebufferEnd
    healthScore
    frameJitterMs
    packetLossPercentage
  }
}
    `;
export type GetRebufferingEventsQueryResult = Apollo.QueryResult<GetRebufferingEventsQuery, GetRebufferingEventsQueryVariables>;
export const GetTenantDocument = gql`
    query GetTenant {
  tenant {
    id
    name
    settings
    cluster
    createdAt
  }
}
    `;
export type GetTenantQueryResult = Apollo.QueryResult<GetTenantQuery, GetTenantQueryVariables>;
export const GetClustersDocument = gql`
    query GetClusters {
  clusters {
    id
    name
    region
    status
    createdAt
    nodes {
      id
      name
      type
      status
      region
      ipAddress
      lastSeen
      createdAt
      latitude
      longitude
      location
    }
  }
}
    `;
export type GetClustersQueryResult = Apollo.QueryResult<GetClustersQuery, GetClustersQueryVariables>;
export const GetClusterDocument = gql`
    query GetCluster($id: ID!) {
  cluster(id: $id) {
    id
    name
    region
    status
    createdAt
    nodes {
      id
      name
      cluster
      type
      status
      region
      ipAddress
      lastSeen
      createdAt
      latitude
      longitude
      location
    }
  }
}
    `;
export type GetClusterQueryResult = Apollo.QueryResult<GetClusterQuery, GetClusterQueryVariables>;
export const GetNodesDocument = gql`
    query GetNodes {
  nodes {
    id
    name
    cluster
    type
    status
    region
    ipAddress
    lastSeen
    createdAt
    latitude
    longitude
    location
  }
}
    `;
export type GetNodesQueryResult = Apollo.QueryResult<GetNodesQuery, GetNodesQueryVariables>;
export const GetNodeDocument = gql`
    query GetNode($id: ID!) {
  node(id: $id) {
    id
    name
    cluster
    type
    status
    region
    ipAddress
    lastSeen
    createdAt
    latitude
    longitude
    location
  }
}
    `;
export type GetNodeQueryResult = Apollo.QueryResult<GetNodeQuery, GetNodeQueryVariables>;
export const GetServiceInstancesDocument = gql`
    query GetServiceInstances($clusterId: String) {
  serviceInstances(clusterId: $clusterId) {
    ...ServiceInstanceInfo
  }
}
    ${ServiceInstanceInfoFragmentDoc}`;
export type GetServiceInstancesQueryResult = Apollo.QueryResult<GetServiceInstancesQuery, GetServiceInstancesQueryVariables>;
export const GetTenantClusterAssignmentsDocument = gql`
    query GetTenantClusterAssignments {
  tenantClusterAssignments {
    ...TenantClusterAssignmentInfo
  }
}
    ${TenantClusterAssignmentInfoFragmentDoc}`;
export type GetTenantClusterAssignmentsQueryResult = Apollo.QueryResult<GetTenantClusterAssignmentsQuery, GetTenantClusterAssignmentsQueryVariables>;
export const IntrospectSchemaDocument = gql`
    query IntrospectSchema {
  __schema {
    queryType {
      name
      fields {
        name
        description
        type {
          ...TypeRef
        }
        args {
          name
          description
          type {
            ...TypeRef
          }
          defaultValue
        }
      }
    }
    mutationType {
      name
      fields {
        name
        description
        type {
          ...TypeRef
        }
        args {
          name
          description
          type {
            ...TypeRef
          }
          defaultValue
        }
      }
    }
    subscriptionType {
      name
      fields {
        name
        description
        type {
          ...TypeRef
        }
        args {
          name
          description
          type {
            ...TypeRef
          }
          defaultValue
        }
      }
    }
    types {
      ...FullType
    }
  }
}
    ${TypeRefFragmentDoc}
${FullTypeFragmentDoc}`;
export type IntrospectSchemaQueryResult = Apollo.QueryResult<IntrospectSchemaQuery, IntrospectSchemaQueryVariables>;
export const GetRootTypesDocument = gql`
    query GetRootTypes {
  __schema {
    queryType {
      name
      fields {
        name
        description
      }
    }
    mutationType {
      name
      fields {
        name
        description
      }
    }
    subscriptionType {
      name
      fields {
        name
        description
      }
    }
  }
}
    `;
export type GetRootTypesQueryResult = Apollo.QueryResult<GetRootTypesQuery, GetRootTypesQueryVariables>;
export const GetViewerMetrics5mDocument = gql`
    query GetViewerMetrics5m($stream: String, $timeRange: TimeRangeInput) {
  viewerMetrics5m(stream: $stream, timeRange: $timeRange) {
    timestamp
    internalName
    nodeId
    peakViewers
    avgViewers
    uniqueCountries
    uniqueCities
    avgConnectionQuality
    avgBufferHealth
  }
}
    `;
export type GetViewerMetrics5mQueryResult = Apollo.QueryResult<GetViewerMetrics5mQuery, GetViewerMetrics5mQueryVariables>;
export const GetPerformanceServiceInstancesDocument = gql`
    query GetPerformanceServiceInstances($clusterId: String) {
  serviceInstances(clusterId: $clusterId) {
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
    cpuUsagePercent
    memoryUsageMb
  }
}
    `;
export type GetPerformanceServiceInstancesQueryResult = Apollo.QueryResult<GetPerformanceServiceInstancesQuery, GetPerformanceServiceInstancesQueryVariables>;
export const GetPlatformPerformanceDocument = gql`
    query GetPlatformPerformance($timeRange: TimeRangeInput) {
  viewerMetrics5m(timeRange: $timeRange) {
    timestamp
    internalName
    nodeId
    peakViewers
    avgViewers
    uniqueCountries
    uniqueCities
    avgConnectionQuality
    avgBufferHealth
  }
  nodeMetrics(timeRange: $timeRange) {
    timestamp
    nodeId
    cpuUsage
    memoryUsage
    diskUsage
    healthScore
    status
  }
}
    `;
export type GetPlatformPerformanceQueryResult = Apollo.QueryResult<GetPlatformPerformanceQuery, GetPlatformPerformanceQueryVariables>;
export const GetStreamPerformanceDocument = gql`
    query GetStreamPerformance($stream: String!, $timeRange: TimeRangeInput) {
  viewerMetrics5m(stream: $stream, timeRange: $timeRange) {
    timestamp
    internalName
    nodeId
    peakViewers
    avgViewers
    uniqueCountries
    uniqueCities
    avgConnectionQuality
    avgBufferHealth
  }
  routingEvents(stream: $stream, timeRange: $timeRange) {
    timestamp
    selectedNode
    score
    status
    clientCountry
    nodeLatitude
    nodeLongitude
  }
}
    `;
export type GetStreamPerformanceQueryResult = Apollo.QueryResult<GetStreamPerformanceQuery, GetStreamPerformanceQueryVariables>;
export const GetNodeEfficiencyDocument = gql`
    query GetNodeEfficiency($nodeId: String!, $timeRange: TimeRangeInput) {
  nodeMetrics(nodeId: $nodeId, timeRange: $timeRange) {
    timestamp
    nodeId
    cpuUsage
    memoryUsage
    diskUsage
    networkRx
    networkTx
    healthScore
    status
  }
  routingEvents(timeRange: $timeRange) {
    timestamp
    streamName
    selectedNode
    score
    status
    clientCountry
  }
}
    `;
export type GetNodeEfficiencyQueryResult = Apollo.QueryResult<GetNodeEfficiencyQuery, GetNodeEfficiencyQueryVariables>;
export const GetRegionalPerformanceDocument = gql`
    query GetRegionalPerformance($timeRange: TimeRangeInput) {
  viewerMetrics5m(timeRange: $timeRange) {
    timestamp
    internalName
    nodeId
    avgViewers
    uniqueCountries
    avgConnectionQuality
    avgBufferHealth
  }
  connectionEvents(timeRange: $timeRange) {
    timestamp
    nodeId
    countryCode
    city
    eventType
  }
}
    `;
export type GetRegionalPerformanceQueryResult = Apollo.QueryResult<GetRegionalPerformanceQuery, GetRegionalPerformanceQueryVariables>;
export const GetRoutingEventsDocument = gql`
    query GetRoutingEvents($stream: String, $timeRange: TimeRangeInput) {
  routingEvents(stream: $stream, timeRange: $timeRange) {
    timestamp
    streamName
    selectedNode
    status
    details
    score
    clientIp
    clientCountry
    clientLatitude
    clientLongitude
    nodeLatitude
    nodeLongitude
    nodeName
  }
}
    `;
export type GetRoutingEventsQueryResult = Apollo.QueryResult<GetRoutingEventsQuery, GetRoutingEventsQueryVariables>;
export const GetConnectionEventsDocument = gql`
    query GetConnectionEvents($stream: String, $timeRange: TimeRangeInput) {
  connectionEvents(stream: $stream, timeRange: $timeRange) {
    eventId
    timestamp
    tenantId
    internalName
    sessionId
    connectionAddr
    connector
    nodeId
    countryCode
    city
    latitude
    longitude
    eventType
  }
}
    `;
export type GetConnectionEventsQueryResult = Apollo.QueryResult<GetConnectionEventsQuery, GetConnectionEventsQueryVariables>;
export const GetNodeMetricsDocument = gql`
    query GetNodeMetrics($nodeId: String, $timeRange: TimeRangeInput) {
  nodeMetrics(nodeId: $nodeId, timeRange: $timeRange) {
    timestamp
    nodeId
    cpuUsage
    memoryUsage
    diskUsage
    networkRx
    networkTx
    healthScore
    status
    latitude
    longitude
    tags
    metadata
  }
}
    `;
export type GetNodeMetricsQueryResult = Apollo.QueryResult<GetNodeMetricsQuery, GetNodeMetricsQueryVariables>;
export const GetPlatformRoutingEventsDocument = gql`
    query GetPlatformRoutingEvents($timeRange: TimeRangeInput) {
  routingEvents(timeRange: $timeRange) {
    timestamp
    streamName
    selectedNode
    status
    score
    clientCountry
    clientLatitude
    clientLongitude
    nodeLatitude
    nodeLongitude
    nodeName
    details
  }
}
    `;
export type GetPlatformRoutingEventsQueryResult = Apollo.QueryResult<GetPlatformRoutingEventsQuery, GetPlatformRoutingEventsQueryVariables>;
export const GetStreamConnectionEventsDocument = gql`
    query GetStreamConnectionEvents($stream: String!, $timeRange: TimeRangeInput) {
  connectionEvents(stream: $stream, timeRange: $timeRange) {
    timestamp
    sessionId
    connectionAddr
    nodeId
    countryCode
    city
    latitude
    longitude
    eventType
  }
}
    `;
export type GetStreamConnectionEventsQueryResult = Apollo.QueryResult<GetStreamConnectionEventsQuery, GetStreamConnectionEventsQueryVariables>;
export const GetAllNodeMetricsDocument = gql`
    query GetAllNodeMetrics($timeRange: TimeRangeInput) {
  nodeMetrics(timeRange: $timeRange) {
    timestamp
    nodeId
    cpuUsage
    memoryUsage
    diskUsage
    networkRx
    networkTx
    healthScore
    status
    latitude
    longitude
  }
}
    `;
export type GetAllNodeMetricsQueryResult = Apollo.QueryResult<GetAllNodeMetricsQuery, GetAllNodeMetricsQueryVariables>;
export const GetStreamsDocument = gql`
    query GetStreams {
  streams {
    ...StreamInfo
  }
}
    ${StreamInfoFragmentDoc}`;
export type GetStreamsQueryResult = Apollo.QueryResult<GetStreamsQuery, GetStreamsQueryVariables>;
export const GetStreamDocument = gql`
    query GetStream($id: ID!) {
  stream(id: $id) {
    ...StreamInfo
  }
}
    ${StreamInfoFragmentDoc}`;
export type GetStreamQueryResult = Apollo.QueryResult<GetStreamQuery, GetStreamQueryVariables>;
export const ValidateStreamKeyDocument = gql`
    query ValidateStreamKey($streamKey: String!) {
  validateStreamKey(streamKey: $streamKey) {
    valid
    streamKey
    error
  }
}
    `;
export type ValidateStreamKeyQueryResult = Apollo.QueryResult<ValidateStreamKeyQuery, ValidateStreamKeyQueryVariables>;
export const GetStreamKeysDocument = gql`
    query GetStreamKeys($streamId: ID!) {
  streamKeys(streamId: $streamId) {
    ...StreamKeyInfo
  }
}
    ${StreamKeyInfoFragmentDoc}`;
export type GetStreamKeysQueryResult = Apollo.QueryResult<GetStreamKeysQuery, GetStreamKeysQueryVariables>;
export const GetRecordingsDocument = gql`
    query GetRecordings($streamId: ID) {
  recordings(streamId: $streamId) {
    ...RecordingInfo
  }
}
    ${RecordingInfoFragmentDoc}`;
export type GetRecordingsQueryResult = Apollo.QueryResult<GetRecordingsQuery, GetRecordingsQueryVariables>;
export const GetStreamRecordingsDocument = gql`
    query GetStreamRecordings($streamId: ID!) {
  recordings(streamId: $streamId) {
    ...RecordingInfo
  }
}
    ${RecordingInfoFragmentDoc}`;
export type GetStreamRecordingsQueryResult = Apollo.QueryResult<GetStreamRecordingsQuery, GetStreamRecordingsQueryVariables>;
export const StreamEventsDocument = gql`
    subscription StreamEvents($stream: String) {
  streamEvents(stream: $stream) {
    type
    stream
    status
    timestamp
    details
  }
}
    `;
export type StreamEventsSubscriptionResult = Apollo.SubscriptionResult<StreamEventsSubscription>;
export const ViewerMetricsStreamDocument = gql`
    subscription ViewerMetricsStream($stream: String!) {
  viewerMetrics(stream: $stream) {
    stream
    currentViewers
    viewerCount
    peakViewers
    bandwidth
    connectionQuality
    bufferHealth
    timestamp
  }
}
    `;
export type ViewerMetricsStreamSubscriptionResult = Apollo.SubscriptionResult<ViewerMetricsStreamSubscription>;
export const TrackListUpdatesDocument = gql`
    subscription TrackListUpdates($stream: String!) {
  trackListUpdates(stream: $stream) {
    stream
    trackList
    trackCount
    timestamp
  }
}
    `;
export type TrackListUpdatesSubscriptionResult = Apollo.SubscriptionResult<TrackListUpdatesSubscription>;
export const SystemHealthDocument = gql`
    subscription SystemHealth {
  systemHealth {
    node
    cluster
    status
    cpuUsage
    memoryUsage
    diskUsage
    healthScore
    timestamp
  }
}
    `;
export type SystemHealthSubscriptionResult = Apollo.SubscriptionResult<SystemHealthSubscription>;