package decklog

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"

	"google.golang.org/grpc"
	grpc_health_v1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// envelopeSchemaVersion is the schema_version producers stamp on outbound
// events. Bumped when an envelope-affecting proto change ships.
const envelopeSchemaVersion = 2

// newEventID returns a UUIDv7 string for the event_id envelope field. Falls
// back to a v4 UUID if v7 generation fails (rare — only happens when the
// system clock is broken).
func newEventID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

// Client represents a gRPC client for Decklog
type Client struct {
	conn   *grpc.ClientConn
	client pb.DecklogServiceClient
	logger logging.Logger
}

// ClientConfig represents configuration for the Decklog client
type ClientConfig struct {
	Target        string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
	Timeout       time.Duration
}

// NewClient creates a new Decklog gRPC client
func NewClient(cfg ClientConfig, logger logging.Logger) (*Client, error) {
	var opts []grpc.DialOption
	var connectParams *grpc.ConnectParams

	transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:    cfg.CACertFile,
		ServerName:    cfg.ServerName,
		AllowInsecure: cfg.AllowInsecure,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Decklog gRPC TLS: %w", err)
	}
	opts = append(opts, transport)

	// Connect to server
	if cfg.Timeout > 0 {
		connectParams = &grpc.ConnectParams{MinConnectTimeout: cfg.Timeout}
	}
	if connectParams != nil {
		opts = append(opts, grpc.WithConnectParams(*connectParams))
	}
	conn, err := grpc.NewClient(cfg.Target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial server: %w", err)
	}

	return &Client{
		conn:   conn,
		client: pb.NewDecklogServiceClient(conn),
		logger: logger,
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// Health queries the gRPC health status of the Decklog server.
// If service is empty (""), it checks overall server health.
func (c *Client) Health(ctx context.Context, service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	hc := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, err
	}
	return resp.GetStatus(), nil
}

// BatchedClient provides direct protobuf event sending for services like Foghorn
type BatchedClient struct {
	conn         *grpc.ClientConn
	client       pb.DecklogServiceClient
	logger       logging.Logger
	source       string
	serviceToken string

	// Envelope v2 metadata — populated from BatchedClientConfig at
	// construction. Used by stampTriggerEnvelope / stampServiceEnvelope to
	// fill source_cluster_id + source_region on outbound events when the
	// caller didn't set them. Decklog still backfills at ingest, but
	// producer-side stamping is preferred (each producer knows its own
	// region; Decklog only knows the Decklog's region, which can be wrong
	// during cross-region failover).
	clusterID    string
	sourceRegion string
}

// BatchedClientConfig represents configuration for the batched Decklog client
type BatchedClientConfig struct {
	Target        string
	AllowInsecure bool
	CACertFile    string
	ServerName    string
	Timeout       time.Duration
	Source        string // Source identifier for all events (e.g., "foghorn")
	ServiceToken  string // Service token for authentication

	// ClusterID is the producer's local cluster_id (CLUSTER_ID env var at
	// the calling service). Stamped onto every outbound event's
	// source_cluster_id envelope field when the caller didn't set it.
	ClusterID string
	// SourceRegion is the producer's local region (REGION env var). Stamped
	// onto every outbound event's source_region envelope field.
	SourceRegion string

	// Optional, when true, makes the client tolerant of missing Target —
	// NewBatchedClient returns a no-op stub instead of failing, and Send*
	// calls return nil silently. Used by the Livepeer gateway integration
	// where FRAMEWORKS_DECKLOG_GRPC_ADDR may be unset (non-FW deployments
	// of the gateway fork). Existing monorepo callers leave this false and
	// keep failing loudly on misconfiguration.
	Optional bool
}

