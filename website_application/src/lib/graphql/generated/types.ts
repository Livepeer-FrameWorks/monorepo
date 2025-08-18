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
  JSON: { input: any; output: any; }
  Time: { input: string; output: string; }
};

export type BillingStatus = {
  __typename?: 'BillingStatus';
  currentTier: BillingTier;
  nextBillingDate: Scalars['Time']['output'];
  outstandingAmount: Scalars['Float']['output'];
  status: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
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
  streamId: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
  title: Scalars['String']['output'];
  updatedAt: Scalars['Time']['output'];
};

export type Cluster = {
  __typename?: 'Cluster';
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  nodes: Array<Node>;
  region: Scalars['String']['output'];
  status: NodeStatus;
};

export type CreateClipInput = {
  description?: InputMaybe<Scalars['String']['input']>;
  endTime: Scalars['Int']['input'];
  startTime: Scalars['Int']['input'];
  streamId: Scalars['ID']['input'];
  title: Scalars['String']['input'];
};

export type CreateDeveloperTokenInput = {
  expiresIn?: InputMaybe<Scalars['Int']['input']>;
  name: Scalars['String']['input'];
  permissions?: InputMaybe<Scalars['String']['input']>;
};

export type CreatePaymentInput = {
  amount: Scalars['Float']['input'];
  currency?: InputMaybe<Scalars['String']['input']>;
  method: PaymentMethod;
};

