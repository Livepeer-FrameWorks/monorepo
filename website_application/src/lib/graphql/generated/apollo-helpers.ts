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

export type BillingStatusKeySpecifier = ('currentTier' | 'nextBillingDate' | 'outstandingAmount' | 'status' | 'tenantId' | BillingStatusKeySpecifier)[];
export type BillingStatusFieldPolicy = {
	currentTier?: FieldPolicy<any> | FieldReadFunction<any>,
	nextBillingDate?: FieldPolicy<any> | FieldReadFunction<any>,
	outstandingAmount?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>
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
export type ClipKeySpecifier = ('createdAt' | 'description' | 'duration' | 'endTime' | 'id' | 'playbackId' | 'startTime' | 'status' | 'streamId' | 'tenantId' | 'title' | 'updatedAt' | ClipKeySpecifier)[];
export type ClipFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	duration?: FieldPolicy<any> | FieldReadFunction<any>,
	endTime?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	startTime?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	title?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ClusterKeySpecifier = ('createdAt' | 'id' | 'name' | 'nodes' | 'region' | 'status' | ClusterKeySpecifier)[];
export type ClusterFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	nodes?: FieldPolicy<any> | FieldReadFunction<any>,
	region?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>
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
export type InvoiceKeySpecifier = ('amount' | 'createdAt' | 'currency' | 'dueDate' | 'id' | 'lineItems' | 'status' | 'tenantId' | InvoiceKeySpecifier)[];
export type InvoiceFieldPolicy = {
	amount?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	currency?: FieldPolicy<any> | FieldReadFunction<any>,
	dueDate?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	lineItems?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>
};
export type LineItemKeySpecifier = ('description' | 'quantity' | 'total' | 'unitPrice' | LineItemKeySpecifier)[];
export type LineItemFieldPolicy = {
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	quantity?: FieldPolicy<any> | FieldReadFunction<any>,
	total?: FieldPolicy<any> | FieldReadFunction<any>,
	unitPrice?: FieldPolicy<any> | FieldReadFunction<any>
};
export type MutationKeySpecifier = ('createClip' | 'createDeveloperToken' | 'createPayment' | 'createStream' | 'deleteStream' | 'refreshStreamKey' | 'revokeDeveloperToken' | 'updateBillingTier' | 'updateStream' | 'updateTenant' | MutationKeySpecifier)[];
export type MutationFieldPolicy = {
	createClip?: FieldPolicy<any> | FieldReadFunction<any>,
	createDeveloperToken?: FieldPolicy<any> | FieldReadFunction<any>,
	createPayment?: FieldPolicy<any> | FieldReadFunction<any>,
	createStream?: FieldPolicy<any> | FieldReadFunction<any>,
	deleteStream?: FieldPolicy<any> | FieldReadFunction<any>,
	refreshStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	revokeDeveloperToken?: FieldPolicy<any> | FieldReadFunction<any>,
	updateBillingTier?: FieldPolicy<any> | FieldReadFunction<any>,
	updateStream?: FieldPolicy<any> | FieldReadFunction<any>,
	updateTenant?: FieldPolicy<any> | FieldReadFunction<any>
};
export type NodeKeySpecifier = ('clusterId' | 'createdAt' | 'id' | 'ipAddress' | 'lastSeen' | 'name' | 'region' | 'status' | 'type' | NodeKeySpecifier)[];
export type NodeFieldPolicy = {
	clusterId?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	ipAddress?: FieldPolicy<any> | FieldReadFunction<any>,
	lastSeen?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	region?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	type?: FieldPolicy<any> | FieldReadFunction<any>
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
export type QueryKeySpecifier = ('billingStatus' | 'billingTiers' | 'cluster' | 'clusters' | 'developerTokens' | 'invoice' | 'invoices' | 'me' | 'node' | 'nodes' | 'platformOverview' | 'stream' | 'streamAnalytics' | 'streamEmbed' | 'streams' | 'tenant' | 'usageRecords' | 'validateStreamKey' | 'viewerMetrics' | QueryKeySpecifier)[];
export type QueryFieldPolicy = {
	billingStatus?: FieldPolicy<any> | FieldReadFunction<any>,
	billingTiers?: FieldPolicy<any> | FieldReadFunction<any>,
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	clusters?: FieldPolicy<any> | FieldReadFunction<any>,
	developerTokens?: FieldPolicy<any> | FieldReadFunction<any>,
	invoice?: FieldPolicy<any> | FieldReadFunction<any>,
	invoices?: FieldPolicy<any> | FieldReadFunction<any>,
	me?: FieldPolicy<any> | FieldReadFunction<any>,
	node?: FieldPolicy<any> | FieldReadFunction<any>,
	nodes?: FieldPolicy<any> | FieldReadFunction<any>,
	platformOverview?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	streamAnalytics?: FieldPolicy<any> | FieldReadFunction<any>,
	streamEmbed?: FieldPolicy<any> | FieldReadFunction<any>,
	streams?: FieldPolicy<any> | FieldReadFunction<any>,
	tenant?: FieldPolicy<any> | FieldReadFunction<any>,
	usageRecords?: FieldPolicy<any> | FieldReadFunction<any>,
	validateStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamKeySpecifier = ('createdAt' | 'description' | 'id' | 'name' | 'playbackId' | 'record' | 'status' | 'streamKey' | 'tenantId' | 'updatedAt' | StreamKeySpecifier)[];
export type StreamFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	record?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamAnalyticsKeySpecifier = ('averageViewers' | 'peakViewers' | 'streamId' | 'timeRange' | 'totalViewTime' | 'totalViews' | 'uniqueViewers' | StreamAnalyticsKeySpecifier)[];
export type StreamAnalyticsFieldPolicy = {
	averageViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	timeRange?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViewTime?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViews?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueViewers?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamEmbedKeySpecifier = ('embedCode' | 'height' | 'iframeUrl' | 'streamId' | 'width' | StreamEmbedKeySpecifier)[];
export type StreamEmbedFieldPolicy = {
	embedCode?: FieldPolicy<any> | FieldReadFunction<any>,
	height?: FieldPolicy<any> | FieldReadFunction<any>,
	iframeUrl?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	width?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamEventKeySpecifier = ('details' | 'nodeId' | 'status' | 'streamId' | 'tenantId' | 'timestamp' | 'type' | StreamEventKeySpecifier)[];
export type StreamEventFieldPolicy = {
	details?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	type?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamValidationKeySpecifier = ('error' | 'streamKey' | 'tenantId' | 'valid' | StreamValidationKeySpecifier)[];
export type StreamValidationFieldPolicy = {
	error?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	valid?: FieldPolicy<any> | FieldReadFunction<any>
};
export type SubscriptionKeySpecifier = ('streamEvents' | 'systemHealth' | 'tenantEvents' | 'trackListUpdates' | 'viewerMetrics' | SubscriptionKeySpecifier)[];
export type SubscriptionFieldPolicy = {
	streamEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	systemHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	trackListUpdates?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics?: FieldPolicy<any> | FieldReadFunction<any>
};
export type SystemHealthEventKeySpecifier = ('clusterId' | 'cpuUsage' | 'diskUsage' | 'healthScore' | 'memoryUsage' | 'nodeId' | 'status' | 'timestamp' | SystemHealthEventKeySpecifier)[];
export type SystemHealthEventFieldPolicy = {
	clusterId?: FieldPolicy<any> | FieldReadFunction<any>,
	cpuUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	diskUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	healthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	memoryUsage?: FieldPolicy<any> | FieldReadFunction<any>,
	nodeId?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TenantKeySpecifier = ('clusterId' | 'createdAt' | 'id' | 'name' | 'settings' | TenantKeySpecifier)[];
export type TenantFieldPolicy = {
	clusterId?: FieldPolicy<any> | FieldReadFunction<any>,
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	settings?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TimeRangeKeySpecifier = ('end' | 'start' | TimeRangeKeySpecifier)[];
export type TimeRangeFieldPolicy = {
	end?: FieldPolicy<any> | FieldReadFunction<any>,
	start?: FieldPolicy<any> | FieldReadFunction<any>
};
export type TrackListEventKeySpecifier = ('streamId' | 'tenantId' | 'timestamp' | 'trackCount' | 'trackList' | TrackListEventKeySpecifier)[];
export type TrackListEventFieldPolicy = {
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	trackCount?: FieldPolicy<any> | FieldReadFunction<any>,
	trackList?: FieldPolicy<any> | FieldReadFunction<any>
};
export type UsageRecordKeySpecifier = ('cost' | 'id' | 'quantity' | 'resourceType' | 'tenantId' | 'timestamp' | 'unit' | UsageRecordKeySpecifier)[];
export type UsageRecordFieldPolicy = {
	cost?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	quantity?: FieldPolicy<any> | FieldReadFunction<any>,
	resourceType?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	unit?: FieldPolicy<any> | FieldReadFunction<any>
};
export type UserKeySpecifier = ('createdAt' | 'email' | 'id' | 'name' | 'role' | 'tenantId' | UserKeySpecifier)[];
export type UserFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	email?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	role?: FieldPolicy<any> | FieldReadFunction<any>,
	tenantId?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetricKeySpecifier = ('timestamp' | 'viewerCount' | ViewerMetricKeySpecifier)[];
export type ViewerMetricFieldPolicy = {
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetricsKeySpecifier = ('bandwidth' | 'bufferHealth' | 'connectionQuality' | 'currentViewers' | 'peakViewers' | 'streamId' | 'timestamp' | ViewerMetricsKeySpecifier)[];
export type ViewerMetricsFieldPolicy = {
	bandwidth?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionQuality?: FieldPolicy<any> | FieldReadFunction<any>,
	currentViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	streamId?: FieldPolicy<any> | FieldReadFunction<any>,
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>
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
	Clip?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClipKeySpecifier | (() => undefined | ClipKeySpecifier),
		fields?: ClipFieldPolicy,
	},
	Cluster?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ClusterKeySpecifier | (() => undefined | ClusterKeySpecifier),
		fields?: ClusterFieldPolicy,
	},
	DeveloperToken?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | DeveloperTokenKeySpecifier | (() => undefined | DeveloperTokenKeySpecifier),
		fields?: DeveloperTokenFieldPolicy,
	},
	Invoice?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | InvoiceKeySpecifier | (() => undefined | InvoiceKeySpecifier),
		fields?: InvoiceFieldPolicy,
	},
	LineItem?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | LineItemKeySpecifier | (() => undefined | LineItemKeySpecifier),
		fields?: LineItemFieldPolicy,
	},
	Mutation?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | MutationKeySpecifier | (() => undefined | MutationKeySpecifier),
		fields?: MutationFieldPolicy,
	},
	Node?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | NodeKeySpecifier | (() => undefined | NodeKeySpecifier),
		fields?: NodeFieldPolicy,
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
	Stream?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamKeySpecifier | (() => undefined | StreamKeySpecifier),
		fields?: StreamFieldPolicy,
	},
	StreamAnalytics?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamAnalyticsKeySpecifier | (() => undefined | StreamAnalyticsKeySpecifier),
		fields?: StreamAnalyticsFieldPolicy,
	},
	StreamEmbed?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamEmbedKeySpecifier | (() => undefined | StreamEmbedKeySpecifier),
		fields?: StreamEmbedFieldPolicy,
	},
	StreamEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamEventKeySpecifier | (() => undefined | StreamEventKeySpecifier),
		fields?: StreamEventFieldPolicy,
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
	ViewerMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerMetricKeySpecifier | (() => undefined | ViewerMetricKeySpecifier),
		fields?: ViewerMetricFieldPolicy,
	},
	ViewerMetrics?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | ViewerMetricsKeySpecifier | (() => undefined | ViewerMetricsKeySpecifier),
		fields?: ViewerMetricsFieldPolicy,
	}
};
export type TypedTypePolicies = StrictTypedTypePolicies & TypePolicies;
export const StreamInfoFragmentDoc = gql`
    fragment StreamInfo on Stream {
  id
  name
  description
  streamKey
  playbackId
  status
  record
  tenantId
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
    tenantId
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
    streamId
    tenantId
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
export const UpdateTenantDocument = gql`
    mutation UpdateTenant($input: UpdateTenantInput!) {
  updateTenant(input: $input) {
    id
    name
    settings
    clusterId
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
    tenantId
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
    tenantId
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
    tenantId
    createdAt
    updatedAt
  }
}
    `;
export type RefreshStreamKeyMutationFn = Apollo.MutationFunction<RefreshStreamKeyMutation, RefreshStreamKeyMutationVariables>;
export type RefreshStreamKeyMutationResult = Apollo.MutationResult<RefreshStreamKeyMutation>;
export type RefreshStreamKeyMutationOptions = Apollo.BaseMutationOptions<RefreshStreamKeyMutation, RefreshStreamKeyMutationVariables>;
export const GetStreamAnalyticsDocument = gql`
    query GetStreamAnalytics($streamId: ID!, $timeRange: TimeRangeInput) {
  streamAnalytics(streamId: $streamId, timeRange: $timeRange) {
    streamId
    totalViews
    totalViewTime
    peakViewers
    averageViewers
    uniqueViewers
    timeRange {
      start
      end
    }
  }
}
    `;
export type GetStreamAnalyticsQueryResult = Apollo.QueryResult<GetStreamAnalyticsQuery, GetStreamAnalyticsQueryVariables>;
export const GetViewerMetricsDocument = gql`
    query GetViewerMetrics($streamId: ID, $timeRange: TimeRangeInput) {
  viewerMetrics(streamId: $streamId, timeRange: $timeRange) {
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
    tenantId
    resourceType
    quantity
    unit
    cost
    timestamp
  }
}
    `;
export type GetUsageRecordsQueryResult = Apollo.QueryResult<GetUsageRecordsQuery, GetUsageRecordsQueryVariables>;
export const GetMeDocument = gql`
    query GetMe {
  me {
    id
    email
    name
    tenantId
    role
    createdAt
  }
}
    `;
export type GetMeQueryResult = Apollo.QueryResult<GetMeQuery, GetMeQueryVariables>;
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
    tenantId
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
    tenantId
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
    tenantId
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
export const GetTenantDocument = gql`
    query GetTenant {
  tenant {
    id
    name
    settings
    clusterId
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
      clusterId
      type
      status
      region
      ipAddress
      lastSeen
      createdAt
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
    clusterId
    type
    status
    region
    ipAddress
    lastSeen
    createdAt
  }
}
    `;
export type GetNodesQueryResult = Apollo.QueryResult<GetNodesQuery, GetNodesQueryVariables>;
export const GetNodeDocument = gql`
    query GetNode($id: ID!) {
  node(id: $id) {
    id
    name
    clusterId
    type
    status
    region
    ipAddress
    lastSeen
    createdAt
  }
}
    `;
export type GetNodeQueryResult = Apollo.QueryResult<GetNodeQuery, GetNodeQueryVariables>;
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
    tenantId
  }
}
    `;