// NewBatchedClient creates a new Decklog gRPC client
func NewBatchedClient(cfg BatchedClientConfig, logger logging.Logger) (*BatchedClient, error) {
	// Optional + missing Target: return a no-op stub. Send* methods short-circuit
	// when the underlying gRPC client is nil (see disabled() helper).
	if cfg.Optional && cfg.Target == "" {
		source := cfg.Source
		if source == "" {
			source = "unknown"
		}
		logger.WithField("source", source).Info("Decklog client disabled (no target; running in optional mode)")
		return &BatchedClient{logger: logger, source: source}, nil
	}

	var opts []grpc.DialOption
	var connectParams *grpc.ConnectParams

	transport, err := grpcutil.ClientTLS(grpcutil.ClientTLSConfig{
		CACertFile:    cfg.CACertFile,
		ServerName:    cfg.ServerName,
		AllowInsecure: cfg.AllowInsecure,
	}, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Decklog batched client TLS: %w", err)
	}
	opts = append(opts, transport)

	// Connect to server
	if cfg.Timeout > 0 {
		connectParams = &grpc.ConnectParams{MinConnectTimeout: cfg.Timeout}
	}
	if connectParams != nil {
		opts = append(opts, grpc.WithConnectParams(*connectParams))
	}
	conn, err := grpc.NewClient(cfg.Target, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial server: %w", err)
	}

	source := cfg.Source
	if source == "" {
		source = "unknown"
	}

	client := &BatchedClient{
		conn:         conn,
		client:       pb.NewDecklogServiceClient(conn),
		logger:       logger,
		source:       source,
		serviceToken: cfg.ServiceToken,
		clusterID:    cfg.ClusterID,
		sourceRegion: cfg.SourceRegion,
	}

	logger.WithFields(logging.Fields{
		"target": cfg.Target,
		"source": source,
	}).Info("Decklog client initialized")

	return client, nil
}

// authContext returns a context with service token authorization metadata.
func (c *BatchedClient) authContext() context.Context {
	return c.authContextFrom(context.Background())
}

func (c *BatchedClient) authContextFrom(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.serviceToken != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+c.serviceToken)
	}
	return ctx
}

// stampTriggerEnvelope fills envelope v2 fields on a MistTrigger when the
// caller didn't set them. event_id is always stamped fresh (UUIDv7 — the
// dedup key downstream consumers use). source_cluster_id / source_region
// come from the BatchedClientConfig captured at construction. Decklog still
// backfills at ingest as a safety net, but producer-side stamping is the
// preferred path so cross-region failovers don't get the wrong source label.
func (c *BatchedClient) stampTriggerEnvelope(trigger *pb.MistTrigger) {
	if trigger == nil {
		return
	}
	if trigger.EventId == "" {
		trigger.EventId = newEventID()
	}
	if trigger.SchemaVersion == 0 {
		trigger.SchemaVersion = envelopeSchemaVersion
	}
	if trigger.GetClusterId() == "" && c.clusterID != "" {
		clusterID := c.clusterID
		trigger.ClusterId = &clusterID
	}
	if trigger.SourceRegion == "" && c.sourceRegion != "" {
		trigger.SourceRegion = c.sourceRegion
	}
}

// stampServiceEnvelope fills envelope v2 fields on a ServiceEvent when the
// caller didn't set them. Mirrors stampTriggerEnvelope but ServiceEvent has
// the source_cluster_id field directly (no oneof).
func (c *BatchedClient) stampServiceEnvelope(event *pb.ServiceEvent) {
	if event == nil {
		return
	}
	if event.EventId == "" {
		event.EventId = newEventID()
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = envelopeSchemaVersion
	}
	if event.SourceClusterId == "" && c.clusterID != "" {
		event.SourceClusterId = c.clusterID
	}
	if event.SourceRegion == "" && c.sourceRegion != "" {
		event.SourceRegion = c.sourceRegion
	}
}

// stampGatewayEnvelope fills envelope v2 fields on a GatewayTelemetryEvent.
// The proto reuses gateway-specific fields (gateway_region, cluster_id) for
// the source_* roles per the proto's own comment; we still stamp event_id
// and schema_version + fall back the cluster/region pair where helpful.
func (c *BatchedClient) stampGatewayEnvelope(event *pb.GatewayTelemetryEvent) {
	if event == nil {
		return
	}
	if event.EventId == "" {
		event.EventId = newEventID()
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = envelopeSchemaVersion
	}
}

// SendTrigger sends an enriched MistTrigger to Decklog
func (c *BatchedClient) SendTrigger(trigger *pb.MistTrigger) error {
	return c.SendTriggerContext(context.Background(), trigger)
}

// SendTriggerContext sends an enriched MistTrigger to Decklog with caller-owned
// cancellation/deadline. Batch flushers use this so a stuck Decklog call cannot
// stall their drain loop indefinitely.
func (c *BatchedClient) SendTriggerContext(ctx context.Context, trigger *pb.MistTrigger) error {
	c.stampTriggerEnvelope(trigger)
	ctx = c.authContextFrom(ctx)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
			"error":        err,
		}).Error("Failed to send trigger to Decklog")
		return fmt.Errorf("failed to send trigger: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"trigger_type": trigger.GetTriggerType(),
		"node_id":      trigger.GetNodeId(),
		"request_id":   trigger.GetRequestId(),
	}).Debug("Trigger sent to Decklog")

	return nil
}