export type CreateStreamInput = {
  description?: InputMaybe<Scalars['String']['input']>;
  name: Scalars['String']['input'];
  record?: InputMaybe<Scalars['Boolean']['input']>;
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

export type Invoice = {
  __typename?: 'Invoice';
  amount: Scalars['Float']['output'];
  createdAt: Scalars['Time']['output'];
  currency: Scalars['String']['output'];
  dueDate: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  lineItems: Array<LineItem>;
  status: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
};

export type LineItem = {
  __typename?: 'LineItem';
  description: Scalars['String']['output'];
  quantity: Scalars['Int']['output'];
  total: Scalars['Float']['output'];
  unitPrice: Scalars['Float']['output'];
};

export type Mutation = {
  __typename?: 'Mutation';
  createClip: Clip;
  createDeveloperToken: DeveloperToken;
  createPayment: Payment;
  createStream: Stream;
  deleteStream: Scalars['Boolean']['output'];
  refreshStreamKey: Stream;
  revokeDeveloperToken: Scalars['Boolean']['output'];
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


export type MutationDeleteStreamArgs = {
  id: Scalars['ID']['input'];
};


export type MutationRefreshStreamKeyArgs = {
  id: Scalars['ID']['input'];
};


export type MutationRevokeDeveloperTokenArgs = {
  id: Scalars['ID']['input'];
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
  clusterId: Scalars['String']['output'];
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  ipAddress?: Maybe<Scalars['String']['output']>;
  lastSeen: Scalars['Time']['output'];
  name: Scalars['String']['output'];
  region: Scalars['String']['output'];
  status: NodeStatus;
  type: Scalars['String']['output'];
};

export enum NodeStatus {
  Degraded = 'DEGRADED',
  Healthy = 'HEALTHY',
  Unhealthy = 'UNHEALTHY'
}

export type Payment = {
  __typename?: 'Payment';
  amount: Scalars['Float']['output'];
  createdAt: Scalars['Time']['output'];
  currency: Scalars['String']['output'];
  id: Scalars['ID']['output'];
  method: PaymentMethod;
  status: Scalars['String']['output'];
};

export enum PaymentMethod {
  BankTransfer = 'BANK_TRANSFER',
  Card = 'CARD',
  Crypto = 'CRYPTO'
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
  cluster?: Maybe<Cluster>;
  clusters: Array<Cluster>;
  developerTokens: Array<DeveloperToken>;
  invoice?: Maybe<Invoice>;
  invoices: Array<Invoice>;
  me?: Maybe<User>;
  node?: Maybe<Node>;
  nodes: Array<Node>;
  platformOverview: PlatformOverview;
  stream?: Maybe<Stream>;
  streamAnalytics?: Maybe<StreamAnalytics>;
  streamEmbed: StreamEmbed;
  streams: Array<Stream>;
  tenant?: Maybe<Tenant>;
  usageRecords: Array<UsageRecord>;
  validateStreamKey: StreamValidation;
  viewerMetrics: Array<ViewerMetric>;
};


export type QueryClusterArgs = {
  id: Scalars['ID']['input'];
};


export type QueryInvoiceArgs = {
  id: Scalars['ID']['input'];
};


export type QueryNodeArgs = {
  id: Scalars['ID']['input'];
};


export type QueryPlatformOverviewArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamArgs = {
  id: Scalars['ID']['input'];
};


export type QueryStreamAnalyticsArgs = {
  streamId: Scalars['ID']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamEmbedArgs = {
  id: Scalars['ID']['input'];
};


export type QueryUsageRecordsArgs = {
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryValidateStreamKeyArgs = {
  streamKey: Scalars['String']['input'];
};


export type QueryViewerMetricsArgs = {
  streamId?: InputMaybe<Scalars['ID']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};

export type Stream = {
  __typename?: 'Stream';
  createdAt: Scalars['Time']['output'];
  description?: Maybe<Scalars['String']['output']>;
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  playbackId: Scalars['String']['output'];
  record: Scalars['Boolean']['output'];
  status: StreamStatus;
  streamKey: Scalars['String']['output'];
  tenantId: Scalars['String']['output'];
  updatedAt: Scalars['Time']['output'];
};

export type StreamAnalytics = {
  __typename?: 'StreamAnalytics';
  averageViewers: Scalars['Float']['output'];
  peakViewers: Scalars['Int']['output'];
  streamId: Scalars['ID']['output'];
  timeRange: TimeRange;
  totalViewTime: Scalars['Float']['output'];
  totalViews: Scalars['Int']['output'];
  uniqueViewers: Scalars['Int']['output'];
};

export type StreamEmbed = {
  __typename?: 'StreamEmbed';
  embedCode: Scalars['String']['output'];
  height: Scalars['Int']['output'];
  iframeUrl: Scalars['String']['output'];
  streamId: Scalars['ID']['output'];
  width: Scalars['Int']['output'];
};

export type StreamEvent = {
  __typename?: 'StreamEvent';
  details?: Maybe<Scalars['JSON']['output']>;
  nodeId?: Maybe<Scalars['String']['output']>;
  status: StreamStatus;
  streamId: Scalars['ID']['output'];
  tenantId: Scalars['ID']['output'];
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
  tenantId?: Maybe<Scalars['String']['output']>;
  valid: Scalars['Boolean']['output'];
};

export type Subscription = {
  __typename?: 'Subscription';
  streamEvents: StreamEvent;
  systemHealth: SystemHealthEvent;
  tenantEvents: TenantEvent;
  trackListUpdates: TrackListEvent;
  viewerMetrics: ViewerMetrics;
};


export type SubscriptionStreamEventsArgs = {
  streamId?: InputMaybe<Scalars['ID']['input']>;
  tenantId?: InputMaybe<Scalars['ID']['input']>;
};


export type SubscriptionTenantEventsArgs = {
  tenantId: Scalars['ID']['input'];
};


export type SubscriptionTrackListUpdatesArgs = {
  streamId: Scalars['ID']['input'];
};


export type SubscriptionViewerMetricsArgs = {
  streamId: Scalars['ID']['input'];
};

export type SystemHealthEvent = {
  __typename?: 'SystemHealthEvent';
  clusterId: Scalars['ID']['output'];
  cpuUsage: Scalars['Float']['output'];
  diskUsage: Scalars['Float']['output'];
  healthScore: Scalars['Float']['output'];
  memoryUsage: Scalars['Float']['output'];
  nodeId: Scalars['ID']['output'];
  status: NodeStatus;
  timestamp: Scalars['Time']['output'];
};

export type Tenant = {
  __typename?: 'Tenant';
  clusterId?: Maybe<Scalars['String']['output']>;
  createdAt: Scalars['Time']['output'];
  id: Scalars['ID']['output'];
  name: Scalars['String']['output'];
  settings?: Maybe<Scalars['JSON']['output']>;
};

export type TenantEvent = StreamEvent | TrackListEvent | ViewerMetrics;

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
  streamId: Scalars['ID']['output'];
  tenantId: Scalars['ID']['output'];
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
  tenantId: Scalars['String']['output'];
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
  tenantId: Scalars['String']['output'];
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
  streamId: Scalars['ID']['output'];
  timestamp: Scalars['Time']['output'];
};

export type CreatePaymentMutationVariables = Exact<{
  input: CreatePaymentInput;
}>;


export type CreatePaymentMutation = { __typename?: 'Mutation', createPayment: { __typename?: 'Payment', id: string, amount: number, currency: string, method: PaymentMethod, status: string, createdAt: string } };

export type UpdateBillingTierMutationVariables = Exact<{
  tierId: Scalars['ID']['input'];
}>;


export type UpdateBillingTierMutation = { __typename?: 'Mutation', updateBillingTier: { __typename?: 'BillingStatus', tenantId: string, nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

export type CreateClipMutationVariables = Exact<{
  input: CreateClipInput;
}>;


export type CreateClipMutation = { __typename?: 'Mutation', createClip: { __typename?: 'Clip', id: string, streamId: string, tenantId: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string } };

export type CreateApiTokenMutationVariables = Exact<{
  input: CreateDeveloperTokenInput;
}>;


export type CreateApiTokenMutation = { __typename?: 'Mutation', createDeveloperToken: { __typename?: 'DeveloperToken', id: string, name: string, token?: string | null | undefined, permissions: string, expiresAt?: string | null | undefined, createdAt: string } };

export type RevokeApiTokenMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type RevokeApiTokenMutation = { __typename?: 'Mutation', revokeDeveloperToken: boolean };

export type UpdateTenantMutationVariables = Exact<{
  input: UpdateTenantInput;
}>;


export type UpdateTenantMutation = { __typename?: 'Mutation', updateTenant: { __typename?: 'Tenant', id: string, name: string, settings?: any | null | undefined, clusterId?: string | null | undefined, createdAt: string } };

export type CreateStreamMutationVariables = Exact<{
  input: CreateStreamInput;
}>;


export type CreateStreamMutation = { __typename?: 'Mutation', createStream: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string } };

export type UpdateStreamMutationVariables = Exact<{
  id: Scalars['ID']['input'];
  input: UpdateStreamInput;
}>;


export type UpdateStreamMutation = { __typename?: 'Mutation', updateStream: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string } };

export type DeleteStreamMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type DeleteStreamMutation = { __typename?: 'Mutation', deleteStream: boolean };

export type RefreshStreamKeyMutationVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type RefreshStreamKeyMutation = { __typename?: 'Mutation', refreshStreamKey: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string } };

export type GetStreamAnalyticsQueryVariables = Exact<{
  streamId: Scalars['ID']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamAnalyticsQuery = { __typename?: 'Query', streamAnalytics?: { __typename?: 'StreamAnalytics', streamId: string, totalViews: number, totalViewTime: number, peakViewers: number, averageViewers: number, uniqueViewers: number, timeRange: { __typename?: 'TimeRange', start: string, end: string } } | null | undefined };

export type GetViewerMetricsQueryVariables = Exact<{
  streamId?: InputMaybe<Scalars['ID']['input']>;
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


export type GetUsageRecordsQuery = { __typename?: 'Query', usageRecords: Array<{ __typename?: 'UsageRecord', id: string, tenantId: string, resourceType: string, quantity: number, unit: string, cost: number, timestamp: string }> };

export type GetMeQueryVariables = Exact<{ [key: string]: never; }>;


export type GetMeQuery = { __typename?: 'Query', me?: { __typename?: 'User', id: string, email: string, name?: string | null | undefined, tenantId: string, role: string, createdAt: string } | null | undefined };

export type GetBillingTiersQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingTiersQuery = { __typename?: 'Query', billingTiers: Array<{ __typename?: 'BillingTier', id: string, name: string, description?: string | null | undefined, price: number, currency: string, features: Array<string> }> };

export type GetBillingStatusQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingStatusQuery = { __typename?: 'Query', billingStatus: { __typename?: 'BillingStatus', tenantId: string, nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

export type GetInvoicesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetInvoicesQuery = { __typename?: 'Query', invoices: Array<{ __typename?: 'Invoice', id: string, tenantId: string, amount: number, currency: string, status: string, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: number, total: number }> }> };

export type GetInvoiceQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetInvoiceQuery = { __typename?: 'Query', invoice?: { __typename?: 'Invoice', id: string, tenantId: string, amount: number, currency: string, status: string, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: number, total: number }> } | null | undefined };

export type GetApiTokensQueryVariables = Exact<{ [key: string]: never; }>;


export type GetApiTokensQuery = { __typename?: 'Query', developerTokens: Array<{ __typename?: 'DeveloperToken', id: string, name: string, permissions: string, status: string, lastUsedAt?: string | null | undefined, expiresAt?: string | null | undefined, createdAt: string }> };

export type GetTenantQueryVariables = Exact<{ [key: string]: never; }>;


export type GetTenantQuery = { __typename?: 'Query', tenant?: { __typename?: 'Tenant', id: string, name: string, settings?: any | null | undefined, clusterId?: string | null | undefined, createdAt: string } | null | undefined };

export type GetClustersQueryVariables = Exact<{ [key: string]: never; }>;


export type GetClustersQuery = { __typename?: 'Query', clusters: Array<{ __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> }> };

export type GetClusterQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetClusterQuery = { __typename?: 'Query', cluster?: { __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, clusterId: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> } | null | undefined };

export type GetNodesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetNodesQuery = { __typename?: 'Query', nodes: Array<{ __typename?: 'Node', id: string, name: string, clusterId: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> };

export type GetNodeQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetNodeQuery = { __typename?: 'Query', node?: { __typename?: 'Node', id: string, name: string, clusterId: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string } | null | undefined };

export type StreamInfoFragment = { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string };

export type GetStreamsQueryVariables = Exact<{ [key: string]: never; }>;


export type GetStreamsQuery = { __typename?: 'Query', streams: Array<{ __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string }> };

export type GetStreamQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetStreamQuery = { __typename?: 'Query', stream?: { __typename?: 'Stream', id: string, name: string, description?: string | null | undefined, streamKey: string, playbackId: string, status: StreamStatus, record: boolean, tenantId: string, createdAt: string, updatedAt: string } | null | undefined };

export type ValidateStreamKeyQueryVariables = Exact<{
  streamKey: Scalars['String']['input'];
}>;


export type ValidateStreamKeyQuery = { __typename?: 'Query', validateStreamKey: { __typename?: 'StreamValidation', valid: boolean, streamKey: string, error?: string | null | undefined, tenantId?: string | null | undefined } };

export type GetStreamEmbedQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetStreamEmbedQuery = { __typename?: 'Query', streamEmbed: { __typename?: 'StreamEmbed', streamId: string, embedCode: string, iframeUrl: string, width: number, height: number } };

export type StreamEventsSubscriptionVariables = Exact<{
  streamId?: InputMaybe<Scalars['ID']['input']>;
  tenantId?: InputMaybe<Scalars['ID']['input']>;
}>;


export type StreamEventsSubscription = { __typename?: 'Subscription', streamEvents: { __typename?: 'StreamEvent', type: StreamEventType, streamId: string, tenantId: string, status: StreamStatus, timestamp: string, nodeId?: string | null | undefined, details?: any | null | undefined } };

export type ViewerMetricsStreamSubscriptionVariables = Exact<{
  streamId: Scalars['ID']['input'];
}>;


export type ViewerMetricsStreamSubscription = { __typename?: 'Subscription', viewerMetrics: { __typename?: 'ViewerMetrics', streamId: string, currentViewers: number, peakViewers: number, bandwidth: number, connectionQuality?: number | null | undefined, bufferHealth?: number | null | undefined, timestamp: string } };

export type TrackListUpdatesSubscriptionVariables = Exact<{
  streamId: Scalars['ID']['input'];
}>;


export type TrackListUpdatesSubscription = { __typename?: 'Subscription', trackListUpdates: { __typename?: 'TrackListEvent', streamId: string, tenantId: string, trackList: string, trackCount: number, timestamp: string } };

export type TenantEventsSubscriptionVariables = Exact<{
  tenantId: Scalars['ID']['input'];
}>;


export type TenantEventsSubscription = { __typename?: 'Subscription', tenantEvents: { __typename?: 'StreamEvent', type: StreamEventType, streamId: string, tenantId: string, status: StreamStatus, timestamp: string, nodeId?: string | null | undefined, details?: any | null | undefined } | { __typename?: 'TrackListEvent', streamId: string, tenantId: string, trackList: string, trackCount: number, timestamp: string } | { __typename?: 'ViewerMetrics', streamId: string, currentViewers: number, peakViewers: number, bandwidth: number, connectionQuality?: number | null | undefined, bufferHealth?: number | null | undefined, timestamp: string } };

export type SystemHealthSubscriptionVariables = Exact<{ [key: string]: never; }>;


export type SystemHealthSubscription = { __typename?: 'Subscription', systemHealth: { __typename?: 'SystemHealthEvent', nodeId: string, clusterId: string, status: NodeStatus, cpuUsage: number, memoryUsage: number, diskUsage: number, healthScore: number, timestamp: string } };
