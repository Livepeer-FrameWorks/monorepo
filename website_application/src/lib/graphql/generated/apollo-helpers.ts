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
  stream: Scalars['String']['input'];
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
  cluster: Scalars['String']['output'];
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
  cluster?: Maybe<Cluster>;
  clusters: Array<Cluster>;
  currentStreamHealth?: Maybe<StreamHealthMetric>;
  developerTokens: Array<DeveloperToken>;
  invoice?: Maybe<Invoice>;
  invoices: Array<Invoice>;
  node?: Maybe<Node>;
  nodes: Array<Node>;
  platformOverview: PlatformOverview;
  rebufferingEvents: Array<RebufferingEvent>;
  stream?: Maybe<Stream>;
  streamAnalytics?: Maybe<StreamAnalytics>;
  streamEmbed: StreamEmbed;
  streamHealthAlerts: Array<StreamHealthAlert>;
  streamHealthMetrics: Array<StreamHealthMetric>;
  streamQualityChanges: Array<StreamQualityChange>;
  streams: Array<Stream>;
  tenant?: Maybe<Tenant>;
  usageRecords: Array<UsageRecord>;
  validateStreamKey: StreamValidation;
  viewerMetrics: Array<ViewerMetric>;
};


export type QueryClusterArgs = {
  id: Scalars['ID']['input'];
};