export type ValidateStreamKeyQueryResult = Apollo.QueryResult<ValidateStreamKeyQuery, ValidateStreamKeyQueryVariables>;
export const GetStreamEmbedDocument = gql`
    query GetStreamEmbed($id: ID!) {
  streamEmbed(id: $id) {
    streamId
    embedCode
    iframeUrl
    width
    height
  }
}
    `;
export type GetStreamEmbedQueryResult = Apollo.QueryResult<GetStreamEmbedQuery, GetStreamEmbedQueryVariables>;
export const StreamEventsDocument = gql`
    subscription StreamEvents($streamId: ID, $tenantId: ID) {
  streamEvents(streamId: $streamId, tenantId: $tenantId) {
    type
    streamId
    tenantId
    status
    timestamp
    nodeId
    details
  }
}
    `;
export type StreamEventsSubscriptionResult = Apollo.SubscriptionResult<StreamEventsSubscription>;
export const ViewerMetricsStreamDocument = gql`
    subscription ViewerMetricsStream($streamId: ID!) {
  viewerMetrics(streamId: $streamId) {
    streamId
    currentViewers
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
    subscription TrackListUpdates($streamId: ID!) {
  trackListUpdates(streamId: $streamId) {
    streamId
    tenantId
    trackList
    trackCount
    timestamp
  }
}
    `;
export type TrackListUpdatesSubscriptionResult = Apollo.SubscriptionResult<TrackListUpdatesSubscription>;
export const TenantEventsDocument = gql`
    subscription TenantEvents($tenantId: ID!) {
  tenantEvents(tenantId: $tenantId) {
    ... on StreamEvent {
      type
      streamId
      tenantId
      status
      timestamp
      nodeId
      details
    }
    ... on ViewerMetrics {
      streamId
      currentViewers
      peakViewers
      bandwidth
      connectionQuality
      bufferHealth
      timestamp
    }
    ... on TrackListEvent {
      streamId
      tenantId
      trackList
      trackCount
      timestamp
    }
  }
}
    `;
export type TenantEventsSubscriptionResult = Apollo.SubscriptionResult<TenantEventsSubscription>;
export const SystemHealthDocument = gql`
    subscription SystemHealth {
  systemHealth {
    nodeId
    clusterId
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