// SendLoadBalancing sends load balancing data to Decklog
func (c *BatchedClient) SendLoadBalancing(data *pb.LoadBalancingData) error {
	ctx := c.authContext()
	// Wrap into unified envelope
	trigger := &pb.MistTrigger{
		TriggerType: "LOAD_BALANCING",
		TriggerPayload: &pb.MistTrigger_LoadBalancingData{
			LoadBalancingData: data,
		},
	}
	if data.GetStreamId() != "" {
		streamID := data.GetStreamId()
		trigger.StreamId = &streamID
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"selected_node": data.GetSelectedNode(),
			"client_ip":     data.GetClientIp(),
			"error":         err,
		}).Error("Failed to send load balancing data to Decklog")
		return fmt.Errorf("failed to send load balancing data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"selected_node": data.GetSelectedNode(),
		"client_ip":     data.GetClientIp(),
	}).Debug("Load balancing data sent to Decklog")

	return nil
}

// SendClipLifecycle sends clip lifecycle data to Decklog
func (c *BatchedClient) SendClipLifecycle(data *pb.ClipLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "CLIP_LIFECYCLE",
		TriggerPayload: &pb.MistTrigger_ClipLifecycleData{
			ClipLifecycleData: data,
		},
	}
	if data.GetStreamId() != "" {
		streamID := data.GetStreamId()
		trigger.StreamId = &streamID
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"clip_hash": data.GetClipHash(),
			"stage":     data.GetStage().String(),
			"error":     err,
		}).Error("Failed to send clip lifecycle data to Decklog")
		return fmt.Errorf("failed to send clip lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"clip_hash": data.GetClipHash(),
		"stage":     data.GetStage().String(),
	}).Debug("Clip lifecycle data sent to Decklog")

	c.emitArtifactLifecycleEvent(buildArtifactLifecycleEvent(
		pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		data.GetClipHash(),
		data.GetStreamId(),
		clipStageToStatus(data.GetStage()),
		int64Ptr(data.GetStartedAt()),
		int64Ptr(data.GetCompletedAt()),
		int64Ptr(data.GetExpiresAt()),
		data.GetTenantId(),
		data.GetUserId(),
	))

	return nil
}

// SendDVRLifecycle sends DVR lifecycle data to Decklog
func (c *BatchedClient) SendDVRLifecycle(data *pb.DVRLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "DVR_LIFECYCLE",
		TriggerPayload: &pb.MistTrigger_DvrLifecycleData{
			DvrLifecycleData: data,
		},
	}
	if data.GetStreamId() != "" {
		streamID := data.GetStreamId()
		trigger.StreamId = &streamID
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"dvr_hash": data.GetDvrHash(),
			"status":   data.GetStatus().String(),
			"error":    err,
		}).Error("Failed to send DVR lifecycle data to Decklog")
		return fmt.Errorf("failed to send DVR lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"dvr_hash": data.GetDvrHash(),
		"status":   data.GetStatus().String(),
	}).Debug("DVR lifecycle data sent to Decklog")

	c.emitArtifactLifecycleEvent(buildArtifactLifecycleEvent(
		pb.ArtifactEvent_ARTIFACT_TYPE_DVR,
		data.GetDvrHash(),
		data.GetStreamId(),
		dvrStatusToStatus(data.GetStatus()),
		int64Ptr(data.GetStartedAt()),
		int64Ptr(data.GetEndedAt()),
		int64Ptr(data.GetExpiresAt()),
		data.GetTenantId(),
		data.GetUserId(),
	))

	return nil
}

// SendVodLifecycle sends VOD lifecycle data to Decklog
func (c *BatchedClient) SendVodLifecycle(data *pb.VodLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "VOD_LIFECYCLE",
		TriggerPayload: &pb.MistTrigger_VodLifecycleData{
			VodLifecycleData: data,
		},
	}
	if data.GetTenantId() != "" {
		tenantID := data.GetTenantId()
		trigger.TenantId = &tenantID
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"vod_hash": data.GetVodHash(),
			"status":   data.GetStatus().String(),
			"error":    err,
		}).Error("Failed to send VOD lifecycle data to Decklog")
		return fmt.Errorf("failed to send VOD lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"vod_hash": data.GetVodHash(),
		"status":   data.GetStatus().String(),
	}).Debug("VOD lifecycle data sent to Decklog")

	c.emitArtifactLifecycleEvent(buildArtifactLifecycleEvent(
		pb.ArtifactEvent_ARTIFACT_TYPE_VOD,
		data.GetVodHash(),
		"",
		vodStatusToStatus(data.GetStatus()),
		int64Ptr(data.GetStartedAt()),
		int64Ptr(data.GetCompletedAt()),
		int64Ptr(data.GetExpiresAt()),
		data.GetTenantId(),
		data.GetUserId(),
	))

	return nil
}