export type QueryCurrentStreamHealthArgs = {
  stream: Scalars['String']['input'];
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


export type QueryRebufferingEventsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamArgs = {
  id: Scalars['ID']['input'];
};


export type QueryStreamAnalyticsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamEmbedArgs = {
  id: Scalars['ID']['input'];
};


export type QueryStreamHealthAlertsArgs = {
  stream?: InputMaybe<Scalars['String']['input']>;
  timeRange?: InputMaybe<TimeRangeInput>;
};


export type QueryStreamHealthMetricsArgs = {
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
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


export type QueryViewerMetricsArgs = {
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
  updatedAt: Scalars['Time']['output'];
};

export type StreamAnalytics = {
  __typename?: 'StreamAnalytics';
  alertCount?: Maybe<Scalars['Int']['output']>;
  averageHealthScore?: Maybe<Scalars['Float']['output']>;
  averageViewers: Scalars['Float']['output'];
  bufferState?: Maybe<BufferState>;
  currentBitrate?: Maybe<Scalars['Int']['output']>;
  currentCodec?: Maybe<Scalars['String']['output']>;
  currentFps?: Maybe<Scalars['Float']['output']>;
  currentHealthScore?: Maybe<Scalars['Float']['output']>;
  currentIssues?: Maybe<Scalars['String']['output']>;
  currentResolution?: Maybe<Scalars['String']['output']>;
  frameJitterMs?: Maybe<Scalars['Float']['output']>;
  keyframeStabilityMs?: Maybe<Scalars['Float']['output']>;
  packetLossPercentage?: Maybe<Scalars['Float']['output']>;
  peakViewers: Scalars['Int']['output'];
  qualityTier?: Maybe<Scalars['String']['output']>;
  rebufferCount?: Maybe<Scalars['Int']['output']>;
  stream: Scalars['String']['output'];
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
  stream: Scalars['String']['output'];
  width: Scalars['Int']['output'];
};

export type StreamEvent = {
  __typename?: 'StreamEvent';
  details?: Maybe<Scalars['String']['output']>;
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
  width?: Maybe<Scalars['Int']['output']>;
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
  streamEvents: StreamEvent;
  systemHealth: SystemHealthEvent;
  trackListUpdates: TrackListEvent;
  userEvents: TenantEvent;
  viewerMetrics: ViewerMetrics;
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


export type CreatePaymentMutation = { __typename?: 'Mutation', createPayment: { __typename?: 'Payment', id: string, amount: number, currency: string, method: PaymentMethod, status: string, createdAt: string } };

export type UpdateBillingTierMutationVariables = Exact<{
  tierId: Scalars['ID']['input'];
}>;


export type UpdateBillingTierMutation = { __typename?: 'Mutation', updateBillingTier: { __typename?: 'BillingStatus', nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

export type CreateClipMutationVariables = Exact<{
  input: CreateClipInput;
}>;


export type CreateClipMutation = { __typename?: 'Mutation', createClip: { __typename?: 'Clip', id: string, stream: string, title: string, description?: string | null | undefined, startTime: number, endTime: number, duration: number, playbackId: string, status: string, createdAt: string, updatedAt: string } };

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

export type GetStreamAnalyticsQueryVariables = Exact<{
  stream: Scalars['String']['input'];
  timeRange?: InputMaybe<TimeRangeInput>;
}>;


export type GetStreamAnalyticsQuery = { __typename?: 'Query', streamAnalytics?: { __typename?: 'StreamAnalytics', stream: string, totalViews: number, totalViewTime: number, peakViewers: number, averageViewers: number, uniqueViewers: number, currentHealthScore?: number | null | undefined, averageHealthScore?: number | null | undefined, frameJitterMs?: number | null | undefined, keyframeStabilityMs?: number | null | undefined, currentIssues?: string | null | undefined, bufferState?: BufferState | null | undefined, packetLossPercentage?: number | null | undefined, qualityTier?: string | null | undefined, currentCodec?: string | null | undefined, currentResolution?: string | null | undefined, currentBitrate?: number | null | undefined, currentFps?: number | null | undefined, rebufferCount?: number | null | undefined, alertCount?: number | null | undefined, timeRange: { __typename?: 'TimeRange', start: string, end: string } } | null | undefined };

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

export type GetBillingTiersQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingTiersQuery = { __typename?: 'Query', billingTiers: Array<{ __typename?: 'BillingTier', id: string, name: string, description?: string | null | undefined, price: number, currency: string, features: Array<string> }> };

export type GetBillingStatusQueryVariables = Exact<{ [key: string]: never; }>;


export type GetBillingStatusQuery = { __typename?: 'Query', billingStatus: { __typename?: 'BillingStatus', nextBillingDate: string, outstandingAmount: number, status: string, currentTier: { __typename?: 'BillingTier', id: string, name: string, price: number, currency: string, features: Array<string> } } };

export type GetInvoicesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetInvoicesQuery = { __typename?: 'Query', invoices: Array<{ __typename?: 'Invoice', id: string, amount: number, currency: string, status: string, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: number, total: number }> }> };

export type GetInvoiceQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetInvoiceQuery = { __typename?: 'Query', invoice?: { __typename?: 'Invoice', id: string, amount: number, currency: string, status: string, dueDate: string, createdAt: string, lineItems: Array<{ __typename?: 'LineItem', description: string, quantity: number, unitPrice: number, total: number }> } | null | undefined };

export type GetApiTokensQueryVariables = Exact<{ [key: string]: never; }>;


export type GetApiTokensQuery = { __typename?: 'Query', developerTokens: Array<{ __typename?: 'DeveloperToken', id: string, name: string, permissions: string, status: string, lastUsedAt?: string | null | undefined, expiresAt?: string | null | undefined, createdAt: string }> };

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


export type GetClustersQuery = { __typename?: 'Query', clusters: Array<{ __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> }> };

export type GetClusterQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetClusterQuery = { __typename?: 'Query', cluster?: { __typename?: 'Cluster', id: string, name: string, region: string, status: NodeStatus, createdAt: string, nodes: Array<{ __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> } | null | undefined };

export type GetNodesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetNodesQuery = { __typename?: 'Query', nodes: Array<{ __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string }> };

export type GetNodeQueryVariables = Exact<{
  id: Scalars['ID']['input'];
}>;


export type GetNodeQuery = { __typename?: 'Query', node?: { __typename?: 'Node', id: string, name: string, cluster: string, type: string, status: NodeStatus, region: string, ipAddress?: string | null | undefined, lastSeen: string, createdAt: string } | null | undefined };

export type IntrospectSchemaQueryVariables = Exact<{ [key: string]: never; }>;


export type IntrospectSchemaQuery = { __typename?: 'Query', __schema: { __typename?: '__Schema', queryType: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined }, mutationType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined } | null | undefined, subscriptionType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> }> | null | undefined } | null | undefined, types: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, description?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }>, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, inputFields?: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, interfaces?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined, enumValues?: Array<{ __typename?: '__EnumValue', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined }> | null | undefined, possibleTypes?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined }> } };

export type FullTypeFragment = { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, description?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined, args: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }>, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, inputFields?: Array<{ __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } }> | null | undefined, interfaces?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined, enumValues?: Array<{ __typename?: '__EnumValue', name: string, description?: string | null | undefined, isDeprecated: boolean, deprecationReason?: string | null | undefined }> | null | undefined, possibleTypes?: Array<{ __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined }> | null | undefined };

export type InputValueFragment = { __typename?: '__InputValue', name: string, description?: string | null | undefined, defaultValue?: string | null | undefined, type: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } };

export type TypeRefFragment = { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined, ofType?: { __typename?: '__Type', kind: __TypeKind, name?: string | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined } | null | undefined };

export type GetRootTypesQueryVariables = Exact<{ [key: string]: never; }>;


export type GetRootTypesQuery = { __typename?: 'Query', __schema: { __typename?: '__Schema', queryType: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined }, mutationType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined } | null | undefined, subscriptionType?: { __typename?: '__Type', name?: string | null | undefined, fields?: Array<{ __typename?: '__Field', name: string, description?: string | null | undefined }> | null | undefined } | null | undefined } };

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

export type StreamEventsSubscriptionVariables = Exact<{
  stream?: InputMaybe<Scalars['String']['input']>;
}>;


export type StreamEventsSubscription = { __typename?: 'Subscription', streamEvents: { __typename?: 'StreamEvent', type: StreamEventType, stream: string, status: StreamStatus, timestamp: string, details?: string | null | undefined } };

export type ViewerMetricsStreamSubscriptionVariables = Exact<{
  stream: Scalars['String']['input'];
}>;


export type ViewerMetricsStreamSubscription = { __typename?: 'Subscription', viewerMetrics: { __typename?: 'ViewerMetrics', stream: string, currentViewers: number, peakViewers: number, bandwidth: number, connectionQuality?: number | null | undefined, bufferHealth?: number | null | undefined, timestamp: string } };

export type TrackListUpdatesSubscriptionVariables = Exact<{
  stream: Scalars['String']['input'];
}>;


export type TrackListUpdatesSubscription = { __typename?: 'Subscription', trackListUpdates: { __typename?: 'TrackListEvent', stream: string, trackList: string, trackCount: number, timestamp: string } };

export type TenantEventsSubscriptionVariables = Exact<{ [key: string]: never; }>;