func (c *BatchedClient) emitArtifactLifecycleEvent(event *pb.ServiceEvent) {
	if c == nil || event == nil || c.client == nil {
		return
	}
	if event.Source == "" {
		event.Source = c.source
	}
	if event.Timestamp == nil {
		event.Timestamp = timestamppb.Now()
	}
	c.stampServiceEnvelope(event)
	go func(ev *pb.ServiceEvent) {
		if _, err := c.client.SendServiceEvent(c.authContext(), ev); err != nil {
			c.logger.WithError(err).WithField("event_type", ev.EventType).Warn("Failed to emit artifact lifecycle service event")
		}
	}(event)
}

func buildArtifactLifecycleEvent(
	artifactType pb.ArtifactEvent_ArtifactType,
	artifactID, streamID, status string,
	startedAt, completedAt, expiresAt *int64,
	tenantID, userID string,
) *pb.ServiceEvent {
	if artifactID == "" || tenantID == "" {
		return nil
	}

	payload := &pb.ArtifactEvent{
		ArtifactType: artifactType,
		ArtifactId:   artifactID,
		StreamId:     streamID,
		Status:       status,
	}
	if startedAt != nil {
		payload.StartedAt = startedAt
	}
	if completedAt != nil {
		payload.CompletedAt = completedAt
	}
	if expiresAt != nil {
		payload.ExpiresAt = expiresAt
	}

	return &pb.ServiceEvent{
		EventType:    "artifact_lifecycle",
		Source:       "foghorn",
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   artifactID,
		Payload:      &pb.ServiceEvent_ArtifactEvent{ArtifactEvent: payload},
	}
}

func clipStageToStatus(stage pb.ClipLifecycleData_Stage) string {
	switch stage {
	case pb.ClipLifecycleData_STAGE_REQUESTED:
		return "requested"
	case pb.ClipLifecycleData_STAGE_QUEUED:
		return "queued"
	case pb.ClipLifecycleData_STAGE_PROGRESS:
		return "processing"
	case pb.ClipLifecycleData_STAGE_DONE:
		return "completed"
	case pb.ClipLifecycleData_STAGE_FAILED:
		return "failed"
	case pb.ClipLifecycleData_STAGE_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

func dvrStatusToStatus(status pb.DVRLifecycleData_Status) string {
	switch status {
	case pb.DVRLifecycleData_STATUS_STARTED:
		return "started"
	case pb.DVRLifecycleData_STATUS_RECORDING:
		return "recording"
	case pb.DVRLifecycleData_STATUS_STOPPED:
		return "stopped"
	case pb.DVRLifecycleData_STATUS_FAILED:
		return "failed"
	case pb.DVRLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

func vodStatusToStatus(status pb.VodLifecycleData_Status) string {
	switch status {
	case pb.VodLifecycleData_STATUS_REQUESTED:
		return "requested"
	case pb.VodLifecycleData_STATUS_UPLOADING:
		return "uploading"
	case pb.VodLifecycleData_STATUS_PROCESSING:
		return "processing"
	case pb.VodLifecycleData_STATUS_COMPLETED:
		return "completed"
	case pb.VodLifecycleData_STATUS_FAILED:
		return "failed"
	case pb.VodLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

func int64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	v := value
	return &v
}

// Close gracefully shuts down the client
func (c *BatchedClient) Close() error {
	if c.conn != nil {
		_ = c.conn.Close()
	}

	c.logger.WithField("source", c.source).Info("Decklog client closed")
	return nil
}

// Health queries the gRPC health status of the Decklog server.
// If service is empty (""), it checks overall server health.
func (c *BatchedClient) Health(ctx context.Context, service string) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	hc := grpc_health_v1.NewHealthClient(c.conn)
	resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: service})
	if err != nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, err
	}
	return resp.GetStatus(), nil
}

// SendAPIRequestBatch sends aggregated API request metrics to Decklog
func (c *BatchedClient) SendAPIRequestBatch(data *pb.APIRequestBatch) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "API_REQUEST_BATCH",
		TriggerPayload: &pb.MistTrigger_ApiRequestBatch{
			ApiRequestBatch: data,
		},
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"source_node":     data.GetSourceNode(),
			"aggregate_count": len(data.GetAggregates()),
			"error":           err,
		}).Error("Failed to send API request batch to Decklog")
		return fmt.Errorf("failed to send API request batch: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"source_node":     data.GetSourceNode(),
		"aggregate_count": len(data.GetAggregates()),
	}).Debug("API request batch sent to Decklog")

	return nil
}