export type TenantEventsSubscription = { __typename?: 'Subscription', userEvents: { __typename?: 'StreamEvent', type: StreamEventType, stream: string, status: StreamStatus, timestamp: string, details?: string | null | undefined } | { __typename?: 'TrackListEvent', stream: string, trackList: string, trackCount: number, timestamp: string } | { __typename?: 'ViewerMetrics', stream: string, currentViewers: number, peakViewers: number, bandwidth: number, connectionQuality?: number | null | undefined, bufferHealth?: number | null | undefined, timestamp: string } };

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
export type ClipKeySpecifier = ('createdAt' | 'description' | 'duration' | 'endTime' | 'id' | 'playbackId' | 'startTime' | 'status' | 'stream' | 'title' | 'updatedAt' | ClipKeySpecifier)[];
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
export type NodeKeySpecifier = ('cluster' | 'createdAt' | 'id' | 'ipAddress' | 'lastSeen' | 'name' | 'region' | 'status' | 'type' | NodeKeySpecifier)[];
export type NodeFieldPolicy = {
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
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
export type QueryKeySpecifier = ('billingStatus' | 'billingTiers' | 'cluster' | 'clusters' | 'currentStreamHealth' | 'developerTokens' | 'invoice' | 'invoices' | 'node' | 'nodes' | 'platformOverview' | 'rebufferingEvents' | 'stream' | 'streamAnalytics' | 'streamEmbed' | 'streamHealthAlerts' | 'streamHealthMetrics' | 'streamQualityChanges' | 'streams' | 'tenant' | 'usageRecords' | 'validateStreamKey' | 'viewerMetrics' | QueryKeySpecifier)[];
export type QueryFieldPolicy = {
	billingStatus?: FieldPolicy<any> | FieldReadFunction<any>,
	billingTiers?: FieldPolicy<any> | FieldReadFunction<any>,
	cluster?: FieldPolicy<any> | FieldReadFunction<any>,
	clusters?: FieldPolicy<any> | FieldReadFunction<any>,
	currentStreamHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	developerTokens?: FieldPolicy<any> | FieldReadFunction<any>,
	invoice?: FieldPolicy<any> | FieldReadFunction<any>,
	invoices?: FieldPolicy<any> | FieldReadFunction<any>,
	node?: FieldPolicy<any> | FieldReadFunction<any>,
	nodes?: FieldPolicy<any> | FieldReadFunction<any>,
	platformOverview?: FieldPolicy<any> | FieldReadFunction<any>,
	rebufferingEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	streamAnalytics?: FieldPolicy<any> | FieldReadFunction<any>,
	streamEmbed?: FieldPolicy<any> | FieldReadFunction<any>,
	streamHealthAlerts?: FieldPolicy<any> | FieldReadFunction<any>,
	streamHealthMetrics?: FieldPolicy<any> | FieldReadFunction<any>,
	streamQualityChanges?: FieldPolicy<any> | FieldReadFunction<any>,
	streams?: FieldPolicy<any> | FieldReadFunction<any>,
	tenant?: FieldPolicy<any> | FieldReadFunction<any>,
	usageRecords?: FieldPolicy<any> | FieldReadFunction<any>,
	validateStreamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerMetrics?: FieldPolicy<any> | FieldReadFunction<any>
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
export type StreamKeySpecifier = ('createdAt' | 'description' | 'id' | 'name' | 'playbackId' | 'record' | 'status' | 'streamKey' | 'updatedAt' | StreamKeySpecifier)[];
export type StreamFieldPolicy = {
	createdAt?: FieldPolicy<any> | FieldReadFunction<any>,
	description?: FieldPolicy<any> | FieldReadFunction<any>,
	id?: FieldPolicy<any> | FieldReadFunction<any>,
	name?: FieldPolicy<any> | FieldReadFunction<any>,
	playbackId?: FieldPolicy<any> | FieldReadFunction<any>,
	record?: FieldPolicy<any> | FieldReadFunction<any>,
	status?: FieldPolicy<any> | FieldReadFunction<any>,
	streamKey?: FieldPolicy<any> | FieldReadFunction<any>,
	updatedAt?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamAnalyticsKeySpecifier = ('alertCount' | 'averageHealthScore' | 'averageViewers' | 'bufferState' | 'currentBitrate' | 'currentCodec' | 'currentFps' | 'currentHealthScore' | 'currentIssues' | 'currentResolution' | 'frameJitterMs' | 'keyframeStabilityMs' | 'packetLossPercentage' | 'peakViewers' | 'qualityTier' | 'rebufferCount' | 'stream' | 'timeRange' | 'totalViewTime' | 'totalViews' | 'uniqueViewers' | StreamAnalyticsKeySpecifier)[];
export type StreamAnalyticsFieldPolicy = {
	alertCount?: FieldPolicy<any> | FieldReadFunction<any>,
	averageHealthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	averageViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferState?: FieldPolicy<any> | FieldReadFunction<any>,
	currentBitrate?: FieldPolicy<any> | FieldReadFunction<any>,
	currentCodec?: FieldPolicy<any> | FieldReadFunction<any>,
	currentFps?: FieldPolicy<any> | FieldReadFunction<any>,
	currentHealthScore?: FieldPolicy<any> | FieldReadFunction<any>,
	currentIssues?: FieldPolicy<any> | FieldReadFunction<any>,
	currentResolution?: FieldPolicy<any> | FieldReadFunction<any>,
	frameJitterMs?: FieldPolicy<any> | FieldReadFunction<any>,
	keyframeStabilityMs?: FieldPolicy<any> | FieldReadFunction<any>,
	packetLossPercentage?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	qualityTier?: FieldPolicy<any> | FieldReadFunction<any>,
	rebufferCount?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	timeRange?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViewTime?: FieldPolicy<any> | FieldReadFunction<any>,
	totalViews?: FieldPolicy<any> | FieldReadFunction<any>,
	uniqueViewers?: FieldPolicy<any> | FieldReadFunction<any>
};
export type StreamEmbedKeySpecifier = ('embedCode' | 'height' | 'iframeUrl' | 'stream' | 'width' | StreamEmbedKeySpecifier)[];
export type StreamEmbedFieldPolicy = {
	embedCode?: FieldPolicy<any> | FieldReadFunction<any>,
	height?: FieldPolicy<any> | FieldReadFunction<any>,
	iframeUrl?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
	width?: FieldPolicy<any> | FieldReadFunction<any>
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
export type StreamHealthMetricKeySpecifier = ('audioBitrate' | 'audioChannels' | 'audioCodec' | 'audioSampleRate' | 'bitrate' | 'bufferHealth' | 'bufferState' | 'codec' | 'fps' | 'frameJitterMs' | 'hasIssues' | 'healthScore' | 'height' | 'issuesDescription' | 'keyframeStabilityMs' | 'nodeId' | 'packetLossPercentage' | 'packetsLost' | 'packetsSent' | 'qualityTier' | 'stream' | 'timestamp' | 'width' | StreamHealthMetricKeySpecifier)[];
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
	width?: FieldPolicy<any> | FieldReadFunction<any>
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
export type SubscriptionKeySpecifier = ('streamEvents' | 'systemHealth' | 'trackListUpdates' | 'userEvents' | 'viewerMetrics' | SubscriptionKeySpecifier)[];
export type SubscriptionFieldPolicy = {
	streamEvents?: FieldPolicy<any> | FieldReadFunction<any>,
	systemHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	trackListUpdates?: FieldPolicy<any> | FieldReadFunction<any>,
	userEvents?: FieldPolicy<any> | FieldReadFunction<any>,
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
export type ViewerMetricKeySpecifier = ('timestamp' | 'viewerCount' | ViewerMetricKeySpecifier)[];
export type ViewerMetricFieldPolicy = {
	timestamp?: FieldPolicy<any> | FieldReadFunction<any>,
	viewerCount?: FieldPolicy<any> | FieldReadFunction<any>
};
export type ViewerMetricsKeySpecifier = ('bandwidth' | 'bufferHealth' | 'connectionQuality' | 'currentViewers' | 'peakViewers' | 'stream' | 'timestamp' | ViewerMetricsKeySpecifier)[];
export type ViewerMetricsFieldPolicy = {
	bandwidth?: FieldPolicy<any> | FieldReadFunction<any>,
	bufferHealth?: FieldPolicy<any> | FieldReadFunction<any>,
	connectionQuality?: FieldPolicy<any> | FieldReadFunction<any>,
	currentViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	peakViewers?: FieldPolicy<any> | FieldReadFunction<any>,
	stream?: FieldPolicy<any> | FieldReadFunction<any>,
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
	RebufferingEvent?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | RebufferingEventKeySpecifier | (() => undefined | RebufferingEventKeySpecifier),
		fields?: RebufferingEventFieldPolicy,
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
	StreamHealthAlert?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamHealthAlertKeySpecifier | (() => undefined | StreamHealthAlertKeySpecifier),
		fields?: StreamHealthAlertFieldPolicy,
	},
	StreamHealthMetric?: Omit<TypePolicy, "fields" | "keyFields"> & {
		keyFields?: false | StreamHealthMetricKeySpecifier | (() => undefined | StreamHealthMetricKeySpecifier),
		fields?: StreamHealthMetricFieldPolicy,
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
export const GetStreamAnalyticsDocument = gql`
    query GetStreamAnalytics($stream: String!, $timeRange: TimeRangeInput) {
  streamAnalytics(stream: $stream, timeRange: $timeRange) {
    stream
    totalViews
    totalViewTime
    peakViewers
    averageViewers
    uniqueViewers
    timeRange {
      start
      end
    }
    currentHealthScore
    averageHealthScore
    frameJitterMs
    keyframeStabilityMs
    currentIssues
    bufferState
    packetLossPercentage
    qualityTier
    currentCodec
    currentResolution
    currentBitrate
    currentFps
    rebufferCount
    alertCount
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
  }
}
    `;
export type GetNodeQueryResult = Apollo.QueryResult<GetNodeQuery, GetNodeQueryVariables>;
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
export const TenantEventsDocument = gql`
    subscription TenantEvents {
  userEvents {
    ... on StreamEvent {
      type
      stream
      status
      timestamp
      details
    }
    ... on ViewerMetrics {
      stream
      currentViewers
      peakViewers
      bandwidth
      connectionQuality
      bufferHealth
      timestamp
    }
    ... on TrackListEvent {
      stream
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