// SendMessageLifecycle sends messaging lifecycle data to Decklog for real-time UI updates
func (c *BatchedClient) SendMessageLifecycle(data *pb.MessageLifecycleData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "MESSAGE_LIFECYCLE",
		TenantId:    data.TenantId,
		TriggerPayload: &pb.MistTrigger_MessageLifecycleData{
			MessageLifecycleData: data,
		},
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"conversation_id": data.GetConversationId(),
			"event_type":      data.GetEventType().String(),
			"error":           err,
		}).Error("Failed to send message lifecycle data to Decklog")
		return fmt.Errorf("failed to send message lifecycle data: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"conversation_id": data.GetConversationId(),
		"event_type":      data.GetEventType().String(),
	}).Debug("Message lifecycle data sent to Decklog")

	return nil
}

// SendFederationEvent sends a federation operation event to Decklog
func (c *BatchedClient) SendFederationEvent(data *pb.FederationEventData) error {
	ctx := c.authContext()
	trigger := &pb.MistTrigger{
		TriggerType: "FEDERATION_EVENT",
		TenantId:    data.TenantId,
		TriggerPayload: &pb.MistTrigger_FederationEventData{
			FederationEventData: data,
		},
	}
	if data.GetStreamId() != "" {
		streamID := data.GetStreamId()
		trigger.StreamId = &streamID
	}
	if data.GetLocalCluster() != "" {
		clusterID := data.GetLocalCluster()
		trigger.ClusterId = &clusterID
	}
	c.stampTriggerEnvelope(trigger)
	_, err := c.client.SendEvent(ctx, trigger)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"event_type":    data.GetEventType().String(),
			"local_cluster": data.GetLocalCluster(),
			"error":         err,
		}).Error("Failed to send federation event to Decklog")
		return fmt.Errorf("failed to send federation event: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"event_type":     data.GetEventType().String(),
		"local_cluster":  data.GetLocalCluster(),
		"remote_cluster": data.GetRemoteCluster(),
	}).Debug("Federation event sent to Decklog")

	return nil
}

// disabled is true for an Optional client constructed without a Target —
// Send* methods short-circuit instead of calling a nil gRPC client.
func (c *BatchedClient) disabled() bool { return c == nil || c.client == nil }

// SendGatewayTelemetry sends a per-orchestrator telemetry event from a Livepeer
// gateway to Decklog. The event must carry at least one tenant id (stream or
// cluster owner); Decklog rejects the RPC otherwise. When the client was
// constructed in Optional mode without a target, this is a quiet no-op.
func (c *BatchedClient) SendGatewayTelemetry(event *pb.GatewayTelemetryEvent) error {
	if c.disabled() {
		return nil
	}
	c.stampGatewayEnvelope(event)
	ctx := c.authContext()
	_, err := c.client.SendGatewayTelemetry(ctx, event)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"gateway_id": event.GetGatewayId(),
			"cluster_id": event.GetClusterId(),
			"error":      err,
		}).Error("Failed to send gateway telemetry to Decklog")
		return fmt.Errorf("failed to send gateway telemetry: %w", err)
	}
	return nil
}

// SendServiceEvent sends a service-plane event to Decklog (service_events topic).
func (c *BatchedClient) SendServiceEvent(event *pb.ServiceEvent) error {
	if c.disabled() {
		return nil
	}
	c.stampServiceEnvelope(event)
	ctx := c.authContext()
	_, err := c.client.SendServiceEvent(ctx, event)
	if err != nil {
		c.logger.WithFields(logging.Fields{
			"event_type": event.GetEventType(),
			"tenant_id":  event.GetTenantId(),
			"error":      err,
		}).Error("Failed to send service event to Decklog")
		return fmt.Errorf("failed to send service event: %w", err)
	}

	c.logger.WithFields(logging.Fields{
		"event_type": event.GetEventType(),
		"tenant_id":  event.GetTenantId(),
	}).Debug("Service event sent to Decklog")
	return nil
}
