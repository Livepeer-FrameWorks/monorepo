package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// PeriscopeMetrics holds all Prometheus metrics for Periscope Ingest
type PeriscopeMetrics struct {
	AnalyticsEvents         *prometheus.CounterVec
	BatchProcessingDuration *prometheus.HistogramVec
	ClickHouseInserts       *prometheus.CounterVec
	DuplicateEvents         *prometheus.CounterVec
	DLQMessages             *prometheus.CounterVec
	KafkaMessages           *prometheus.CounterVec
	KafkaDuration           *prometheus.HistogramVec
	KafkaLag                *prometheus.GaugeVec
	// ProjectionDivergences counts cases where a new projection of an
	// already-seen logical fact carries a different rated field value
	// beyond the per-meter epsilon. The row is still written (append
	// invariant), and the audit is also written to
	// periscope.projection_divergences. Labels: table, meter, field.
	ProjectionDivergences *prometheus.CounterVec
}

// AnalyticsHandler handles analytics events
type AnalyticsHandler struct {
	clickhouse clickhouseConn
	logger     logging.Logger
	metrics    *PeriscopeMetrics
}

type clickhouseBatch = periscopeingestdb.Batch

type clickhouseRows interface {
	Next() bool
	Close() error
	Scan(dest ...interface{}) error
	Err() error
}

type clickhouseConn interface {
	periscopeingestdb.BatchPreparer
	Query(ctx context.Context, query string, args ...interface{}) (clickhouseRows, error)
	Exec(ctx context.Context, query string, args ...interface{}) error
}

type envelopeColumns struct {
	sourceRegion          string
	streamOriginRegion    string
	streamOriginClusterID string
	schemaVersion         uint8
}

func analyticsEnvelopeColumns(event kafka.AnalyticsEvent) envelopeColumns {
	return envelopeColumns{
		sourceRegion:          event.SourceRegion,
		streamOriginRegion:    event.StreamOriginRegion,
		streamOriginClusterID: event.StreamOriginClusterID,
		schemaVersion:         boundedSchemaVersion(event.SchemaVersion),
	}
}

func serviceEnvelopeColumns(event kafka.ServiceEvent) envelopeColumns {
	return envelopeColumns{
		sourceRegion:          event.SourceRegion,
		streamOriginRegion:    event.StreamOriginRegion,
		streamOriginClusterID: event.StreamOriginClusterID,
		schemaVersion:         boundedSchemaVersion(event.SchemaVersion),
	}
}

func boundedSchemaVersion(version int32) uint8 {
	if version <= 0 {
		return 0
	}
	if version > 255 {
		return 255
	}
	return uint8(version)
}

type clickhouseNativeConn struct {
	conn database.ClickHouseNativeConn
}

func (c clickhouseNativeConn) PrepareBatch(ctx context.Context, query string) (clickhouseBatch, error) {
	return c.conn.PrepareBatch(ctx, query)
}

func (c clickhouseNativeConn) Query(ctx context.Context, query string, args ...interface{}) (clickhouseRows, error) {
	return c.conn.Query(ctx, query, args...)
}

func (c clickhouseNativeConn) Exec(ctx context.Context, query string, args ...interface{}) error {
	return c.conn.Exec(ctx, query, args...)
}

// NewAnalyticsHandler creates a new analytics handler
func NewAnalyticsHandler(clickhouse database.ClickHouseNativeConn, logger logging.Logger, metrics *PeriscopeMetrics) *AnalyticsHandler {
	return &AnalyticsHandler{
		clickhouse: clickhouseNativeConn{conn: clickhouse},
		logger:     logger,
		metrics:    metrics,
	}
}

// parseProtobufData parses the transparent protobuf JSON data from the Kafka event
func (h *AnalyticsHandler) parseProtobufData(event kafka.AnalyticsEvent, target proto.Message) error {
	// Convert the Data map back to JSON
	jsonData, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal event data: %w", err)
	}

	// Parse JSON using protojson to maintain proper protobuf semantics
	unmarshaler := protojson.UnmarshalOptions{
		// Mixed-version clusters may emit fields newer than this ingest binary.
		// Discarding unknown fields keeps ingestion forward-compatible.
		DiscardUnknown: true,
	}

	return unmarshaler.Unmarshal(jsonData, target)
}

// HandleAnalyticsEvent processes analytics events and writes to ClickHouse
func (h *AnalyticsHandler) HandleAnalyticsEvent(event kafka.AnalyticsEvent) error {
	start := time.Now()
	ctx := context.Background()

	// Track analytics event received
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
		// Detect missing enrichment early.
		if !isValidUUIDString(event.TenantID) {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "tenant_missing").Inc()
		}
	}

	// PUSH_REWRITE producers must replace the publishing key with the resolved
	// internal name before emission. Enforce that contract before any generic
	// error/DLQ path serializes the payload; tenant validation below deliberately
	// writes event.Data on failure.
	if event.EventType == "push_rewrite" && !h.pushRewritePayloadSatisfiesContract(event) {
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
		}
		return nil
	}

	// Strict enforcement: drop + DLQ missing/invalid tenant_id
	if err := h.requireTenantID(ctx, event); err != nil {
		return err
	}

	// Process each trigger using the canonical event names emitted by Decklog.
	var err error
	switch event.EventType {
	case "viewer_connect":
		err = h.processViewerConnection(ctx, event, true)
	case "viewer_disconnect":
		err = h.processViewerConnection(ctx, event, false)
	case "stream_buffer":
		err = h.processStreamBuffer(ctx, event)
	case "stream_end":
		err = h.processStreamEnd(ctx, event)
	case "push_rewrite":
		err = h.processPushRewrite(ctx, event)
	case "play_rewrite":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_source":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "push_end":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "push_input_close":
		// PUSH_INPUT_CLOSE is the source-presence "publisher gone" edge
		// owned by Foghorn's AdmitAndReserve admission state machine.
		// It MUST NOT mutate stream_state_current — the ingest session
		// is owned by accepted PUSH_REWRITE only. Audited at metric/log
		// level here; no session-state side effect.
		err = h.skipEvent(event, "source_presence_audit_only")
	case "push_out_start":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_track_list":
		err = h.processTrackList(ctx, event)
	case "recording_complete":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "recording_segment":
		err = h.skipEvent(event, "non_canonical_stream_event")
	case "stream_lifecycle_update":
		err = h.processStreamLifecycle(ctx, event)
	case "node_lifecycle_update":
		err = h.processNodeLifecycle(ctx, event)
	case "client_lifecycle_batch":
		err = h.processClientLifecycleBatch(ctx, event)
	case "playback_boot":
		err = h.processPlaybackBootTrace(ctx, event)
	case "playback_session_qoe":
		err = h.processPlaybackSessionQoe(ctx, event)
	case "load_balancing":
		err = h.processLoadBalancing(ctx, event)
	case "clip_lifecycle":
		err = h.processClipLifecycle(ctx, event)
	case "dvr_lifecycle":
		err = h.processDVRLifecycle(ctx, event)
	case "storage_lifecycle":
		err = h.processStorageLifecycle(ctx, event)
	case "storage_snapshot":
		err = h.processStorageSnapshot(ctx, event)
	case "process_billing":
		err = h.processProcessBilling(ctx, event)
	case "vod_lifecycle":
		err = h.processVodLifecycle(ctx, event)
	case "api_request_batch":
		err = h.processAPIRequestBatch(ctx, event)
	case "federation_event":
		err = h.processFederationEvent(ctx, event)
	case "orchestrator_discovery_observed":
		err = h.processOrchestratorDiscoveryObserved(ctx, event)
	case "orchestrator_state_update":
		err = h.processOrchestratorStateUpdate(ctx, event)
	case "orchestrator_transcode_outcome":
		err = h.processOrchestratorTranscodeOutcome(ctx, event)
	case "orchestrator_ai_outcome":
		err = h.processOrchestratorAIOutcome(ctx, event)
	default:
		h.logger.WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Info("Unknown event type, skipping")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "skipped").Inc()
		}
		return nil
	}

	if err != nil {
		if errors.Is(err, errDropped) {
			return nil
		}
		if errors.Is(err, errMissingTenantID) {
			return err
		}
		h.writeIngestError(ctx, event, "", "handler_error", err)
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Failed to process event")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}

	// Track success
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
		h.metrics.BatchProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
	}

	return nil
}

func (h *AnalyticsHandler) pushRewritePayloadSatisfiesContract(event kafka.AnalyticsEvent) bool {
	var mt ipcpb.MistTrigger
	jsonData, marshalErr := json.Marshal(event.Data)
	if marshalErr != nil {
		h.logger.WithField("event_id", event.EventID).
			Warn("Dropping malformed push_rewrite before durable error handling")
		return false
	}
	// This boundary is intentionally stricter than the generic analytics parser:
	// the original map can be written to a DLQ, so an unknown field must not ride
	// past validation with a publishing credential hidden inside it.
	if err := protojson.Unmarshal(jsonData, &mt); err != nil {
		// Proto parse errors may quote the rejected field value. The malformed
		// payload is untrusted and may contain a publishing credential, so only
		// its envelope identity is safe to log here.
		h.logger.WithField("event_id", event.EventID).
			Warn("Dropping malformed push_rewrite before durable error handling")
		return false
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_PushRewrite)
	if !ok || tp == nil || tp.PushRewrite == nil {
		h.logger.WithField("event_id", event.EventID).
			Warn("Dropping malformed push_rewrite before durable error handling")
		return false
	}
	streamName := tp.PushRewrite.GetStreamName()
	pushURL := tp.PushRewrite.GetPushUrl()
	if strings.HasPrefix(streamName, "live+") && !streamKeyTokenPattern.MatchString(pushURL) {
		return true
	}
	fields := logging.Fields{"event_id": event.EventID}
	if !strings.HasPrefix(streamName, "live+") {
		fields["stream_key"] = logging.RedactSecret(streamName)
	}
	h.logger.WithFields(fields).
		Warn("Dropping push_rewrite event that violates the credential-free payload contract")
	return false
}

// Generated stream keys begin with sk_. Require a token boundary so an
// unrelated hostname or path fragment containing those letters is not treated
// as a credential. The producer replaces the key with its sk# log tag before
// publishing, so a matching token in push_url is always the unsafe shape.
var streamKeyTokenPattern = regexp.MustCompile(`(?:^|[^A-Za-z0-9])sk_[A-Za-z0-9_-]+`)

// HandleServiceEvent processes service-plane events from the service_events topic.
func (h *AnalyticsHandler) HandleServiceEvent(event kafka.ServiceEvent) error {
	start := time.Now()
	ctx := context.Background()

	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "received").Inc()
	}

	var err error
	switch event.EventType {
	case "api_request_batch":
		err = h.processServiceAPIRequestBatch(ctx, event)
	case "artifact_node_copy":
		err = h.processArtifactNodeCopy(ctx, event)
	case "tenant_created":
		if err = h.processTenantCreated(ctx, event); err != nil {
			break
		}
		err = h.processServiceEventAudit(ctx, event)
	default:
		err = h.processServiceEventAudit(ctx, event)
	}

	if err != nil {
		if errors.Is(err, errDropped) {
			return nil
		}
		h.logger.WithError(err).WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
		}).Error("Failed to process service event")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
		h.metrics.BatchProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
	}

	return nil
}

// HandleRawMistTriggerMessage ingests Decklog's audit republish of the
// original MistTrigger envelope into the periscope.raw_mist_triggers
// ClickHouse table. The dedupe key is (node_id, trigger_type,
// source_request_id) — duplicate Kafka deliveries collapse on the table's
// ReplacingMergeTree(ingested_at_ms) via argMax-on-read. See
// docs/architecture/trigger-durability.md.
func (h *AnalyticsHandler) HandleRawMistTriggerMessage(ctx context.Context, msg kafka.Message) error {
	var trigger ipcpb.MistTrigger
	if err := proto.Unmarshal(msg.Value, &trigger); err != nil {
		h.logger.WithError(err).WithField("topic", msg.Topic).Error("Failed to unmarshal raw MistTrigger; dropping poison message")
		return nil
	}

	sourceRequestID := trigger.GetRequestId()
	if sourceRequestID == "" {
		// Headers may carry it as a fallback (Decklog stamps both).
		if v, ok := msg.Headers["source_event_id"]; ok {
			sourceRequestID = v
		}
	}
	if sourceRequestID == "" {
		h.logger.WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
		}).Warn("Skipping raw MistTrigger with empty source_request_id")
		return nil
	}

	nodeID := trigger.GetNodeId()
	if nodeID == "" {
		if v, ok := msg.Headers["node_id"]; ok {
			nodeID = v
		}
	}
	triggerType := trigger.GetTriggerType()
	if triggerType == "" {
		if v, ok := msg.Headers["trigger_type"]; ok {
			triggerType = v
		}
	}
	tenantID := trigger.GetTenantId()
	if tenantID == "" {
		if v, ok := msg.Headers["tenant_id"]; ok {
			tenantID = v
		}
	}
	clusterID := trigger.GetClusterId()
	if clusterID == "" {
		if v, ok := msg.Headers["cluster_id"]; ok {
			clusterID = v
		}
	}
	// Projection consumes the protobuf envelope, while the raw journal also
	// accepts Kafka header fallbacks. Keep both views aligned before projecting.
	if trigger.GetNodeId() == "" && nodeID != "" {
		trigger.NodeId = nodeID
	}
	if trigger.GetTriggerType() == "" && triggerType != "" {
		trigger.TriggerType = triggerType
	}
	if trigger.GetTenantId() == "" && tenantID != "" {
		trigger.TenantId = &tenantID
	}
	if trigger.GetClusterId() == "" && clusterID != "" {
		trigger.ClusterId = &clusterID
	}

	receivedAtMS := trigger.GetTimestamp()
	if receivedAtMS == 0 {
		receivedAtMS = time.Now().UnixMilli()
	} else if receivedAtMS < 1_000_000_000_000 {
		receivedAtMS *= 1000
	}
	ingestedAtMS := time.Now().UnixMilli()

	batch, err := periscopeingestdb.PrepareRawMistTrigger(ctx, h.clickhouse)
	if err != nil {
		return fmt.Errorf("raw_mist_triggers prepare: %w", err)
	}
	defer func() { _ = batch.Close() }()
	if err := batch.Append(periscopeingestdb.RawMistTriggerRow{
		NodeID: nodeID, TriggerType: triggerType, SourceRequestID: sourceRequestID,
		Payload: msg.Value, TenantID: tenantID, ClusterID: clusterID,
		ReceivedAtMS:  receivedAtMS,
		ForwardedAtMS: ingestedAtMS, // Producer does not stamp forwarded_at_ms today.
		IngestedAtMS:  ingestedAtMS, SchemaVersion: int32(trigger.GetSchemaVersion()),
	}); err != nil {
		return fmt.Errorf("raw_mist_triggers append: %w", err)
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("raw_mist_triggers send: %w", err)
	}
	if h.metrics != nil && h.metrics.ClickHouseInserts != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("raw_mist_triggers", "inserted").Inc()
	}

	// Project final-fact rows for the trigger types Decklog republished
	// here. Append-only projection per meter-contracts.md. Returning the
	// projection error retries the Kafka message; the raw journal insert
	// above is idempotent on source_request_id, and the final-fact
	// projection is billing-critical.
	if err := h.projectFinalFact(ctx, &trigger, sourceRequestID, receivedAtMS); err != nil {
		h.logger.WithError(err).WithFields(logging.Fields{
			"trigger_type":    triggerType,
			"source_event_id": sourceRequestID,
		}).Warn("Final-fact projection failed; retrying raw trigger message")
		return fmt.Errorf("project final fact: %w", err)
	}
	return nil
}

func (h *AnalyticsHandler) skipEvent(event kafka.AnalyticsEvent, reason string) error {
	fields := logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
	}
	if reason != "" {
		fields["reason"] = reason
	}
	h.logger.WithFields(fields).Info("Skipping analytics event")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "skipped").Inc()
	}
	return nil
}

// processStorageSnapshot handles StorageSnapshot events
func (h *AnalyticsHandler) processStorageSnapshot(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing storage snapshot event: %s", event.EventID)

	// Parse MistTrigger envelope -> StorageSnapshot
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_StorageSnapshot)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for storage_snapshot")
	}
	storageSnapshot := tp.StorageSnapshot

	// Write to ClickHouse for each tenant's usage in the snapshot.
	// cluster_id flows through from the MistTrigger envelope so storage
	// rollups can be billed per cluster.
	batch, err := periscopeingestdb.PrepareStorageSnapshot(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch for storage_snapshots: %v", err)
		return err
	}
	defer func() { _ = batch.Close() }()

	storageScope := storageSnapshot.GetStorageScope()
	if storageScope == "" {
		storageScope = "hot"
	}

	snapshotTimestamp := event.Timestamp
	if ts := storageSnapshot.GetTimestamp(); ts > 0 {
		snapshotTimestamp = time.Unix(ts, 0)
	}

	clusterID := mt.GetClusterId()

	// Provider attribution: the emitter (Foghorn for cold, Helmsman for hot)
	// stamps `storage_provider_tenant_id` etc. on the snapshot. Missing
	// provider IDs are kept empty so downstream settlement can distinguish
	// explicit provider attribution from unattributed storage.
	providerTenantID := storageSnapshot.GetStorageProviderTenantId()
	providerClusterID := storageSnapshot.GetStorageProviderClusterId()
	storageBackend := storageSnapshot.GetStorageBackend()
	if storageBackend == "" {
		if storageScope == "cold" {
			storageBackend = "s3"
		} else {
			storageBackend = "edge_disk"
		}
	}
	if providerTenantID == "" {
		h.logger.WithFields(logging.Fields{
			"event_id":      event.EventID,
			"node_id":       storageSnapshot.GetNodeId(),
			"storage_scope": storageScope,
		}).Warn("storage_snapshot missing storage_provider_tenant_id; defaulting to empty (counts as platform attribution downstream)")
	}

	// ingested_at_ms stamps this pass's wall-clock time so the storage
	// rebuilder can cursor on ingest time, not the source `timestamp`.
	// Late-arriving snapshots (where ingest happens minutes/hours after
	// the recorded timestamp) still land in a future rebuilder pass.
	ingestedAtMS := time.Now().UnixMilli()

	for _, usage := range storageSnapshot.GetUsage() {
		if !isValidUUIDString(usage.GetTenantId()) {
			h.logger.WithFields(logging.Fields{
				"event_id":  event.EventID,
				"tenant_id": usage.GetTenantId(),
				"node_id":   storageSnapshot.GetNodeId(),
			}).Warn("Skipping storage snapshot row: missing or invalid tenant_id")
			continue
		}
		tenantID := uuid.MustParse(usage.GetTenantId())
		if err := batch.Append(periscopeingestdb.StorageSnapshotRow{
			Timestamp: snapshotTimestamp, NodeID: storageSnapshot.GetNodeId(), TenantID: tenantID,
			ClusterID: clusterID, StorageScope: storageScope,
			StorageProviderTenantID: providerTenantID, StorageProviderClusterID: providerClusterID, StorageBackend: storageBackend,
			TotalBytes: usage.GetTotalBytes(), FileCount: usage.GetFileCount(), DVRBytes: usage.GetDvrBytes(),
			ClipBytes: usage.GetClipBytes(), VODBytes: usage.GetVodBytes(), FrozenDVRBytes: usage.GetFrozenDvrBytes(),
			FrozenClipBytes: usage.GetFrozenClipBytes(), FrozenVODBytes: usage.GetFrozenVodBytes(), IngestedAtMS: ingestedAtMS,
		}); err != nil {
			h.logger.Errorf("Failed to append to storage_snapshots batch: %v", err)
			return err
		}
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send storage_snapshots batch: %v", err)
		return err
	}

	return nil
}

// processStreamLifecycle handles stream lifecycle events
// Dual-writes to: live_streams (current state) + stream_events (historical log)
func (h *AnalyticsHandler) processStreamLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> StreamLifecycleUpdate
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	streamID := mistTriggerStreamID(&mt)
	if !isValidUUIDString(streamID) {
		h.logger.WithFields(logging.Fields{
			"event_id":  event.EventID,
			"tenant_id": event.TenantID,
			"stream_id": streamID,
		}).Warn("Stream lifecycle event missing or invalid stream_id; skipping to avoid corrupting current state")
		return nil
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_StreamLifecycleUpdate)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_lifecycle_update")
	}
	streamLifecycle := tp.StreamLifecycleUpdate
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamLifecycle.GetInternalName())
	env := analyticsEnvelopeColumns(event)

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "attempt").Inc()
	}

	// 1. Write to live_streams (current state - ReplacingMergeTree)
	// This is the primary source of truth for stream status
	stateBatch, err := periscopeingestdb.PrepareStreamLifecycleState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_streams batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return err
	}
	defer func() { _ = stateBatch.Close() }()

	// Derive status from buffer state
	status := "live"
	if streamLifecycle.GetStatus() != "" {
		status = streamLifecycle.GetStatus()
	}

	// Derive buffer_state if not set but buffer_ms is available.
	// live_streams.buffer_state is non-nullable; prefer a reasonable default over empty.
	bufferState := streamLifecycle.GetBufferState()
	if bufferState == "" && streamLifecycle.GetBufferMs() > 0 {
		bufferState = "FULL"
	}

	var startedAt *time.Time
	if streamLifecycle.StartedAt != nil && *streamLifecycle.StartedAt > 0 {
		value := time.Unix(*streamLifecycle.StartedAt, 0)
		startedAt = &value
	} else if status == "live" {
		if existingStartedAt, ok := h.lookupCurrentLiveStreamStartedAt(ctx, event.TenantID, parseUUID(streamID)); ok {
			startedAt = &existingStartedAt
		} else {
			startedAt = &event.Timestamp
		}
	} else if existingStartedAt, ok := h.lookupCurrentStreamStartedAt(ctx, event.TenantID, parseUUID(streamID)); ok {
		startedAt = &existingStartedAt
	}

	tenantUUID := uuid.MustParse(event.TenantID)
	if appendErr := stateBatch.Append(periscopeingestdb.StreamLifecycleStateRow{
		TenantID: tenantUUID, StreamID: parseUUID(streamID), InternalName: internalName, NodeID: mt.GetNodeId(),
		Status: status, BufferState: bufferState, CurrentViewers: streamLifecycle.GetTotalViewers(),
		TotalInputs: uint16(streamLifecycle.GetTotalInputs()), UploadedBytes: streamLifecycle.GetUploadedBytes(),
		DownloadedBytes: streamLifecycle.GetDownloadedBytes(), ViewerSeconds: streamLifecycle.GetViewerSeconds(),
		HasIssues: optionalBoolUInt8(streamLifecycle.GetHasIssues()), IssuesDescription: optionalString(streamLifecycle.GetIssuesDescription()),
		TrackCount: optionalUint16(streamLifecycle.GetTrackCount()), QualityTier: optionalString(streamLifecycle.GetQualityTier()),
		PrimaryWidth: optionalUint16(streamLifecycle.GetPrimaryWidth()), PrimaryHeight: optionalUint16(streamLifecycle.GetPrimaryHeight()),
		PrimaryFPS: optionalFloat32(streamLifecycle.GetPrimaryFps()), PrimaryCodec: optionalString(streamLifecycle.GetPrimaryCodec()),
		PrimaryBitrate: optionalUint32(uint32(streamLifecycle.GetPrimaryBitrate())), PacketsSent: streamLifecycle.PacketsSent,
		PacketsLost: streamLifecycle.PacketsLost, PacketsRetransmitted: streamLifecycle.PacketsRetransmitted,
		StartedAt: startedAt, UpdatedAt: event.Timestamp,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to live_streams batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_streams batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_streams", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "attempt").Inc()
	}

	// 2. Write to stream_events (historical log - MergeTree)
	eventBatch, err := periscopeingestdb.PrepareStreamLifecycleEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_events batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return err
	}
	defer func() { _ = eventBatch.Close() }()

	statusValue, bufferStateValue := status, streamLifecycle.GetBufferState()
	downloadedBytes, uploadedBytes := streamLifecycle.GetDownloadedBytes(), streamLifecycle.GetUploadedBytes()
	totalViewers, totalInputs, totalOutputs := streamLifecycle.GetTotalViewers(), uint16(streamLifecycle.GetTotalInputs()), uint16(0)
	viewerSeconds := streamLifecycle.GetViewerSeconds()
	if appendErr := eventBatch.Append(periscopeingestdb.StreamLifecycleEventRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: tenantUUID, StreamID: parseUUID(streamID),
		InternalName: internalName, NodeID: mt.GetNodeId(), ClusterID: mt.GetClusterId(), EventType: "stream_lifecycle", Status: &statusValue,
		BufferState: &bufferStateValue, DownloadedBytes: &downloadedBytes, UploadedBytes: &uploadedBytes,
		TotalViewers: &totalViewers, TotalInputs: &totalInputs, TotalOutputs: &totalOutputs, ViewerSeconds: &viewerSeconds,
		HasIssues: optionalBoolUInt8(streamLifecycle.GetHasIssues()), IssuesDescription: optionalString(streamLifecycle.GetIssuesDescription()),
		TrackCount: optionalUint16(streamLifecycle.GetTrackCount()), QualityTier: optionalString(streamLifecycle.GetQualityTier()),
		PrimaryWidth: optionalUint16(streamLifecycle.GetPrimaryWidth()), PrimaryHeight: optionalUint16(streamLifecycle.GetPrimaryHeight()),
		PrimaryFPS: optionalFloat32(streamLifecycle.GetPrimaryFps()), EventData: marshalTypedEventData(&streamLifecycle),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return appendErr
	}

	if sendErr := eventBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_events", "success").Inc()
	}

	// 3. Write to stream_health_metrics (for health charts - every 10s sample)
	// This provides continuous health data from polling, not just sparse STREAM_BUFFER triggers
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "attempt").Inc()
	}

	healthBatch, err := periscopeingestdb.PrepareStreamLifecycleHealth(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}
	defer func() { _ = healthBatch.Close() }()

	// Calculate buffer_health ratio (0.0-1.0): buffer_ms / max_keepaway_ms.
	var bufferHealth *float32
	if streamLifecycle.GetBufferMs() > 0 && streamLifecycle.GetMaxKeepawayMs() > 0 {
		ratio := float32(streamLifecycle.GetBufferMs()) / float32(streamLifecycle.GetMaxKeepawayMs())
		if ratio > 1 {
			ratio = 1
		}
		bufferHealth = &ratio
	}

	// ClickHouse JSON type expects an object at the top level. Store track details under { "tracks": [...] }.
	trackMetadataJSON := "{}"
	if raw := strings.TrimSpace(streamLifecycle.GetTrackDetailsJson()); raw != "" {
		if strings.HasPrefix(raw, "{") {
			trackMetadataJSON = raw
		} else if strings.HasPrefix(raw, "[") {
			trackMetadataJSON = fmt.Sprintf("{\"tracks\":%s}", raw)
		}
	}

	var audioChannels *uint8
	if v := streamLifecycle.GetAudioChannels(); v > 0 {
		value := uint8(v)
		audioChannels = &value
	}

	if err := healthBatch.Append(periscopeingestdb.StreamLifecycleHealthRow{
		Timestamp: event.Timestamp, TenantID: tenantUUID, StreamID: parseUUID(streamID), InternalName: internalName, NodeID: mt.GetNodeId(),
		Bitrate: optionalUint32(uint32(streamLifecycle.GetPrimaryBitrate())), FPS: optionalFloat32(streamLifecycle.GetPrimaryFps()),
		Width: optionalUint16(streamLifecycle.GetPrimaryWidth()), Height: optionalUint16(streamLifecycle.GetPrimaryHeight()),
		Codec: optionalString(streamLifecycle.GetPrimaryCodec()), QualityTier: optionalString(streamLifecycle.GetQualityTier()),
		BufferState: bufferState, BufferSize: optionalUint32(streamLifecycle.GetBufferMs()), BufferHealth: bufferHealth,
		HasIssues: optionalBoolUInt8(streamLifecycle.GetHasIssues()), IssuesDescription: optionalString(streamLifecycle.GetIssuesDescription()),
		TrackCount: optionalUint16(streamLifecycle.GetTrackCount()), TrackMetadata: trackMetadataJSON,
		AudioChannels: audioChannels, AudioSampleRate: optionalUint32(streamLifecycle.GetAudioSampleRate()),
		AudioCodec: optionalString(streamLifecycle.GetAudioCodec()), AudioBitrate: optionalUint32(streamLifecycle.GetAudioBitrate()),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}

	if err := healthBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send stream_health_metrics: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("stream_health_metrics", "success").Inc()
	}

	return nil
}

// processViewerConnection writes connection_events (connect/disconnect) to ClickHouse
func (h *AnalyticsHandler) processViewerConnection(ctx context.Context, event kafka.AnalyticsEvent, isConnect bool) error {
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	streamID := mistTriggerStreamID(&mt)
	if err := h.requireStreamID(ctx, event, streamID); err != nil {
		return err
	}
	if h.isDuplicateEvent(ctx, "viewer_connection_events", parseUUID(event.EventID), event.EventType) {
		return nil
	}

	var streamName, sessionID, connector, nodeID, host, requestURL string
	var duration, upBytes, downBytes int64
	var secondsConnected uint64
	countryCode := "--"
	city := ""
	latitude := float64(0)
	longitude := float64(0)
	var clientBucketH3 *uint64
	var clientBucketRes *uint8
	var nodeBucketH3 *uint64
	var nodeBucketRes *uint8

	payloadIsConnect := false
	payloadType := ""
	switch p := mt.GetTriggerPayload().(type) {
	case *ipcpb.MistTrigger_ViewerConnect:
		payloadIsConnect = true
		payloadType = "viewer_connect"
		vc := p.ViewerConnect
		streamName = vc.GetStreamName()
		sessionID = vc.GetSessionId()
		connector = vc.GetConnector()
		host = vc.GetHost()
		requestURL = vc.GetRequestUrl()
		nodeID = mt.GetNodeId()
		if vc.GetClientCountry() != "" {
			countryCode = vc.GetClientCountry()
		}
		if vc.GetClientCity() != "" {
			city = vc.GetClientCity()
		}
		if vc.ClientLatitude != nil {
			latitude = vc.GetClientLatitude()
		}
		if vc.ClientLongitude != nil {
			longitude = vc.GetClientLongitude()
		}
		if bucket := vc.GetClientBucket(); bucket != nil && bucket.H3Index != 0 {
			h3, resolution := bucket.H3Index, uint8(bucket.Resolution)
			clientBucketH3, clientBucketRes = &h3, &resolution
		}
		if bucket := vc.GetNodeBucket(); bucket != nil && bucket.H3Index != 0 {
			h3, resolution := bucket.H3Index, uint8(bucket.Resolution)
			nodeBucketH3, nodeBucketRes = &h3, &resolution
		}
	case *ipcpb.MistTrigger_ViewerDisconnect:
		payloadIsConnect = false
		payloadType = "viewer_disconnect"
		vd := p.ViewerDisconnect
		streamName = vd.GetStreamName()
		sessionID = vd.GetSessionId()
		connector = vd.GetConnector()
		host = vd.GetHost()
		nodeID = vd.GetNodeId()
		duration = vd.GetDuration()
		secondsConnected = vd.GetSecondsConnected()
		upBytes = vd.GetUpBytes()
		downBytes = vd.GetDownBytes()
		if vd.GetCountryCode() != "" {
			countryCode = vd.GetCountryCode()
		}
		if vd.GetCity() != "" {
			city = vd.GetCity()
		}
		if vd.Latitude != nil {
			latitude = vd.GetLatitude()
		}
		if vd.Longitude != nil {
			longitude = vd.GetLongitude()
		}
		if bucket := vd.GetClientBucket(); bucket != nil && bucket.H3Index != 0 {
			h3, resolution := bucket.H3Index, uint8(bucket.Resolution)
			clientBucketH3, clientBucketRes = &h3, &resolution
		}
		if bucket := vd.GetNodeBucket(); bucket != nil && bucket.H3Index != 0 {
			h3, resolution := bucket.H3Index, uint8(bucket.Resolution)
			nodeBucketH3, nodeBucketRes = &h3, &resolution
		}
	default:
		return fmt.Errorf("unexpected payload for viewer connection")
	}
	if payloadIsConnect != isConnect {
		expectedType := map[bool]string{true: "viewer_connect", false: "viewer_disconnect"}[isConnect]
		return fmt.Errorf("viewer connection payload mismatch: expected %s, got %s", expectedType, payloadType)
	}

	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	streamName = mist.ExtractInternalName(streamName)

	clusterID := mt.GetClusterId()
	originClusterID := mt.GetOriginClusterId()
	if clusterID == "" {
		clusterID = originClusterID
	}
	if originClusterID == "" {
		originClusterID = clusterID
	}
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareViewerConnectionEvent(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer func() { _ = batch.Close() }()

	eventType := map[bool]string{true: "connect", false: "disconnect"}[isConnect]
	durationUI := uint32(0)
	bytesTransferred := uint64(0)
	if !isConnect {
		if duration > 0 {
			durationUI = uint32(duration)
		} else if secondsConnected > 0 {
			durationUI = uint32(secondsConnected)
		}
		bytesTransferred = uint64(max64(0, upBytes) + max64(0, downBytes))
	}

	if err := batch.Append(periscopeingestdb.ViewerConnectionEventRow{
		EventID: parseUUID(event.EventID), Timestamp: event.Timestamp, TenantID: uuid.MustParse(event.TenantID), StreamID: parseUUID(streamID),
		InternalName: streamName, SessionID: sessionID, ConnectionAddr: host, Connector: connector, NodeID: nodeID,
		ClusterID: clusterID, OriginClusterID: originClusterID, RequestURL: optionalString(requestURL),
		CountryCode: countryCode, City: city, Latitude: latitude, Longitude: longitude,
		ClientBucketH3: clientBucketH3, ClientBucketRes: clientBucketRes,
		NodeBucketH3: nodeBucketH3, NodeBucketRes: nodeBucketRes,
		EventType: eventType, SessionDuration: durationUI, BytesTransferred: bytesTransferred,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		return err
	}
	return batch.Send()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func nilIfZeroFloat32(v float32) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nilIfZeroBool(v bool) interface{} {
	if !v {
		return nil
	}
	return v
}
func nilIfZeroUint64(v uint64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nilIfZeroUint32(v uint32) interface{} {
	if v == 0 {
		return nil
	}
	return v
}
func nonNegativeUint64(v int64) uint64 {
	if v <= 0 {
		return 0
	}
	return uint64(v)
}
func nilIfZeroUint8(v int32) interface{} {
	// Proto returns int32, convert to uint8 for ClickHouse
	if v == 0 {
		return nil
	}
	return uint8(v)
}
func nilIfZeroUint16(v int32) interface{} {
	// Proto returns int32, convert to uint16 for ClickHouse
	if v == 0 {
		return nil
	}
	return uint16(v)
}
func nilIfEmptyString(v string) interface{} {
	if v == "" {
		return nil
	}
	return v
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func optionalFloat32(value float32) *float32 {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalBoolUInt8(value bool) *uint8 {
	if !value {
		return nil
	}
	one := uint8(1)
	return &one
}

func optionalUint16(value int32) *uint16 {
	if value == 0 {
		return nil
	}
	converted := uint16(value)
	return &converted
}

func optionalUint8(value int32) *uint8 {
	if value == 0 {
		return nil
	}
	converted := uint8(value)
	return &converted
}

func optionalUint32(value uint32) *uint32 {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalUint64(value uint64) *uint64 {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

func optionalUnixTime(value int64) *time.Time {
	if value == 0 {
		return nil
	}
	converted := time.Unix(value, 0)
	return &converted
}

func optionalStringPointer(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	return value
}

func optionalInt32Pointer(value *int32) *int32 {
	if value == nil || *value == 0 {
		return nil
	}
	return value
}

func optionalInt64Pointer(value *int64) *int64 {
	if value == nil || *value == 0 {
		return nil
	}
	return value
}

func optionalFloat64Pointer(value *float64) *float64 {
	if value == nil || *value == 0 {
		return nil
	}
	return value
}

func optionalBoolUint8Pointer(value *bool) *uint8 {
	if value == nil {
		return nil
	}
	converted := boolToUint8(*value)
	return &converted
}

func optionalUint32FromInt32(value int32) *uint32 {
	if value == 0 {
		return nil
	}
	converted := uint32(value)
	return &converted
}

func parseUUID(value string) uuid.UUID {
	if value == "" {
		return uuid.Nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return parsed
}

func parseUUIDOrNil(value string) interface{} {
	if value == "" {
		return nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil
	}
	if parsed == uuid.Nil {
		return nil
	}
	return parsed
}

func optionalUUID(value string) *uuid.UUID {
	parsed := parseUUID(value)
	if parsed == uuid.Nil {
		return nil
	}
	return &parsed
}

func (h *AnalyticsHandler) lookupCurrentStreamStartedAt(ctx context.Context, tenantID string, streamID uuid.UUID) (time.Time, bool) {
	return h.lookupCurrentStreamStartedAtByStatus(ctx, tenantID, streamID, "")
}

func (h *AnalyticsHandler) lookupCurrentLiveStreamStartedAt(ctx context.Context, tenantID string, streamID uuid.UUID) (time.Time, bool) {
	return h.lookupCurrentStreamStartedAtByStatus(ctx, tenantID, streamID, "live")
}

func (h *AnalyticsHandler) lookupCurrentStreamStartedAtByStatus(ctx context.Context, tenantID string, streamID uuid.UUID, requiredStatus string) (time.Time, bool) {
	tenantUUID, err := uuid.Parse(strings.TrimSpace(tenantID))
	if err != nil || tenantUUID == uuid.Nil || streamID == uuid.Nil {
		return time.Time{}, false
	}
	query := `
		SELECT started_at
		FROM periscope.stream_state_current FINAL
		WHERE tenant_id = ?
		  AND stream_id = ?
		  AND started_at IS NOT NULL`
	args := []any{tenantUUID, streamID}
	if requiredStatus != "" {
		query += " AND status = ?"
		args = append(args, requiredStatus)
	}
	query += " LIMIT 1"
	rows, err := h.clickhouse.Query(ctx, query, args...)
	if err != nil {
		h.logger.WithError(err).Debug("Stream started_at lookup failed")
		return time.Time{}, false
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return time.Time{}, false
	}
	var startedAt time.Time
	if err := rows.Scan(&startedAt); err != nil {
		h.logger.WithError(err).Debug("Stream started_at lookup scan failed")
		return time.Time{}, false
	}
	if startedAt.IsZero() {
		return time.Time{}, false
	}
	return startedAt.UTC(), true
}

func (h *AnalyticsHandler) isDuplicateEvent(ctx context.Context, table string, eventID uuid.UUID, eventType string) bool {
	if eventID == uuid.Nil {
		return false
	}
	rows, err := h.clickhouse.Query(ctx, fmt.Sprintf("SELECT 1 FROM %s WHERE event_id = ? LIMIT 1", table), eventID)
	if err != nil {
		h.logger.WithError(err).WithField("event_id", eventID).Warn("Failed to check for duplicate event")
		return false
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		h.logger.WithField("event_id", eventID).WithField("table", table).Debug("Skipping duplicate event")
		if h.metrics != nil && h.metrics.DuplicateEvents != nil {
			h.metrics.DuplicateEvents.WithLabelValues(eventType).Inc()
		}
		return true
	}
	return false
}

func isValidUUIDString(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return false
	}
	return parsed != uuid.Nil
}

func mistTriggerStreamID(mt *ipcpb.MistTrigger) string {
	if mt == nil {
		return ""
	}
	if streamID := mt.GetStreamId(); isValidUUIDString(streamID) {
		return streamID
	}

	switch p := mt.GetTriggerPayload().(type) {
	case *ipcpb.MistTrigger_PushRewrite:
		return validPayloadStreamID(p.PushRewrite.GetStreamId())
	case *ipcpb.MistTrigger_PlayRewrite:
		return validPayloadStreamID(p.PlayRewrite.GetStreamId())
	case *ipcpb.MistTrigger_StreamSource:
		return validPayloadStreamID(p.StreamSource.GetStreamId())
	case *ipcpb.MistTrigger_PushOutStart:
		return validPayloadStreamID(p.PushOutStart.GetStreamId())
	case *ipcpb.MistTrigger_PushEnd:
		return validPayloadStreamID(p.PushEnd.GetStreamId())
	case *ipcpb.MistTrigger_ViewerConnect:
		return validPayloadStreamID(p.ViewerConnect.GetStreamId())
	case *ipcpb.MistTrigger_ViewerDisconnect:
		return validPayloadStreamID(p.ViewerDisconnect.GetStreamId())
	case *ipcpb.MistTrigger_StreamBuffer:
		return validPayloadStreamID(p.StreamBuffer.GetStreamId())
	case *ipcpb.MistTrigger_StreamEnd:
		return validPayloadStreamID(p.StreamEnd.GetStreamId())
	case *ipcpb.MistTrigger_TrackList:
		return validPayloadStreamID(p.TrackList.GetStreamId())
	case *ipcpb.MistTrigger_RecordingComplete:
		return validPayloadStreamID(p.RecordingComplete.GetStreamId())
	case *ipcpb.MistTrigger_RecordingSegment:
		return validPayloadStreamID(p.RecordingSegment.GetStreamId())
	case *ipcpb.MistTrigger_StreamLifecycleUpdate:
		return validPayloadStreamID(p.StreamLifecycleUpdate.GetStreamId())
	case *ipcpb.MistTrigger_ClientLifecycleUpdate:
		return validPayloadStreamID(p.ClientLifecycleUpdate.GetStreamId())
	case *ipcpb.MistTrigger_ClientLifecycleBatch:
		return validPayloadStreamID(p.ClientLifecycleBatch.GetStreamId())
	case *ipcpb.MistTrigger_LoadBalancingData:
		return validPayloadStreamID(p.LoadBalancingData.GetStreamId())
	case *ipcpb.MistTrigger_ClipLifecycleData:
		return validPayloadStreamID(p.ClipLifecycleData.GetStreamId())
	case *ipcpb.MistTrigger_DvrLifecycleData:
		return validPayloadStreamID(p.DvrLifecycleData.GetStreamId())
	case *ipcpb.MistTrigger_StorageLifecycleData:
		return validPayloadStreamID(p.StorageLifecycleData.GetStreamId())
	case *ipcpb.MistTrigger_FederationEventData:
		return validPayloadStreamID(p.FederationEventData.GetStreamId())
	}
	return mt.GetStreamId()
}

func validPayloadStreamID(streamID string) string {
	if isValidUUIDString(streamID) {
		return streamID
	}
	return ""
}

func getStringFromMap(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func getInt64FromMap(data map[string]interface{}, key string) (int64, bool) {
	if data == nil {
		return 0, false
	}
	value, ok := data[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	case string:
		// protojson encodes int64/uint64 as decimal strings; Decklog serializes
		// ServiceEvent payloads via protojson, so these arrive as strings.
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func getUint64FromMap(data map[string]interface{}, key string) uint64 {
	if data == nil {
		return 0
	}
	value, ok := data[key]
	if !ok {
		return 0
	}
	switch v := value.(type) {
	case uint64:
		return v
	case uint32:
		return uint64(v)
	case int64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case int:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case float64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case float32:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil || n < 0 {
			return 0
		}
		return uint64(n)
	default:
		return 0
	}
}

var (
	errDropped         = errors.New("dropped")
	errMissingTenantID = errors.New("missing_or_invalid_tenant_id")
)

func (h *AnalyticsHandler) writeIngestError(ctx context.Context, event kafka.AnalyticsEvent, streamID string, reason string, cause error) {
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "attempt").Inc()
	}

	payloadJSON := "{}"
	if event.Data != nil {
		if bytes, err := json.Marshal(event.Data); err == nil {
			payloadJSON = string(bytes)
		} else {
			// Preserve reason but note payload marshal failure.
			reason = fmt.Sprintf("%s (payload_marshal_error: %v)", reason, err)
		}
	}

	errorMessage := reason
	if cause != nil {
		errorMessage = fmt.Sprintf("%s: %v", reason, cause)
	}
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareIngestError(ctx, h.clickhouse)
	if err != nil {
		h.logger.WithError(err).Error("Failed to prepare ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}
	defer func() { _ = batch.Close() }()

	if appendErr := batch.Append(periscopeingestdb.IngestErrorRow{
		ReceivedAt: event.Timestamp, EventID: event.EventID, EventType: event.EventType, Source: event.Source,
		TenantID: event.TenantID, StreamID: streamID, Error: errorMessage, PayloadJSON: payloadJSON,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.WithError(appendErr).Error("Failed to append ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}

	if err := batch.Send(); err != nil {
		h.logger.WithError(err).Error("Failed to send ingest_errors batch")
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "error").Inc()
		}
		return
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("ingest_errors", "success").Inc()
	}
}

func (h *AnalyticsHandler) requireTenantID(ctx context.Context, event kafka.AnalyticsEvent) error {
	if isValidUUIDString(event.TenantID) {
		return nil
	}

	h.writeIngestError(ctx, event, "", "missing_or_invalid_tenant_id", nil)
	h.logger.WithFields(logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
		"tenant_id":  event.TenantID,
	}).Warn("Dropping analytics event: missing or invalid tenant_id")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
	}
	return errMissingTenantID
}

func (h *AnalyticsHandler) requireStreamID(ctx context.Context, event kafka.AnalyticsEvent, streamID string) error {
	if isValidUUIDString(streamID) {
		return nil
	}

	h.writeIngestError(ctx, event, streamID, "missing_or_invalid_stream_id", nil)
	h.logger.WithFields(logging.Fields{
		"event_type": event.EventType,
		"event_id":   event.EventID,
		"tenant_id":  event.TenantID,
		"stream_id":  streamID,
	}).Warn("Dropping analytics event: missing or invalid stream_id")
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
	}
	return errDropped
}

// processPushRewrite handles PUSH_REWRITE events (publisher ingest start)
func (h *AnalyticsHandler) processPushRewrite(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing push rewrite event: %s", event.EventID)
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_PushRewrite)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for push_rewrite")
	}
	pr := tp.PushRewrite
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(pr.GetStreamName())
	env := analyticsEnvelopeColumns(event)
	streamUUID := parseUUID(mistTriggerStreamID(&mt))

	if streamUUID != uuid.Nil {
		startedAt := event.Timestamp
		if existingStartedAt, ok := h.lookupCurrentLiveStreamStartedAt(ctx, event.TenantID, streamUUID); ok {
			startedAt = existingStartedAt
		}
		stateBatch, err := periscopeingestdb.PrepareStreamLifecycleState(ctx, h.clickhouse)
		if err != nil {
			return err
		}
		defer stateBatch.Close()
		if appendErr := stateBatch.Append(periscopeingestdb.StreamLifecycleStateRow{
			TenantID: uuid.MustParse(event.TenantID), StreamID: streamUUID, InternalName: internalName,
			NodeID: mt.GetNodeId(), Status: "live", BufferState: "UNKNOWN", TotalInputs: 1,
			StartedAt: &startedAt, UpdatedAt: event.Timestamp,
		}); appendErr != nil {
			return appendErr
		}
		if sendErr := stateBatch.Send(); sendErr != nil {
			return sendErr
		}
	}

	if h.isDuplicateEvent(ctx, "stream_event_log", parseUUID(event.EventID), event.EventType) {
		return nil
	}

	batch, err := periscopeingestdb.PreparePushRewriteEvent(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer batch.Close()

	var prot *string
	if pr.Protocol != nil && *pr.Protocol != "" {
		prot = pr.Protocol
	}
	// Prefer publisher geo (client-side) when available; otherwise fall back to node geo.
	var lat *float64
	if pr.PublisherLatitude != nil {
		lat = pr.PublisherLatitude
	} else if pr.Latitude != nil {
		lat = pr.Latitude
	}
	var lon *float64
	if pr.PublisherLongitude != nil {
		lon = pr.PublisherLongitude
	} else if pr.Longitude != nil {
		lon = pr.Longitude
	}
	// Publisher location (where encoder is running, from GeoIP)
	var pubCountry *string
	if pr.PublisherCountryCode != nil && *pr.PublisherCountryCode != "" {
		pubCountry = pr.PublisherCountryCode
	}
	var pubCity *string
	if pr.PublisherCity != nil && *pr.PublisherCity != "" {
		pubCity = pr.PublisherCity
	}

	status := "live"
	requestURL := pr.GetPushUrl()
	if appendErr := batch.Append(periscopeingestdb.PushRewriteEventRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: uuid.MustParse(event.TenantID),
		StreamID: streamUUID, InternalName: internalName, NodeID: mt.GetNodeId(), ClusterID: mt.GetClusterId(),
		EventType: "stream_start", Status: &status, RequestURL: &requestURL, Protocol: prot,
		Latitude: lat, Longitude: lon, CountryCode: pubCountry, City: pubCity, EventData: marshalTypedEventData(pr),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		return appendErr
	}
	return batch.Send()
}

// processLoadBalancing handles load balancing events
func (h *AnalyticsHandler) processLoadBalancing(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing load balancing event: %s", event.EventID)

	// Parse MistTrigger envelope
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mistTriggerStreamID(&mt)); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_LoadBalancingData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for load_balancing")
	}
	loadBalancing := tp.LoadBalancingData
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(loadBalancing.GetInternalName())

	// Write to ClickHouse routing_events table - using ACTUAL fields from LoadBalancingPayload
	remoteClusterID := loadBalancing.GetRemoteClusterId()
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareRoutingDecision(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}
	defer batch.Close()

	var selID *string
	if loadBalancing.SelectedNodeId != nil && *loadBalancing.SelectedNodeId != "" {
		selID = loadBalancing.SelectedNodeId
	}
	var routeKm *float64
	if loadBalancing.RoutingDistanceKm != nil {
		routeKm = loadBalancing.RoutingDistanceKm
	}

	var clientBucketH3 *uint64
	var clientBucketRes *uint8
	if loadBalancing.ClientBucket != nil && loadBalancing.ClientBucket.H3Index != 0 {
		clientBucketH3 = &loadBalancing.ClientBucket.H3Index
		resolution := uint8(loadBalancing.ClientBucket.Resolution)
		clientBucketRes = &resolution
	}
	var nodeBucketH3 *uint64
	var nodeBucketRes *uint8
	if loadBalancing.NodeBucket != nil && loadBalancing.NodeBucket.H3Index != 0 {
		nodeBucketH3 = &loadBalancing.NodeBucket.H3Index
		resolution := uint8(loadBalancing.NodeBucket.Resolution)
		nodeBucketRes = &resolution
	}

	// Dual-tenant attribution (RFC: routing-events-dual-tenant-attribution)
	var streamTenantID *uuid.UUID
	if loadBalancing.StreamTenantId != nil && *loadBalancing.StreamTenantId != "" {
		if parsed, err := uuid.Parse(*loadBalancing.StreamTenantId); err == nil {
			streamTenantID = &parsed
		}
	}
	clusterID := loadBalancing.GetClusterId()
	var candidatesCount *int32
	if loadBalancing.CandidatesCount != nil && *loadBalancing.CandidatesCount > 0 {
		value := int32(*loadBalancing.CandidatesCount)
		candidatesCount = &value
	}
	eventType := loadBalancing.GetEventType()
	source := loadBalancing.GetSource()

	clientCountry := loadBalancing.GetClientCountry()
	if clientCountry == "" {
		clientCountry = "--"
	}

	latencyMS := loadBalancing.GetLatencyMs()
	if appendErr := batch.Append(periscopeingestdb.RoutingDecisionRow{
		Timestamp: event.Timestamp, TenantID: uuid.MustParse(event.TenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)),
		InternalName: internalName, SelectedNode: loadBalancing.GetSelectedNode(), Status: loadBalancing.GetStatus(),
		Details: loadBalancing.GetDetails(), Score: int64(loadBalancing.GetScore()), ClientIP: loadBalancing.GetClientIp(),
		ClientCountry: clientCountry, ClientLatitude: loadBalancing.GetLatitude(), ClientLongitude: loadBalancing.GetLongitude(),
		ClientBucketH3: clientBucketH3, ClientBucketRes: clientBucketRes,
		NodeLatitude: loadBalancing.GetNodeLatitude(), NodeLongitude: loadBalancing.GetNodeLongitude(), NodeName: loadBalancing.GetNodeName(),
		NodeBucketH3: nodeBucketH3, NodeBucketRes: nodeBucketRes, SelectedNodeID: selID, RoutingDistanceKM: routeKm,
		StreamTenantID: streamTenantID, ClusterID: clusterID, RemoteClusterID: remoteClusterID, LatencyMS: &latencyMS,
		CandidatesCount: candidatesCount, EventType: optionalString(eventType), Source: optionalString(source),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", err)
		return err
	}

	// Load balancing events go ONLY to routing_events table
	// They represent routing requests, NOT viewers
	// Viewer counts come from USER_NEW/USER_END (connection_events)

	h.logger.WithFields(logging.Fields{
		"event_id":    event.EventID,
		"stream_name": loadBalancing.GetInternalName(),
	}).Info("Processed load balancing event")

	return nil
}

// processClientLifecycleBatch handles per-client QoE samples coalesced by Foghorn
// into a single ClientLifecycleBatch trigger. Each batch becomes one ClickHouse
// INSERT into client_qoe_samples — one insert per (tenant, stream, node) per flush
// window, coalescing the per-viewer, per-Helmsman-poll samples rather than inserting
// each individually.
//
// Viewer-count and billing semantics live on the USER_NEW / USER_END trigger
// path (viewer_connection_events); these QoE samples are diagnostic-only.
func (h *AnalyticsHandler) processClientLifecycleBatch(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing client lifecycle batch event: %s", event.EventID)

	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_ClientLifecycleBatch)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for client_lifecycle_batch")
	}
	batchPayload := tp.ClientLifecycleBatch
	samples := batchPayload.GetSamples()
	if len(samples) == 0 {
		return nil
	}

	batchStreamID := parseUUID(batchPayload.GetStreamId())
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareClientQOESample(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}
	defer batch.Close()

	for _, sample := range samples {
		// Normalize internal name (strip live+/vod+ prefix).
		internalName := mist.ExtractInternalName(sample.GetInternalName())

		// Per-sample connection quality only when MistServer reported packets sent.
		var connectionQuality *float32
		if sample.GetPacketsSent() > 0 {
			quality := float32(1.0 - (float64(sample.GetPacketsLost()) / float64(sample.GetPacketsSent())))
			connectionQuality = &quality
		}

		// Prefer the per-sample stream_id (Foghorn enrichment) but fall back to
		// the batch-level one for samples that lost it in transit.
		sampleStreamID := batchStreamID
		if sid := sample.GetStreamId(); sid != "" {
			sampleStreamID = parseUUID(sid)
		}

		position := sample.GetPosition()
		if appendErr := batch.Append(periscopeingestdb.ClientQOESampleRow{
			Timestamp: event.Timestamp, EventID: optionalUUID(sample.GetEventId()), TenantID: uuid.MustParse(event.TenantID),
			StreamID: sampleStreamID, InternalName: internalName, SessionID: sample.GetSessionId(), NodeID: sample.GetNodeId(),
			Protocol: sample.GetProtocol(), Host: sample.GetHost(), ConnectionTime: sample.GetConnectionTime(), Position: &position,
			BandwidthIn: uint64(sample.GetBandwidthInBps()), BandwidthOut: uint64(sample.GetBandwidthOutBps()),
			BytesDownloaded: uint64(sample.GetBytesDownloaded()), BytesUploaded: uint64(sample.GetBytesUploaded()),
			PacketsSent: uint64(sample.GetPacketsSent()), PacketsLost: uint64(sample.GetPacketsLost()),
			PacketsRetransmitted: uint64(sample.GetPacketsRetransmitted()), ConnectionQuality: connectionQuality,
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
		}); appendErr != nil {
			h.logger.Errorf("Failed to append client QoE sample to ClickHouse batch: %v", appendErr)
			return appendErr
		}
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send ClickHouse client QoE batch: %v", err)
		return err
	}
	return nil
}

// processPlaybackBootTrace handles a browser-originated player boot waterfall.
// One trace = one ClickHouse row in player_boot_samples. Attribution is already
// server-derived (Bridge stamped tenant_id/stream_id/cluster fields and minted
// the canonical event_id); this handler just persists. Diagnostic/lossy — never
// a viewer-count or billing source. Percentiles are computed at read time, so
// there is no rollup MV here.
func (h *AnalyticsHandler) processPlaybackBootTrace(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing player boot trace event: %s", event.EventID)

	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_PlaybackBootTrace)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for playback_boot")
	}
	t := tp.PlaybackBootTrace

	var clusterAttributed uint8
	if t.GetClusterAttributed() {
		clusterAttributed = 1
	}
	var isLive uint8
	if t.GetIsLive() {
		isLive = 1
	}

	// Manifest/first-segment headline fields + full resource array.
	var manifestURL, firstSegmentURL string
	var manifestMs, firstSegmentMs uint32
	var manifestSize, firstSegmentSize uint64
	var cacheStatus string
	var ageSeconds *uint32
	for _, r := range t.GetResources() {
		switch r.GetKind() {
		case "manifest":
			if manifestURL == "" {
				manifestURL = r.GetUrl()
				manifestMs = r.GetDurationMs()
				manifestSize = r.GetTransferSize()
			}
		case "first_segment":
			if firstSegmentURL == "" {
				firstSegmentURL = r.GetUrl()
				firstSegmentMs = r.GetDurationMs()
				firstSegmentSize = r.GetTransferSize()
			}
		case "mist_json":
			if cacheStatus == "" {
				cacheStatus = r.GetCacheStatus()
			}
			if ageSeconds == nil && r.AgeSeconds != nil {
				v := r.GetAgeSeconds()
				ageSeconds = &v
			}
		}
	}

	resourcesJSON := "[]"
	if res := t.GetResources(); len(res) > 0 {
		if encoded, err := json.Marshal(res); err == nil {
			resourcesJSON = string(encoded)
		}
	}

	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PreparePlayerBootSample(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare player_boot_samples batch: %v", err)
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}
	defer batch.Close()

	if appendErr := batch.Append(periscopeingestdb.PlayerBootSampleRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: uuid.MustParse(event.TenantID), StreamID: optionalUUID(t.GetStreamId()),
		ArtifactHash: t.GetArtifactHash(), InternalName: mist.ExtractInternalName(t.GetInternalName()), SessionID: t.GetSessionId(), TraceID: t.GetTraceId(),
		NodeID: t.GetNodeId(), ServingClusterID: t.GetServingClusterId(), OriginClusterID: t.GetOriginClusterId(), ClusterAttributed: clusterAttributed,
		TotalTTFMS: t.GetTotalTtfMs(), GatewayResolveMS: t.GetGatewayResolveMs(), MistHydrateMS: t.GetMistHydrateMs(),
		PlayerSelectMS: t.GetPlayerSelectMs(), ConnectMS: t.GetConnectMs(), PrebufferMS: t.GetPrebufferMs(),
		Outcome: t.GetOutcome(), ErrorCode: t.GetErrorCode(), PlayerType: t.GetPlayerType(), Protocol: t.GetProtocol(),
		ContentType: t.GetContentType(), IsLive: isLive, ConnectionType: t.GetConnectionType(), PlayerVersion: t.GetPlayerVersion(),
		ManifestURL: manifestURL, ManifestMS: manifestMs, ManifestTransferSize: manifestSize,
		FirstSegmentURL: firstSegmentURL, FirstSegmentMS: firstSegmentMs, FirstSegmentTransferSize: firstSegmentSize,
		CDNCacheStatus: cacheStatus, AgeSeconds: ageSeconds, Resources: resourcesJSON,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append player boot trace to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send player_boot_samples batch: %v", err)
		return err
	}
	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
	}
	return nil
}

// processPlaybackSessionQoe handles a browser-originated viewer-experienced QoE
// delta (one beacon = one row in client_qoe_session_deltas). Counters are additive
// deltas for the window since the previous beacon, so summing a session's beacons
// reconstructs totals even when the final beacon is lost. Attribution is already
// server-derived by Bridge. Diagnostic/lossy — never a viewer-count or billing
// source.
//
// Dedupe is two-sided: event_id gives Kafka-replay idempotency at the envelope,
// while the table's ReplacingMergeTree collapses a double-fired client beacon on
// the client-stable (tenant_id, content_id, session_id, beacon_seq) key. Ratios
// are computed at read time over the deduped rows — no rollup MV here.
func (h *AnalyticsHandler) processPlaybackSessionQoe(ctx context.Context, event kafka.AnalyticsEvent) error {
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_PlaybackSessionQoe)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for playback_session_qoe")
	}
	t := tp.PlaybackSessionQoe

	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareClientQOESessionDelta(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare client_qoe_session_deltas batch: %v", err)
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "error").Inc()
		}
		return err
	}
	defer batch.Close()

	if appendErr := batch.Append(periscopeingestdb.ClientQOESessionDeltaRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: uuid.MustParse(event.TenantID), StreamID: optionalUUID(t.GetStreamId()),
		ArtifactHash: t.GetArtifactHash(), InternalName: mist.ExtractInternalName(t.GetInternalName()), ContentID: t.GetContentId(), SessionID: t.GetSessionId(),
		BeaconSeq: t.GetBeaconSeq(), IsFinal: boolToUint8(t.GetIsFinal()), FlushReason: t.GetFlushReason(), NodeID: t.GetNodeId(),
		ServingClusterID: t.GetServingClusterId(), OriginClusterID: t.GetOriginClusterId(), ClusterAttributed: boolToUint8(t.GetClusterAttributed()),
		PlayerType: t.GetPlayerType(), Protocol: t.GetProtocol(), ContentType: t.GetContentType(), IsLive: boolToUint8(t.GetIsLive()),
		ConnectionType: t.GetConnectionType(), PlayerVersion: t.GetPlayerVersion(), PlayedMS: t.GetPlayedMs(), RebufferMS: t.GetRebufferMs(),
		RebufferCount: t.GetRebufferCount(), SeekWaitMS: t.GetSeekWaitMs(), FrameStatsSupported: boolToUint8(t.GetFrameStatsSupported()),
		FramesDecoded: t.GetFramesDecoded(), FramesDropped: t.GetFramesDropped(), FramesCorrupted: t.GetFramesCorrupted(),
		FirstFrame: boolToUint8(t.GetFirstFrame()), FatalError: boolToUint8(t.GetFatalError()), ErrorCode: t.GetErrorCode(),
		BitrateBPSSeconds: t.GetBitrateBpsSeconds(), ABRUpswitchCount: t.GetAbrUpswitchCount(), ABRDownswitchCount: t.GetAbrDownswitchCount(),
		PlayIntent: boolToUint8(t.GetPlayIntent()), LiveEdgeLatencyMS: t.GetLiveEdgeLatencyMs(), BucketWidthS: t.GetBucketWidthS(),
		AssetDurationS: t.GetAssetDurationS(), MaxBucketReached: t.GetMaxBucketReached(), SourceRegion: env.sourceRegion,
		StreamOriginRegion: env.streamOriginRegion, StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append session QoE delta to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send client_qoe_session_deltas batch: %v", err)
		return err
	}

	// VOD retention histogram (VOD beacons only): fan the sparse per-bucket
	// watched-seconds deltas out into one vod_retention_buckets row per bucket.
	if err := h.fanOutVodRetention(ctx, event, t); err != nil {
		return err
	}

	if h.metrics != nil {
		h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "processed").Inc()
	}
	return nil
}

// fanOutVodRetention writes one vod_retention_buckets row per histogram bucket
// carried on a session QoE beacon. The parallel arrays are length-validated at
// Bridge; a defensive guard here drops a mismatched pair rather than mis-pairing.
func (h *AnalyticsHandler) fanOutVodRetention(ctx context.Context, event kafka.AnalyticsEvent, t *ipcpb.PlaybackSessionQoe) error {
	buckets := t.GetRetentionBuckets()
	seconds := t.GetRetentionSecondsWatched()
	if len(buckets) == 0 || len(buckets) != len(seconds) {
		return nil
	}

	batch, err := periscopeingestdb.PrepareVODRetentionBucket(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare vod_retention_buckets batch: %v", err)
		return err
	}
	defer batch.Close()

	eventID := parseUUID(event.EventID)
	internalName := mist.ExtractInternalName(t.GetInternalName())
	env := analyticsEnvelopeColumns(event)
	for i, bucket := range buckets {
		if appendErr := batch.Append(periscopeingestdb.VODRetentionBucketRow{
			Timestamp: event.Timestamp, EventID: eventID, TenantID: uuid.MustParse(event.TenantID),
			ArtifactHash: t.GetArtifactHash(), InternalName: internalName, ContentID: t.GetContentId(), SessionID: t.GetSessionId(),
			BeaconSeq: t.GetBeaconSeq(), BucketWidthS: t.GetBucketWidthS(), AssetDurationS: t.GetAssetDurationS(),
			BucketIndex: bucket, SecondsWatched: seconds[i], SourceRegion: env.sourceRegion, SchemaVersion: env.schemaVersion,
		}); appendErr != nil {
			h.logger.Errorf("Failed to append vod_retention_buckets row: %v", appendErr)
			return appendErr
		}
	}
	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send vod_retention_buckets batch: %v", err)
		return err
	}
	return nil
}

// processNodeLifecycle handles node health and resource metrics
// Dual-writes to: live_nodes (current state) + node_metrics (historical log)
func (h *AnalyticsHandler) processNodeLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing node lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> NodeLifecycleUpdate
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_NodeLifecycleUpdate)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for node_lifecycle_update")
	}
	nodeLifecycle := tp.NodeLifecycleUpdate

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "attempt").Inc()
	}
	env := analyticsEnvelopeColumns(event)

	// 1. Write to live_nodes (current state - ReplacingMergeTree)
	stateBatch, err := periscopeingestdb.PrepareNodeState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_nodes batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return err
	}
	defer stateBatch.Close()

	cpuPercent := float32(nodeLifecycle.GetCpuTenths()) / 10.0
	modeStr := strings.ToLower(strings.TrimPrefix(nodeLifecycle.GetOperationalMode().String(), "NODE_OPERATIONAL_MODE_"))
	if modeStr == "unspecified" {
		modeStr = "normal"
	}

	// Build operational metadata only - skip bulk data (streams, artifacts, storage)
	metadata := map[string]interface{}{}
	if caps := nodeLifecycle.GetCapabilities(); caps != nil {
		metadata["capabilities"] = caps
	}
	if limits := nodeLifecycle.GetLimits(); limits != nil {
		metadata["limits"] = limits
	}
	if bwLimit := nodeLifecycle.GetBwLimit(); bwLimit > 0 {
		metadata["bw_limit"] = bwLimit
	}
	if baseUrl := nodeLifecycle.GetBaseUrl(); baseUrl != "" {
		metadata["base_url"] = baseUrl
	}
	metadataJSON, _ := json.Marshal(metadata)

	clusterID := mt.GetClusterId()
	if appendErr := stateBatch.Append(periscopeingestdb.NodeStateRow{
		TenantID: uuid.MustParse(event.TenantID), ClusterID: clusterID, NodeID: nodeLifecycle.GetNodeId(), CPUPercent: cpuPercent,
		RAMUsedBytes: uint64(nodeLifecycle.GetRamCurrent()), RAMTotalBytes: uint64(nodeLifecycle.GetRamMax()),
		DiskUsedBytes: uint64(nodeLifecycle.GetDiskUsedBytes()), DiskTotalBytes: uint64(nodeLifecycle.GetDiskTotalBytes()),
		UpSpeed: uint64(nodeLifecycle.GetUpSpeed()), DownSpeed: uint64(nodeLifecycle.GetDownSpeed()), BWLimit: uint64(nodeLifecycle.GetBwLimit()),
		ActiveStreams: uint32(nodeLifecycle.GetActiveStreams()), IsHealthy: boolToUint8(nodeLifecycle.GetIsHealthy()),
		OperationalMode: modeStr, Latitude: nodeLifecycle.GetLatitude(), Longitude: nodeLifecycle.GetLongitude(),
		Location: nodeLifecycle.GetLocation(), Metadata: metadataJSON, UpdatedAt: event.Timestamp,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to live_nodes batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_nodes batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_nodes", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "attempt").Inc()
	}

	// 2. Write to node_metrics (historical log - MergeTree)
	metricsBatch, err := periscopeingestdb.PrepareNodeMetricsSample(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}
	defer metricsBatch.Close()

	if err := metricsBatch.Append(periscopeingestdb.NodeMetricsSampleRow{
		Timestamp: event.Timestamp, TenantID: uuid.MustParse(event.TenantID), ClusterID: clusterID, NodeID: nodeLifecycle.GetNodeId(),
		CPUUsage: cpuPercent, RAMMax: uint64(nodeLifecycle.GetRamMax()), RAMCurrent: uint64(nodeLifecycle.GetRamCurrent()),
		SHMTotalBytes: uint64(nodeLifecycle.GetShmTotalBytes()), SHMUsedBytes: uint64(nodeLifecycle.GetShmUsedBytes()),
		DiskTotalBytes: uint64(nodeLifecycle.GetDiskTotalBytes()), DiskUsedBytes: uint64(nodeLifecycle.GetDiskUsedBytes()),
		BandwidthIn: uint64(nodeLifecycle.GetBandwidthInTotal()), BandwidthOut: uint64(nodeLifecycle.GetBandwidthOutTotal()),
		UpSpeed: uint64(nodeLifecycle.GetUpSpeed()), DownSpeed: uint64(nodeLifecycle.GetDownSpeed()),
		ConnectionsCurrent: uint32(nodeLifecycle.GetConnectionsCurrent()), StreamCount: uint32(nodeLifecycle.GetActiveStreams()),
		IsHealthy: boolToUint8(nodeLifecycle.GetIsHealthy()), OperationalMode: modeStr,
		Latitude: nodeLifecycle.GetLatitude(), Longitude: nodeLifecycle.GetLongitude(), Metadata: metadataJSON,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		h.logger.Errorf("Failed to append to node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}

	if err := metricsBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send node_metrics batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("node_metrics", "success").Inc()
	}

	return nil
}

func boolToUint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func getUint64SliceFromMap(data map[string]interface{}, key string) []uint64 {
	if data == nil {
		return nil
	}
	value, ok := data[key]
	if !ok || value == nil {
		return nil
	}

	switch v := value.(type) {
	case []uint64:
		return v
	case []interface{}:
		out := make([]uint64, 0, len(v))
		for _, raw := range v {
			switch n := raw.(type) {
			case uint64:
				out = append(out, n)
			case uint32:
				out = append(out, uint64(n))
			case int64:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case int:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case float64:
				if n >= 0 {
					out = append(out, uint64(n))
				}
			case json.Number:
				if parsed, err := n.Int64(); err == nil && parsed >= 0 {
					out = append(out, uint64(parsed))
				}
			}
		}
		return out
	default:
		return nil
	}
}

// processStreamBuffer handles STREAM_BUFFER webhook events with rich health metrics
func (h *AnalyticsHandler) processStreamBuffer(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream buffer event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamBufferTrigger
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mistTriggerStreamID(&mt)); err != nil {
		return err
	}
	if h.isDuplicateEvent(ctx, "stream_event_log", parseUUID(event.EventID), event.EventType) {
		return nil
	}
	payload, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_StreamBuffer)
	if !ok || payload == nil {
		return fmt.Errorf("unexpected payload for stream_buffer")
	}
	streamBuffer := payload.StreamBuffer
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamBuffer.GetStreamName())

	// Extract primary video and audio tracks for dedicated columns
	primaryVideo, primaryAudio := extractPrimaryTracks(streamBuffer.GetTracks())
	env := analyticsEnvelopeColumns(event)

	// Extract primary video track fields
	var (
		bitrate                      *uint32
		fps                          *float32
		width, height                *uint16
		codec                        *string
		frameMsMax, frameMsMin       *float32
		keyframeMsMax, keyframeMsMin *float32
		framesMax, framesMin         *uint32
		gopSize                      *uint16
	)
	if primaryVideo != nil {
		if v := primaryVideo.GetBitrateKbps(); v > 0 {
			b := uint32(v)
			bitrate = &b
		}
		if v := primaryVideo.GetFps(); v > 0 {
			f := float32(v)
			fps = &f
		}
		if v := primaryVideo.GetWidth(); v > 0 {
			w := uint16(v)
			width = &w
		}
		if v := primaryVideo.GetHeight(); v > 0 {
			ht := uint16(v)
			height = &ht
		}
		if v := primaryVideo.GetCodec(); v != "" {
			codec = &v
		}
		if v := primaryVideo.GetFrameMsMax(); v > 0 {
			f := float32(v)
			frameMsMax = &f
		}
		if v := primaryVideo.GetFrameMsMin(); v > 0 {
			f := float32(v)
			frameMsMin = &f
		}
		if v := primaryVideo.GetKeyframeMsMax(); v > 0 {
			f := float32(v)
			keyframeMsMax = &f
		}
		if v := primaryVideo.GetKeyframeMsMin(); v > 0 {
			f := float32(v)
			keyframeMsMin = &f
		}
		if v := primaryVideo.GetFramesMax(); v > 0 {
			f := uint32(v)
			framesMax = &f
			// Map frames_max (GOP length) to gop_size
			gs := uint16(v)
			gopSize = &gs
		}
		if v := primaryVideo.GetFramesMin(); v > 0 {
			f := uint32(v)
			framesMin = &f
		}
	}

	// Extract stream-wide buffer metrics
	// Note: 0 is a valid buffer value during DRY/rebuffering states.
	// IMPORTANT: only set buffer_size when the optional stream_buffer_ms field is present.
	// Otherwise we turn "unknown" into a hard 0 which corrupts analytics.
	var bufferSize *uint32
	bufferMsPtr := streamBuffer.StreamBufferMs
	if bufferMsPtr != nil {
		bs := uint32(*bufferMsPtr)
		bufferSize = &bs
	}

	// Calculate buffer_health as ratio of current buffer to max allowed distance from live
	// A healthy stream has buffer_health close to 1.0 (buffer full relative to maxkeepaway)
	var bufferHealth *float32
	if bufferSize != nil && streamBuffer.GetMaxKeepawayMs() > 0 {
		bh := float32(*bufferSize) / float32(streamBuffer.GetMaxKeepawayMs())
		if bh > 1.0 {
			bh = 1.0 // Clamp to max 1.0
		}
		bufferHealth = &bh
	}

	// Extract primary audio track fields
	var (
		audioChannels   *uint8
		audioSampleRate *uint32
		audioCodec      *string
		audioBitrate    *uint32
	)
	if primaryAudio != nil {
		if v := primaryAudio.GetChannels(); v > 0 {
			c := uint8(v)
			audioChannels = &c
		}
		if v := primaryAudio.GetSampleRate(); v > 0 {
			sr := uint32(v)
			audioSampleRate = &sr
		}
		if v := primaryAudio.GetCodec(); v != "" {
			audioCodec = &v
		}
		if v := primaryAudio.GetBitrateKbps(); v > 0 {
			b := uint32(v)
			audioBitrate = &b
		}
	}

	// Write to ClickHouse stream_events table
	streamEventsBatch, err := periscopeingestdb.PrepareStreamBufferEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_events batch: %v", err)
		return err
	}
	defer streamEventsBatch.Close()

	status := "live"
	bufferState := streamBuffer.GetBufferState()
	if appendErr := streamEventsBatch.Append(periscopeingestdb.StreamBufferEventRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: parseUUID(event.TenantID),
		StreamID: parseUUID(mistTriggerStreamID(&mt)), InternalName: internalName, NodeID: mt.GetNodeId(), ClusterID: mt.GetClusterId(),
		EventType: "stream_buffer", Status: &status, BufferState: &bufferState,
		HasIssues: optionalBoolUInt8(streamBuffer.GetHasIssues()), IssuesDescription: optionalString(streamBuffer.GetIssuesDescription()),
		TrackCount: optionalUint16(streamBuffer.GetTrackCount()), QualityTier: optionalString(streamBuffer.GetQualityTier()),
		PrimaryWidth: width, PrimaryHeight: height, PrimaryFPS: fps, EventData: marshalTypedEventData(&streamBuffer),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_events batch: %v", appendErr)
		return appendErr
	}

	if sendErr := streamEventsBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_events batch: %v", sendErr)
		return sendErr
	}

	// Serialize tracks to JSON for track_metadata column (ClickHouse JSON requires object, not array)
	trackMetadataJSON := "{}"
	if tracks := streamBuffer.GetTracks(); len(tracks) > 0 {
		if jsonBytes, marshalErr := json.Marshal(map[string]interface{}{"tracks": tracks}); marshalErr == nil {
			trackMetadataJSON = string(jsonBytes)
		}
	}

	// ALSO write to stream_health_metrics table for detailed health tracking and rebuffering_events MV
	healthBatch, err := periscopeingestdb.PrepareStreamBufferHealth(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_health_metrics batch: %v", err)
		return err
	}
	defer healthBatch.Close()

	if appendErr := healthBatch.Append(periscopeingestdb.StreamBufferHealthRow{
		Timestamp: event.Timestamp, TenantID: parseUUID(event.TenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)),
		InternalName: internalName, NodeID: mt.GetNodeId(), BufferState: streamBuffer.GetBufferState(),
		HasIssues: optionalBoolUInt8(streamBuffer.GetHasIssues()), IssuesDescription: optionalString(streamBuffer.GetIssuesDescription()),
		TrackCount: optionalUint16(streamBuffer.GetTrackCount()), TrackMetadata: trackMetadataJSON,
		Bitrate: bitrate, FPS: fps, Width: width, Height: height, Codec: codec, QualityTier: optionalString(streamBuffer.GetQualityTier()),
		FrameMSMax: frameMsMax, FrameMSMin: frameMsMin, KeyframeMSMax: keyframeMsMax, KeyframeMSMin: keyframeMsMin,
		FrameJitterMS: optionalFloat32(float32(streamBuffer.GetStreamJitterMs())), FramesMax: framesMax, FramesMin: framesMin,
		GOPSize: gopSize, BufferSize: bufferSize, BufferHealth: bufferHealth,
		AudioChannels: audioChannels, AudioSampleRate: audioSampleRate, AudioCodec: audioCodec, AudioBitrate: audioBitrate,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to stream_health_metrics batch: %v", appendErr)
		return appendErr
	}

	if sendErr := healthBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_health_metrics batch: %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed stream buffer event for stream: %s (written to both stream_events and stream_health_metrics)", streamBuffer.GetStreamName())
	return nil
}

// extractPrimaryTracks finds the first video and audio tracks from a list of StreamTracks
func extractPrimaryTracks(tracks []*ipcpb.StreamTrack) (video, audio *ipcpb.StreamTrack) {
	for _, t := range tracks {
		if t.GetTrackType() == "video" && video == nil {
			video = t
		}
		if t.GetTrackType() == "audio" && audio == nil {
			audio = t
		}
		if video != nil && audio != nil {
			break
		}
	}
	return
}

// processStreamEnd handles STREAM_END webhook events
func (h *AnalyticsHandler) processStreamEnd(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing stream end event: %s", event.EventID)

	// Parse MistTrigger envelope and extract StreamEndTrigger
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_StreamEnd)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for stream_end")
	}
	streamEnd := tp.StreamEnd
	streamID := mistTriggerStreamID(&mt)
	if err := h.requireStreamID(ctx, event, streamID); err != nil {
		return err
	}
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(streamEnd.GetStreamName())
	env := analyticsEnvelopeColumns(event)
	nodeID := mt.GetNodeId()
	if nodeID == "" {
		nodeID = streamEnd.GetNodeId()
	}

	stateBatch, err := periscopeingestdb.PrepareStreamLifecycleState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream_state_current offline batch: %v", err)
		return err
	}
	defer stateBatch.Close()

	streamUUID := parseUUID(streamID)
	var startedAt *time.Time
	if existingStartedAt, ok := h.lookupCurrentStreamStartedAt(ctx, event.TenantID, streamUUID); ok {
		startedAt = &existingStartedAt
	}

	if appendErr := stateBatch.Append(periscopeingestdb.StreamLifecycleStateRow{
		TenantID: parseUUID(event.TenantID), StreamID: streamUUID, InternalName: internalName, NodeID: nodeID,
		Status: "offline", BufferState: "EMPTY", UploadedBytes: nonNegativeUint64(streamEnd.GetUploadedBytes()),
		DownloadedBytes: nonNegativeUint64(streamEnd.GetDownloadedBytes()), ViewerSeconds: nonNegativeUint64(streamEnd.GetViewerSeconds()),
		StartedAt: startedAt, UpdatedAt: event.Timestamp,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append stream_state_current offline row: %v", appendErr)
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream_state_current offline batch: %v", sendErr)
		return sendErr
	}

	if h.isDuplicateEvent(ctx, "stream_event_log", parseUUID(event.EventID), event.EventType) {
		return nil
	}

	// Write to ClickHouse stream_events table using ONLY end-specific fields
	batch, err := periscopeingestdb.PrepareStreamEndEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}
	defer batch.Close()

	var downloaded, uploaded, viewerSeconds *uint64
	var totalViewers *uint32
	var totalInputs, totalOutputs *uint16
	if streamEnd.DownloadedBytes != nil {
		value := nonNegativeUint64(streamEnd.GetDownloadedBytes())
		downloaded = &value
	}
	if streamEnd.UploadedBytes != nil {
		value := nonNegativeUint64(streamEnd.GetUploadedBytes())
		uploaded = &value
	}
	if streamEnd.TotalViewers != nil {
		value := uint32(streamEnd.GetTotalViewers())
		totalViewers = &value
	}
	if streamEnd.TotalInputs != nil {
		value := uint16(streamEnd.GetTotalInputs())
		totalInputs = &value
	}
	if streamEnd.TotalOutputs != nil {
		value := uint16(streamEnd.GetTotalOutputs())
		totalOutputs = &value
	}
	if streamEnd.ViewerSeconds != nil {
		value := nonNegativeUint64(streamEnd.GetViewerSeconds())
		viewerSeconds = &value
	}

	if appendErr := batch.Append(periscopeingestdb.StreamEndEventRow{
		Timestamp: event.Timestamp, EventID: parseUUID(event.EventID), TenantID: parseUUID(event.TenantID), StreamID: parseUUID(streamID),
		InternalName: internalName, NodeID: mt.GetNodeId(), ClusterID: mt.GetClusterId(), EventType: "stream_end",
		DownloadedBytes: downloaded, UploadedBytes: uploaded, TotalViewers: totalViewers, TotalInputs: totalInputs,
		TotalOutputs: totalOutputs, ViewerSeconds: viewerSeconds, EventData: marshalTypedEventData(&streamEnd),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to ClickHouse batch: %v", appendErr)
		return appendErr
	}

	if sendErr := batch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send ClickHouse batch: %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed stream end event for stream: %s", streamEnd.GetStreamName())
	return nil
}

// processTrackList handles track list events with quality metrics
func (h *AnalyticsHandler) processTrackList(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing track list event: %s", event.EventID)

	// Parse LiveTrackListTrigger from protobuf
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mistTriggerStreamID(&mt)); err != nil {
		return err
	}
	eventID := parseUUID(event.EventID)
	if h.isDuplicateEvent(ctx, "track_list_events", eventID, event.EventType) ||
		h.isDuplicateEvent(ctx, "stream_event_log", eventID, event.EventType) {
		return nil
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_TrackList)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for track_list")
	}
	trackList := tp.TrackList
	// Normalize internal name by stripping live+/vod+ prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(trackList.GetStreamName())
	env := analyticsEnvelopeColumns(event)

	// Write to track_list_events with enhanced quality metrics using typed data
	batch, err := periscopeingestdb.PrepareTrackListEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare track list batch: %v", err)
		return err
	}
	defer batch.Close()

	if appendErr := batch.Append(periscopeingestdb.TrackListEventRow{
		Timestamp: event.Timestamp, EventID: eventID, TenantID: parseUUID(event.TenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)),
		InternalName: internalName, NodeID: mt.GetNodeId(), TrackList: marshalTypedEventData(trackList.GetTracks()),
		TrackCount: uint16(trackList.GetTotalTracks()), VideoTrackCount: uint16(trackList.GetVideoTrackCount()),
		AudioTrackCount: uint16(trackList.GetAudioTrackCount()), PrimaryWidth: optionalUint16(trackList.GetPrimaryWidth()),
		PrimaryHeight: optionalUint16(trackList.GetPrimaryHeight()), PrimaryFPS: optionalFloat32(float32(trackList.GetPrimaryFps())),
		PrimaryVideoCodec: optionalString(trackList.GetPrimaryVideoCodec()), PrimaryVideoBitrate: optionalUint32(uint32(trackList.GetPrimaryVideoBitrate())),
		QualityTier: optionalString(trackList.GetQualityTier()), PrimaryAudioChannels: optionalUint8(trackList.GetPrimaryAudioChannels()),
		PrimaryAudioSampleRate: optionalUint32(uint32(trackList.GetPrimaryAudioSampleRate())),
		PrimaryAudioCodec:      optionalString(trackList.GetPrimaryAudioCodec()), PrimaryAudioBitrate: optionalUint32(uint32(trackList.GetPrimaryAudioBitrate())),
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append track list data: %v", appendErr)
		return appendErr
	}

	if sendErr := batch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send track list batch: %v", sendErr)
		return sendErr
	}

	// Also write a canonical stream event for lifecycle timelines
	eventBatch, err := periscopeingestdb.PrepareTrackListStreamEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare stream events batch (track list): %v", err)
		return err
	}
	defer eventBatch.Close()

	status := "live"
	if appendErr := eventBatch.Append(periscopeingestdb.TrackListStreamEventRow{
		Timestamp: event.Timestamp, EventID: eventID, TenantID: parseUUID(event.TenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)),
		InternalName: internalName, NodeID: mt.GetNodeId(), ClusterID: mt.GetClusterId(), EventType: "track_list_update", Status: &status,
		EventData: marshalTypedEventData(trackList), SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		h.logger.Errorf("Failed to append stream event (track list): %v", appendErr)
		return appendErr
	}

	if sendErr := eventBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send stream event (track list): %v", sendErr)
		return sendErr
	}

	h.logger.Debugf("Successfully processed track list for stream: %s", trackList.GetStreamName())
	return nil
}

// marshalTypedEventData marshals any typed event data structure to JSON string
func marshalTypedEventData(data interface{}) string {
	if data == nil {
		return "{}"
	}
	b, _ := json.Marshal(data)
	return string(b)
}

// lifecycleUpdatedAt picks the ReplacingMergeTree collapse key for artifact_state_current: the SOURCE
// transition time (Foghorn outbox created_at, ms) when the producer stamped it, else Decklog receipt
// time. The source value is stable across at-least-once redeliveries, so a replayed older transition
// keeps its original time. This is BEST-EFFORT (created_at is wall-clock, not a source-owned monotonic
// revision), not a rigorous total order — see periscope.sql artifact_state_current.
func lifecycleUpdatedAt(sourceMs int64, receipt time.Time) time.Time {
	if sourceMs > 0 {
		return time.UnixMilli(sourceMs)
	}
	return receipt
}

func (h *AnalyticsHandler) processClipLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing clip lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> ClipLifecycleData
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mistTriggerStreamID(&mt)); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_ClipLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for clip_lifecycle")
	}
	cl := tp.ClipLifecycleData

	// Required - normalize internal name by stripping any prefix for consistent analytics keys
	internalName := mist.ExtractInternalName(cl.GetStreamInternalName())
	tenantID := event.TenantID
	env := analyticsEnvelopeColumns(event)

	// Prefer clip_hash as the canonical artifact identifier; fall back to request_id if missing.
	requestID := cl.GetClipHash()
	if requestID == "" {
		requestID = cl.GetRequestId()
	}

	// Optional fields are represented with their exact ClickHouse nullable types.
	var expiresAtTime *time.Time
	// Clip time boundaries (enriched by Foghorn from original ClipPullRequest)
	if cl.GetExpiresAt() != 0 {
		value := time.Unix(cl.GetExpiresAt(), 0)
		expiresAtTime = &value
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := periscopeingestdb.PrepareClipArtifactState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}
	defer stateBatch.Close()

	// Map stage string for consistency - convert STAGE_DONE -> done, STAGE_FAILED -> failed, etc.
	stageStr := strings.ToLower(strings.TrimPrefix(cl.GetStage().String(), "STAGE_"))

	if appendErr := stateBatch.Append(periscopeingestdb.ClipArtifactStateRow{
		ArtifactStateBase: periscopeingestdb.ArtifactStateBase{
			TenantID: parseUUID(tenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)), RequestID: requestID, InternalName: internalName,
			ContentType: "clip", Stage: stageStr, ProgressPercent: uint8(cl.GetProgressPercent()), ErrorMessage: optionalString(cl.GetError()),
			RequestedAt: event.Timestamp, StartedAt: optionalUnixTime(cl.GetStartedAt()), CompletedAt: optionalUnixTime(cl.GetCompletedAt()),
			FilePath: optionalString(cl.GetFilePath()), S3URL: optionalString(cl.GetS3Url()), SizeBytes: optionalUint64(cl.GetSizeBytes()),
			ProcessingNodeID: optionalString(cl.GetNodeId()), UpdatedAt: lifecycleUpdatedAt(cl.GetSourceUpdatedAtMs(), event.Timestamp),
			ExpiresAt: expiresAtTime, StorageLocation: optionalString(cl.GetStorageLocation()), SyncStatus: optionalString(cl.GetSyncStatus()),
			HasLocalCopy: cl.HasLocalCopy, IsSynced: cl.IsSynced, IsFinalized: cl.IsFinalized,
		},
		ClipStartUnix: optionalInt64(cl.GetStartUnix()), ClipStopUnix: optionalInt64(cl.GetStopUnix()),
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// Processing speed telemetry (STAGE_DONE enrichment from Foghorn)
	var speed periscopeingestdb.ArtifactSpeedFields
	if cl.ProcessingWallMs != nil {
		value := uint64(cl.GetProcessingWallMs())
		speed.ProcessingWallMS = &value
	}
	if sp := cl.GetProcessingSpeed(); sp != nil && sp.GetTicks() > 0 {
		min, avg, max := float32(sp.GetSpeedMin()), float32(sp.GetSpeedAvg()), float32(sp.GetSpeedMax())
		hard, stale, lockout := sp.GetHardSlowTicks(), sp.GetStaleHoldTicks(), sp.GetLockoutTicks()
		speed.SpeedMinX, speed.SpeedAvgX, speed.SpeedMaxX = &min, &avg, &max
		speed.HardSlowTicks, speed.StaleHoldTicks, speed.LockoutTicks = &hard, &stale, &lockout
		if sp.DrainMs != nil {
			value := uint64(sp.GetDrainMs())
			speed.DrainMS = &value
		}
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := periscopeingestdb.PrepareClipArtifactEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}
	defer batch.Close()

	if err := batch.Append(periscopeingestdb.ClipArtifactEventRow{
		ArtifactEventBase: periscopeingestdb.ArtifactEventBase{
			Timestamp: lifecycleUpdatedAt(cl.GetSourceUpdatedAtMs(), event.Timestamp), TenantID: parseUUID(tenantID),
			StreamID: parseUUID(mistTriggerStreamID(&mt)), InternalName: internalName, ClusterID: mt.GetClusterId(),
			OriginClusterID: mt.GetOriginClusterId(), RequestID: requestID, Stage: stageStr, ContentType: "clip",
			IngestNodeID: optionalString(cl.GetNodeId()), Message: optionalString(cl.GetError()), FilePath: optionalString(cl.GetFilePath()),
			S3URL: optionalString(cl.GetS3Url()), SizeBytes: optionalUint64(cl.GetSizeBytes()), ExpiresAt: optionalInt64(cl.GetExpiresAt()),
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion, EventID: event.EventID,
		},
		StartUnix: optionalInt64(cl.GetStartUnix()), StopUnix: optionalInt64(cl.GetStopUnix()),
		Percent: optionalUint32(uint32(cl.GetProgressPercent())), ArtifactSpeedFields: speed,
	}); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	return nil
}

// processDVRLifecycle handles DVR lifecycle events
// Dual-writes to: live_artifacts (current state) + clip_events (historical log)
func (h *AnalyticsHandler) processDVRLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing DVR lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> DVRLifecycleData
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	if err := h.requireStreamID(ctx, event, mistTriggerStreamID(&mt)); err != nil {
		return err
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_DvrLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for dvr_lifecycle")
	}
	dvrData := tp.DvrLifecycleData

	tenantID := event.TenantID
	var internalName string
	if dvrData.TenantId != nil && *dvrData.TenantId != "" {
		tenantID = *dvrData.TenantId
	}
	if dvrData.StreamInternalName != nil {
		// Normalize internal name by stripping any prefix for consistent analytics keys
		internalName = mist.ExtractInternalName(*dvrData.StreamInternalName)
	}
	env := analyticsEnvelopeColumns(event)

	// Map status to stage (normalize proto enum to lowercase for ClickHouse)
	stageStr := normalizeDVRStage(dvrData.GetStatus())
	nodeID := dvrData.GetNodeId()
	if nodeID == "" {
		nodeID = mt.GetNodeId()
	}

	var expiresAtTime *time.Time
	if dvrData.GetExpiresAt() != 0 {
		value := time.Unix(dvrData.GetExpiresAt(), 0)
		expiresAtTime = &value
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	stateBatch, err := periscopeingestdb.PrepareDVRArtifactState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}
	defer stateBatch.Close()

	if appendErr := stateBatch.Append(periscopeingestdb.DVRArtifactStateRow{
		ArtifactStateBase: periscopeingestdb.ArtifactStateBase{
			TenantID: parseUUID(tenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)), RequestID: dvrData.GetDvrHash(),
			InternalName: internalName, ContentType: "dvr", Stage: stageStr, ErrorMessage: optionalString(dvrData.GetError()),
			RequestedAt: event.Timestamp, StartedAt: optionalUnixTime(dvrData.GetStartedAt()), CompletedAt: optionalUnixTime(dvrData.GetEndedAt()),
			FilePath: optionalString(dvrData.GetManifestPath()), SizeBytes: optionalUint64(dvrData.GetSizeBytes()),
			ProcessingNodeID: optionalString(nodeID), UpdatedAt: lifecycleUpdatedAt(dvrData.GetSourceUpdatedAtMs(), event.Timestamp),
			ExpiresAt: expiresAtTime, StorageLocation: optionalString(dvrData.GetStorageLocation()), SyncStatus: optionalString(dvrData.GetSyncStatus()),
			HasLocalCopy: dvrData.HasLocalCopy, IsSynced: dvrData.IsSynced, IsFinalized: dvrData.IsFinalized,
		},
		SegmentCount: optionalUint32FromInt32(dvrData.GetSegmentCount()), ManifestPath: optionalString(dvrData.GetManifestPath()),
	}); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// 2. Write to clip_events (historical log - MergeTree)
	batch, err := periscopeingestdb.PrepareDVRArtifactEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}
	defer batch.Close()

	nodeIDValue := nodeID
	manifestPath := dvrData.GetManifestPath()
	if err := batch.Append(periscopeingestdb.DVRArtifactEventRow{
		ArtifactEventBase: periscopeingestdb.ArtifactEventBase{
			Timestamp: lifecycleUpdatedAt(dvrData.GetSourceUpdatedAtMs(), event.Timestamp), TenantID: parseUUID(tenantID),
			StreamID: parseUUID(mistTriggerStreamID(&mt)), InternalName: internalName, ClusterID: mt.GetClusterId(),
			OriginClusterID: mt.GetOriginClusterId(), RequestID: dvrData.GetDvrHash(), Stage: stageStr, ContentType: "dvr",
			IngestNodeID: &nodeIDValue, FilePath: &manifestPath, SizeBytes: optionalUint64(dvrData.GetSizeBytes()),
			Message: optionalString(dvrData.GetError()), ExpiresAt: optionalInt64(dvrData.GetExpiresAt()),
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion, EventID: event.EventID,
		},
		StartUnix: optionalInt64(dvrData.GetStartedAt()), StopUnix: optionalInt64(dvrData.GetEndedAt()),
	}); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	return nil
}

// processVodLifecycle handles VOD upload lifecycle events
// Writes to: live_artifacts (current state) + clip_events (historical log)
// VOD differs from clips/DVR in that uploads happen directly to S3 via presigned URLs,
// with Foghorn tracking the lifecycle and emitting events to Kafka.
func (h *AnalyticsHandler) processVodLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing VOD lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> VodLifecycleData
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_VodLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for vod_lifecycle")
	}
	vodData := tp.VodLifecycleData

	tenantID := event.TenantID
	if vodData.TenantId != nil && *vodData.TenantId != "" {
		tenantID = *vodData.TenantId
	}
	env := analyticsEnvelopeColumns(event)

	// Map status to stage string (normalize proto enum to lowercase for ClickHouse)
	stageStr := normalizeVodStage(vodData.GetStatus())

	var expiresAtTime *time.Time
	if vodData.GetExpiresAt() != 0 {
		value := time.Unix(vodData.GetExpiresAt(), 0)
		expiresAtTime = &value
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "attempt").Inc()
	}

	// 1. Write to live_artifacts (current state - ReplacingMergeTree)
	// VOD uses vod_hash as request_id, and content_type='vod'
	stateBatch, err := periscopeingestdb.PrepareVODArtifactState(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare live_artifacts batch for VOD: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return err
	}
	defer stateBatch.Close()

	internalName := vodData.GetVodHash()
	var filename *string
	if vodData.Filename != nil && *vodData.Filename != "" {
		filename = vodData.Filename
	}

	if appendErr := stateBatch.Append(periscopeingestdb.VODArtifactStateRow{ArtifactStateBase: periscopeingestdb.ArtifactStateBase{
		TenantID: parseUUID(tenantID), StreamID: parseUUID(mistTriggerStreamID(&mt)), RequestID: vodData.GetVodHash(), InternalName: internalName,
		Filename: filename, ContentType: "vod", Stage: stageStr, ProgressPercent: vodProgressPercent(vodData),
		ErrorMessage: optionalString(vodData.GetError()), RequestedAt: event.Timestamp,
		StartedAt: optionalUnixTime(vodData.GetStartedAt()), CompletedAt: optionalUnixTime(vodData.GetCompletedAt()),
		FilePath: optionalString(vodData.GetFilePath()), S3URL: optionalString(vodData.GetS3Url()), SizeBytes: optionalUint64(vodData.GetSizeBytes()),
		ProcessingNodeID: optionalString(vodData.GetNodeId()), UpdatedAt: lifecycleUpdatedAt(vodData.GetSourceUpdatedAtMs(), event.Timestamp),
		ExpiresAt: expiresAtTime, StorageLocation: optionalString(vodData.GetStorageLocation()), SyncStatus: optionalString(vodData.GetSyncStatus()),
		HasLocalCopy: vodData.HasLocalCopy, IsSynced: vodData.IsSynced, IsFinalized: vodData.IsFinalized,
	}}); appendErr != nil {
		h.logger.Errorf("Failed to append to live_artifacts batch for VOD: %v", appendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return appendErr
	}

	if sendErr := stateBatch.Send(); sendErr != nil {
		h.logger.Errorf("Failed to send live_artifacts batch for VOD: %v", sendErr)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "error").Inc()
		}
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("live_artifacts", "success").Inc()
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "attempt").Inc()
	}

	// Processing speed telemetry (STATUS_COMPLETED enrichment from Foghorn)
	var vodSpeed periscopeingestdb.ArtifactSpeedFields
	if vodData.ProcessingWallMs != nil {
		value := uint64(vodData.GetProcessingWallMs())
		vodSpeed.ProcessingWallMS = &value
	}
	if sp := vodData.GetProcessingSpeed(); sp != nil && sp.GetTicks() > 0 {
		min, avg, max := float32(sp.GetSpeedMin()), float32(sp.GetSpeedAvg()), float32(sp.GetSpeedMax())
		hard, stale, lockout := sp.GetHardSlowTicks(), sp.GetStaleHoldTicks(), sp.GetLockoutTicks()
		vodSpeed.SpeedMinX, vodSpeed.SpeedAvgX, vodSpeed.SpeedMaxX = &min, &avg, &max
		vodSpeed.HardSlowTicks, vodSpeed.StaleHoldTicks, vodSpeed.LockoutTicks = &hard, &stale, &lockout
		if sp.DrainMs != nil {
			value := uint64(sp.GetDrainMs())
			vodSpeed.DrainMS = &value
		}
	}

	// 2. Write to clip_events (historical log - MergeTree)
	// Reuse clip_events table for VOD lifecycle events (content_type differentiates)
	batch, err := periscopeingestdb.PrepareVODArtifactEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}
	defer batch.Close()

	if err := batch.Append(periscopeingestdb.VODArtifactEventRow{
		ArtifactEventBase: periscopeingestdb.ArtifactEventBase{
			Timestamp: lifecycleUpdatedAt(vodData.GetSourceUpdatedAtMs(), event.Timestamp), TenantID: parseUUID(tenantID),
			StreamID: parseUUID(mistTriggerStreamID(&mt)), InternalName: internalName, ClusterID: mt.GetClusterId(),
			OriginClusterID: mt.GetOriginClusterId(), Filename: filename, RequestID: vodData.GetVodHash(), Stage: stageStr, ContentType: "vod",
			IngestNodeID: optionalString(vodData.GetNodeId()), FilePath: optionalString(vodData.GetFilePath()), S3URL: optionalString(vodData.GetS3Url()),
			SizeBytes: optionalUint64(vodData.GetSizeBytes()), Message: optionalString(vodData.GetError()), ExpiresAt: optionalInt64(vodData.GetExpiresAt()),
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion, EventID: event.EventID,
		},
		ArtifactSpeedFields: vodSpeed,
	}); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("clip_events", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"vod_hash":  vodData.GetVodHash(),
		"tenant_id": tenantID,
		"stage":     stageStr,
	}).Info("VOD lifecycle event processed")

	return nil
}

// normalizeVodStage maps VodLifecycleData.Status enum to lowercase stage string
func normalizeVodStage(status ipcpb.VodLifecycleData_Status) string {
	switch status {
	case ipcpb.VodLifecycleData_STATUS_REQUESTED:
		return "requested"
	case ipcpb.VodLifecycleData_STATUS_UPLOADING:
		return "uploading"
	case ipcpb.VodLifecycleData_STATUS_PROCESSING:
		return "processing"
	case ipcpb.VodLifecycleData_STATUS_COMPLETED:
		return "completed"
	case ipcpb.VodLifecycleData_STATUS_FAILED:
		return "failed"
	case ipcpb.VodLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

func vodProgressPercent(vodData *ipcpb.VodLifecycleData) uint8 {
	if vodData == nil {
		return 0
	}
	progress := vodData.GetProgressPct()
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return uint8(progress)
}

// processStorageLifecycle handles storage lifecycle events
func (h *AnalyticsHandler) processStorageLifecycle(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing storage lifecycle event: %s", event.EventID)

	// Parse MistTrigger envelope -> StorageLifecycleData
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_StorageLifecycleData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for storage_lifecycle")
	}
	sld := tp.StorageLifecycleData
	streamID := mistTriggerStreamID(&mt)
	if !isValidUUIDString(streamID) {
		h.logger.WithFields(logging.Fields{
			"event_id":   event.EventID,
			"tenant_id":  event.TenantID,
			"stream_id":  streamID,
			"asset_hash": sld.GetAssetHash(),
		}).Warn("Storage lifecycle event missing or invalid stream_id")
	}

	// Normalize internal name
	internalName := ""
	if sld.InternalName != nil {
		internalName = mist.ExtractInternalName(sld.GetInternalName())
	}

	actionStr := strings.ToLower(strings.TrimPrefix(sld.GetAction().String(), "ACTION_"))
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareStorageEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare ClickHouse batch: %v", err)
		return err
	}
	defer batch.Close()

	if err := batch.Append(periscopeingestdb.StorageEventRow{
		Timestamp: event.Timestamp, TenantID: parseUUID(event.TenantID), StreamID: parseUUID(streamID), InternalName: internalName,
		AssetHash: sld.GetAssetHash(), Action: actionStr, AssetType: sld.GetAssetType(), SizeBytes: sld.GetSizeBytes(),
		S3URL: optionalString(sld.GetS3Url()), LocalPath: optionalString(sld.GetLocalPath()), NodeID: optionalString(mt.GetNodeId()),
		DurationMS: optionalInt64(sld.GetDurationMs()), WarmDurationMS: optionalInt64(sld.GetWarmDurationMs()), Error: optionalString(sld.GetError()),
		ClusterID: sld.GetClusterId(), OriginClusterID: sld.GetOriginClusterId(), SourceRegion: env.sourceRegion,
		StreamOriginRegion: env.streamOriginRegion, StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		h.logger.Errorf("Failed to append to storage_events batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send storage_events batch: %v", err)
		return err
	}

	// storage_lifecycle is a DIAGNOSTIC stream: the sidecar (Helmsman) emits it
	// before Foghorn validates the sync attempt, so it may reflect a stale,
	// timed-out, or ultimately-rejected attempt. It writes ONLY to storage_events.
	// The authoritative artifact_state_current storage fields (is_synced,
	// sync_status, storage_location, has_local_copy, is_finalized) come SOLELY from
	// Foghorn's guarded, transactionally-captured Clip/DVR/Vod lifecycle events,
	// which are emitted AFTER processSyncComplete/freeze validation.
	return nil
}

// processFederationEvent handles federation operation events (origin-pull, peer topology, query fan-out)
func (h *AnalyticsHandler) processFederationEvent(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing federation event: %s", event.EventID)

	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_FederationEventData)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for federation_event")
	}
	fed := tp.FederationEventData

	eventType := strings.ToLower(fed.GetEventType().String())
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareFederationEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare federation_events batch: %v", err)
		return err
	}
	defer batch.Close()

	if err := batch.Append(periscopeingestdb.FederationEventRow{
		Timestamp: event.Timestamp, TenantID: parseUUID(event.TenantID), EventType: eventType,
		LocalCluster: fed.GetLocalCluster(), RemoteCluster: fed.GetRemoteCluster(), StreamName: fed.GetStreamName(),
		StreamID: optionalUUID(fed.GetStreamId()), SourceNode: optionalString(fed.GetSourceNode()), DestNode: optionalString(fed.GetDestNode()),
		DTSCURL: optionalString(fed.GetDtscUrl()), LatencyMS: fed.LatencyMs, TimeToLiveMS: fed.TimeToLiveMs,
		FailureReason: optionalString(fed.GetFailureReason()), QueriedClusters: fed.QueriedClusters,
		RespondingClusters: fed.RespondingClusters, TotalCandidates: fed.TotalCandidates, BestRemoteScore: fed.BestRemoteScore,
		PeerCluster: optionalString(fed.GetPeerCluster()), Role: fed.GetRole(), Reason: optionalString(fed.GetReason()),
		BlockedCluster: optionalString(fed.GetBlockedCluster()), ExistingReplicationCluster: optionalString(fed.GetExistingReplicationCluster()),
		LocalLat: fed.LocalLat, LocalLon: fed.LocalLon, RemoteLat: fed.RemoteLat, RemoteLon: fed.RemoteLon,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		h.logger.Errorf("Failed to append to federation_events batch: %v", err)
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send federation_events batch: %v", err)
		return err
	}

	return nil
}

func nilIfZeroInt64(v int64) interface{} {
	if v == 0 {
		return nil
	}
	return v
}

func nilIfZeroInt32ToUint32(v int32) interface{} {
	if v == 0 {
		return nil
	}
	return uint32(v)
}

// normalizeDVRStage converts proto DVRLifecycleData_Status to lowercase stage string for ClickHouse
func normalizeDVRStage(status ipcpb.DVRLifecycleData_Status) string {
	switch status {
	case ipcpb.DVRLifecycleData_STATUS_STARTED:
		return "started"
	case ipcpb.DVRLifecycleData_STATUS_RECORDING:
		return "recording"
	case ipcpb.DVRLifecycleData_STATUS_STOPPED:
		return "stopped"
	case ipcpb.DVRLifecycleData_STATUS_FAILED:
		return "failed"
	case ipcpb.DVRLifecycleData_STATUS_DELETED:
		return "deleted"
	default:
		return "unknown"
	}
}

// processProcessBilling handles process billing events from Helmsman
// These track transcoding operations (Livepeer Gateway, MistProcAV) for billing
func (h *AnalyticsHandler) processProcessBilling(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Infof("Processing process billing event: %s", event.EventID)

	// Parse MistTrigger envelope -> ProcessBillingEvent
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_ProcessBilling)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for process_billing")
	}
	pbe := tp.ProcessBilling

	// Normalize stream name by stripping live+/vod+ prefix for consistent analytics keys
	streamName := mist.ExtractInternalName(pbe.GetStreamName())

	// Use tenant_id from event envelope (already enriched by Decklog)
	tenantID := event.TenantID

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "attempt").Inc()
	}

	// Write diagnostic processing telemetry. cluster_id flows from the
	// ProcessBillingEvent (set by Helmsman/Foghorn) so processing minutes
	// can be billed against the right cluster's pricing model. Falls back
	// to the MistTrigger envelope's cluster_id when the producer hasn't
	// stamped the event directly.
	clusterID := pbe.GetClusterId()
	if clusterID == "" {
		clusterID = mt.GetClusterId()
	}
	originClusterID := pbe.GetOriginClusterId()
	if originClusterID == "" {
		originClusterID = mt.GetOriginClusterId()
	}
	env := analyticsEnvelopeColumns(event)

	batch, err := periscopeingestdb.PrepareProcessingEvent(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}
	defer batch.Close()

	trackType := pbe.GetTrackType()
	if trackType == "" {
		trackType = "unknown"
	}

	if err := batch.Append(periscopeingestdb.ProcessingEventRow{
		Timestamp: event.Timestamp, TenantID: parseUUID(tenantID), NodeID: pbe.GetNodeId(), ClusterID: clusterID,
		OriginClusterID: originClusterID, StreamID: parseUUID(mistTriggerStreamID(&mt)), InternalName: streamName,
		ProcessType: pbe.GetProcessType(), TrackType: trackType, DurationMS: pbe.GetDurationMs(),
		InputCodec: optionalStringPointer(pbe.InputCodec), OutputCodec: optionalStringPointer(pbe.OutputCodec),
		SegmentNumber: optionalInt32Pointer(pbe.SegmentNumber), Width: optionalInt32Pointer(pbe.Width), Height: optionalInt32Pointer(pbe.Height),
		RenditionCount: optionalInt32Pointer(pbe.RenditionCount), BroadcasterURL: optionalStringPointer(pbe.BroadcasterUrl),
		UploadTimeUS: optionalInt64Pointer(pbe.UploadTimeUs), LivepeerSessionID: optionalStringPointer(pbe.LivepeerSessionId),
		SegmentStartMS: optionalInt64Pointer(pbe.SegmentStartMs), InputBytes: optionalInt64Pointer(pbe.InputBytes),
		OutputBytesTotal: optionalInt64Pointer(pbe.OutputBytesTotal), AttemptCount: optionalInt32Pointer(pbe.AttemptCount),
		TurnaroundMS: optionalInt64Pointer(pbe.TurnaroundMs), SpeedFactor: optionalFloat64Pointer(pbe.SpeedFactor),
		RenditionsJSON: optionalStringPointer(pbe.RenditionsJson), InputFrames: optionalInt64Pointer(pbe.InputFrames),
		OutputFrames: optionalInt64Pointer(pbe.OutputFrames), DecodeUSPerFrame: optionalInt64Pointer(pbe.DecodeUsPerFrame),
		TransformUSPerFrame: optionalInt64Pointer(pbe.TransformUsPerFrame), EncodeUSPerFrame: optionalInt64Pointer(pbe.EncodeUsPerFrame),
		IsFinal: optionalBoolUint8Pointer(pbe.IsFinal), InputFramesDelta: optionalInt64Pointer(pbe.InputFramesDelta),
		OutputFramesDelta: optionalInt64Pointer(pbe.OutputFramesDelta), InputBytesDelta: optionalInt64Pointer(pbe.InputBytesDelta),
		OutputBytesDelta: optionalInt64Pointer(pbe.OutputBytesDelta), InputWidth: optionalInt32Pointer(pbe.InputWidth),
		InputHeight: optionalInt32Pointer(pbe.InputHeight), OutputWidth: optionalInt32Pointer(pbe.OutputWidth),
		OutputHeight: optionalInt32Pointer(pbe.OutputHeight), InputFPKS: optionalInt32Pointer(pbe.InputFpks),
		OutputFPSMeasured: optionalFloat64Pointer(pbe.OutputFpsMeasured), SampleRate: optionalInt32Pointer(pbe.SampleRate),
		Channels: optionalInt32Pointer(pbe.Channels), SourceTimestampMS: optionalInt64Pointer(pbe.SourceTimestampMs),
		SinkTimestampMS: optionalInt64Pointer(pbe.SinkTimestampMs), SourceAdvancedMS: optionalInt64Pointer(pbe.SourceAdvancedMs),
		SinkAdvancedMS: optionalInt64Pointer(pbe.SinkAdvancedMs), RTFIn: optionalFloat64Pointer(pbe.RtfIn),
		RTFOut: optionalFloat64Pointer(pbe.RtfOut), PipelineLagMS: optionalInt64Pointer(pbe.PipelineLagMs),
		OutputBitrateBPS: optionalInt64Pointer(pbe.OutputBitrateBps), SourceRegion: env.sourceRegion,
		StreamOriginRegion: env.streamOriginRegion, StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		h.logger.Errorf("Failed to append to process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}

	if err := batch.Send(); err != nil {
		h.logger.Errorf("Failed to send process_billing batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("process_billing", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"stream":       streamName,
		"process_type": pbe.GetProcessType(),
		"track_type":   pbe.GetTrackType(),
		"duration_ms":  pbe.GetDurationMs(),
	}).Debug("Successfully processed process billing event")

	return nil
}

// processAPIRequestBatch handles aggregated API request batches from Gateway
// These track GraphQL API usage for analytics (RFC: x402 Agent Access)
func (h *AnalyticsHandler) processAPIRequestBatch(ctx context.Context, event kafka.AnalyticsEvent) error {
	h.logger.Debugf("Processing API request batch event: %s", event.EventID)

	// Parse MistTrigger envelope -> APIRequestBatch
	var mt ipcpb.MistTrigger
	if err := h.parseProtobufData(event, &mt); err != nil {
		return fmt.Errorf("failed to parse MistTrigger: %w", err)
	}
	tp, ok := mt.GetTriggerPayload().(*ipcpb.MistTrigger_ApiRequestBatch)
	if !ok || tp == nil {
		return fmt.Errorf("unexpected payload for api_request_batch")
	}
	batch := tp.ApiRequestBatch

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "attempt").Inc()
	}
	env := analyticsEnvelopeColumns(event)

	// Prepare batch insert to api_requests table
	// Each aggregate becomes one row with request_count > 1
	chBatch, err := periscopeingestdb.PrepareAPIRequest(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}
	defer chBatch.Close()

	batchTimestamp := time.Unix(batch.GetTimestamp(), 0)
	sourceNode := batch.GetSourceNode()
	appendErrors := 0
	rowCount := 0

	for _, agg := range batch.GetAggregates() {
		timestamp := batchTimestamp
		if aggTimestamp := agg.GetTimestamp(); aggTimestamp > 0 {
			timestamp = time.Unix(aggTimestamp, 0)
		}
		tenantID := parseUUID(agg.GetTenantId())
		if tenantID == uuid.Nil {
			// Skip invalid tenant IDs
			continue
		}

		// Use nil for empty operation names (Nullable column)
		operationName := optionalString(agg.GetOperationName())

		userHashes := agg.GetUserHashes()
		if userHashes == nil {
			userHashes = []uint64{}
		}
		tokenHashes := agg.GetTokenHashes()
		if tokenHashes == nil {
			tokenHashes = []uint64{}
		}

		if err := chBatch.Append(periscopeingestdb.APIRequestRow{
			Timestamp: timestamp, TenantID: tenantID, SourceNode: &sourceNode, AuthType: agg.GetAuthType(),
			OperationName: operationName, OperationType: agg.GetOperationType(), RequestCount: agg.GetRequestCount(),
			ErrorCount: agg.GetErrorCount(), TotalDurationMS: agg.GetTotalDurationMs(), TotalComplexity: agg.GetTotalComplexity(),
			LLMInputTokens: agg.GetLlmInputTokens(), LLMOutputTokens: agg.GetLlmOutputTokens(), LLMModel: agg.GetModel(),
			LLMProvider: agg.GetProvider(), UserHashes: userHashes, TokenHashes: tokenHashes,
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
		}); err != nil {
			h.logger.WithFields(logging.Fields{
				"tenant_id": agg.GetTenantId(),
				"error":     err,
			}).Warn("Failed to append aggregate to api_request_batch")
			appendErrors++
			continue
		}
		rowCount++
	}

	if rowCount == 0 {
		// If everything was filtered out (empty/invalid payload), treat as a no-op.
		// Returning an error would cause the Kafka consumer to retry forever and stall the partition.
		if appendErrors > 0 {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
			}
			return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
		}

		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "skip").Inc()
		}
		h.logger.WithFields(logging.Fields{
			"source_node": sourceNode,
		}).Debug("api_request_batch had no valid aggregates; skipping")
		return nil
	}

	if appendErrors > 0 {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
	}

	if err := chBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"source_node":     sourceNode,
		"aggregate_count": rowCount,
	}).Debug("Successfully processed API request batch")

	return nil
}

// validNodeCopyRole gates the role against what producers emit (ipc.ArtifactNodeCopyEvent,
// protojson lower-cased): "origin" or "cache". Anything else is rejected so malformed
// input can't corrupt current state.
func validNodeCopyRole(role string) bool {
	return role == "origin" || role == "cache"
}

// processArtifactNodeCopy records a per-(artifact, node) local-copy transition emitted
// by Foghorn (docs/architecture/analytics-pipeline.md): it appends to the immutable
// artifact_node_copy_events log and upserts the latest state into
// artifact_node_copy_current (ReplacingMergeTree keyed on the Foghorn-assigned monotonic
// version). tenant_id rides the ServiceEvent envelope; transition arrives as a proto
// enum name.
func (h *AnalyticsHandler) processArtifactNodeCopy(ctx context.Context, event kafka.ServiceEvent) error {
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("artifact_node_copy", "attempt").Inc()
	}

	tenantID := parseUUID(event.TenantID)
	if tenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in artifact_node_copy service event")
	}
	artifactHash := getStringFromMap(event.Data, "artifact_hash")
	nodeID := getStringFromMap(event.Data, "node_id")
	if artifactHash == "" || nodeID == "" {
		return fmt.Errorf("artifact_node_copy event missing artifact_hash or node_id")
	}

	timestamp := event.Timestamp
	if ms, ok := getInt64FromMap(event.Data, "timestamp_ms"); ok && ms > 0 {
		timestamp = time.UnixMilli(ms)
	}
	role := strings.ToLower(getStringFromMap(event.Data, "role"))
	transition := strings.ToLower(getStringFromMap(event.Data, "transition"))

	// Reject anything outside the supported contract rather than writing it. A
	// misspelled/empty/unspecified transition must NOT default to a present copy —
	// that would silently corrupt current state.
	switch transition {
	case "gained", "lost", "updated":
	default:
		return fmt.Errorf("artifact_node_copy event has unsupported transition %q", transition)
	}
	if !validNodeCopyRole(role) {
		return fmt.Errorf("artifact_node_copy event has unsupported role %q", role)
	}

	// version is the Foghorn-assigned monotonic revision; it drives ReplacingMergeTree
	// dedup so concurrent updates converge deterministically. Every emitted event carries
	// a real (>0) version, so a missing/zero one means the payload was mis-serialized
	// (e.g. protojson string not parsed) — reject rather than write version=0, which
	// would defeat the ordering guarantee.
	v, ok := getInt64FromMap(event.Data, "version")
	if !ok || v <= 0 {
		return fmt.Errorf("artifact_node_copy event missing/zero version")
	}
	version := uint64(v)

	isComplete := false
	if b, ok := event.Data["is_complete"].(bool); ok {
		isComplete = b
	}
	var sizeBytes *uint64
	if sb, ok := getInt64FromMap(event.Data, "size_bytes"); ok && sb > 0 {
		v := uint64(sb)
		sizeBytes = &v
	}
	// GAINED/UPDATED mean the node holds a local copy; only LOST clears it.
	present := transition != "lost"
	env := serviceEnvelopeColumns(event)

	// event_id (stable, from the durable outbox row) makes the log ReplacingMergeTree
	// idempotent under at-least-once Kafka delivery.
	logBatch, err := periscopeingestdb.PrepareArtifactNodeCopyEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("artifact_node_copy", "error").Inc()
		}
		return err
	}
	defer logBatch.Close()
	if appendErr := logBatch.Append(periscopeingestdb.ArtifactNodeCopyEventRow{
		EventID: event.EventID, Timestamp: timestamp, TenantID: tenantID, ArtifactHash: artifactHash,
		NodeID: nodeID, Role: role, Transition: transition, IsComplete: isComplete, SizeBytes: sizeBytes,
		Version: version, SourceRegion: env.sourceRegion, SchemaVersion: env.schemaVersion,
	}); appendErr != nil {
		return appendErr
	}
	if sendErr := logBatch.Send(); sendErr != nil {
		return sendErr
	}

	curBatch, err := periscopeingestdb.PrepareArtifactNodeCopyCurrent(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer curBatch.Close()
	if appendErr := curBatch.Append(periscopeingestdb.ArtifactNodeCopyCurrentRow{
		TenantID: tenantID, ArtifactHash: artifactHash, NodeID: nodeID, Role: role,
		Present: present, IsComplete: isComplete, SizeBytes: sizeBytes, Version: version, UpdatedAt: timestamp,
	}); appendErr != nil {
		return appendErr
	}
	if sendErr := curBatch.Send(); sendErr != nil {
		return sendErr
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("artifact_node_copy", "success").Inc()
	}
	return nil
}

// processServiceAPIRequestBatch handles API usage aggregates from ServiceEvent payloads.
func (h *AnalyticsHandler) processServiceAPIRequestBatch(ctx context.Context, event kafka.ServiceEvent) error {
	h.logger.Debugf("Processing service API request batch event: %s", event.EventID)

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "attempt").Inc()
	}

	timestamp := event.Timestamp
	if ts, ok := getInt64FromMap(event.Data, "timestamp"); ok {
		timestamp = time.Unix(ts, 0)
	}
	sourceNode := getStringFromMap(event.Data, "source_node")
	env := serviceEnvelopeColumns(event)

	aggregatesRaw, ok := event.Data["aggregates"]
	if !ok {
		return fmt.Errorf("missing aggregates in api_request_batch service event")
	}

	aggregatesSlice, ok := aggregatesRaw.([]interface{})
	if !ok {
		return fmt.Errorf("invalid aggregates type in api_request_batch service event")
	}

	chBatch, err := periscopeingestdb.PrepareAPIRequest(ctx, h.clickhouse)
	if err != nil {
		h.logger.Errorf("Failed to prepare api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}
	defer chBatch.Close()

	appendErrors := 0
	rowCount := 0
	for _, rawAgg := range aggregatesSlice {
		aggMap, ok := rawAgg.(map[string]interface{})
		if !ok {
			continue
		}
		aggTimestamp := timestamp
		if ts, ok := getInt64FromMap(aggMap, "timestamp"); ok {
			aggTimestamp = time.Unix(ts, 0)
		}

		tenantID := parseUUID(getStringFromMap(aggMap, "tenant_id"))
		if tenantID == uuid.Nil {
			continue
		}

		operationName := getStringFromMap(aggMap, "operation_name")
		operationNameValue := optionalString(operationName)

		userHashes := getUint64SliceFromMap(aggMap, "user_hashes")
		if userHashes == nil {
			userHashes = []uint64{}
		}
		tokenHashes := getUint64SliceFromMap(aggMap, "token_hashes")
		if tokenHashes == nil {
			tokenHashes = []uint64{}
		}

		if err := chBatch.Append(periscopeingestdb.APIRequestRow{
			Timestamp: aggTimestamp, TenantID: tenantID, SourceNode: &sourceNode,
			AuthType: getStringFromMap(aggMap, "auth_type"), OperationName: operationNameValue,
			OperationType: getStringFromMap(aggMap, "operation_type"), RequestCount: uint32(getUint64FromMap(aggMap, "request_count")),
			ErrorCount: uint32(getUint64FromMap(aggMap, "error_count")), TotalDurationMS: getUint64FromMap(aggMap, "total_duration_ms"),
			TotalComplexity: uint32(getUint64FromMap(aggMap, "total_complexity")), LLMInputTokens: getUint64FromMap(aggMap, "llm_input_tokens"),
			LLMOutputTokens: getUint64FromMap(aggMap, "llm_output_tokens"), LLMModel: getStringFromMap(aggMap, "model"),
			LLMProvider: getStringFromMap(aggMap, "provider"), UserHashes: userHashes, TokenHashes: tokenHashes,
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
		}); err != nil {
			h.logger.WithFields(logging.Fields{
				"tenant_id": getStringFromMap(aggMap, "tenant_id"),
				"error":     err,
			}).Warn("Failed to append aggregate to api_request_batch")
			appendErrors++
			continue
		}
		rowCount++
	}

	if rowCount == 0 {
		// If everything was filtered out (empty/invalid payload), treat as a no-op.
		// Returning an error would cause the Kafka consumer to retry forever and stall the partition.
		if appendErrors > 0 {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
			}
			return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
		}

		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "skip").Inc()
		}
		h.logger.WithFields(logging.Fields{
			"source_node": sourceNode,
		}).Debug("api_request_batch had no valid aggregates; skipping")
		return nil
	}

	if appendErrors > 0 {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return fmt.Errorf("api_request_batch append failures: %d", appendErrors)
	}

	if err := chBatch.Send(); err != nil {
		h.logger.Errorf("Failed to send api_request_batch batch: %v", err)
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_request_batch", "success").Inc()
	}

	h.logger.WithFields(logging.Fields{
		"source_node":     sourceNode,
		"aggregate_count": rowCount,
	}).Debug("Successfully processed service API request batch")

	if err := h.processServiceAPIRequestBatchAudit(ctx, event, aggregatesSlice, sourceNode, timestamp); err != nil {
		h.logger.WithError(err).Warn("Failed to write API request batch audit to ClickHouse")
	}

	return nil
}

type apiRequestBatchAuditSummary struct {
	SourceNode      string         `json:"source_node"`
	AggregateCount  int            `json:"aggregate_count"`
	RequestCount    uint64         `json:"request_count"`
	ErrorCount      uint64         `json:"error_count"`
	TotalDurationMS uint64         `json:"total_duration_ms"`
	TotalComplexity uint64         `json:"total_complexity"`
	UserHashCount   int            `json:"user_hash_count"`
	TokenHashCount  int            `json:"token_hash_count"`
	AuthTypes       map[string]int `json:"auth_types"`
	OperationTypes  map[string]int `json:"operation_types"`
}

func newAPIRequestBatchAuditSummary(sourceNode string) *apiRequestBatchAuditSummary {
	return &apiRequestBatchAuditSummary{
		SourceNode:     sourceNode,
		AuthTypes:      map[string]int{},
		OperationTypes: map[string]int{},
	}
}

func (s *apiRequestBatchAuditSummary) add(aggMap map[string]interface{}) {
	s.AggregateCount++
	s.RequestCount += getUint64FromMap(aggMap, "request_count")
	s.ErrorCount += getUint64FromMap(aggMap, "error_count")
	s.TotalDurationMS += getUint64FromMap(aggMap, "total_duration_ms")
	s.TotalComplexity += getUint64FromMap(aggMap, "total_complexity")
	s.UserHashCount += len(getUint64SliceFromMap(aggMap, "user_hashes"))
	s.TokenHashCount += len(getUint64SliceFromMap(aggMap, "token_hashes"))
	if authType := getStringFromMap(aggMap, "auth_type"); authType != "" {
		s.AuthTypes[authType]++
	}
	if operationType := getStringFromMap(aggMap, "operation_type"); operationType != "" {
		s.OperationTypes[operationType]++
	}
}

func (h *AnalyticsHandler) processServiceAPIRequestBatchAudit(ctx context.Context, event kafka.ServiceEvent, aggregates []interface{}, sourceNode string, batchTimestamp time.Time) error {
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "attempt").Inc()
	}

	summaries := map[uuid.UUID]*apiRequestBatchAuditSummary{}
	for _, rawAgg := range aggregates {
		aggMap, ok := rawAgg.(map[string]interface{})
		if !ok {
			continue
		}
		tenantID := parseUUID(getStringFromMap(aggMap, "tenant_id"))
		if tenantID == uuid.Nil {
			continue
		}
		summary := summaries[tenantID]
		if summary == nil {
			summary = newAPIRequestBatchAuditSummary(sourceNode)
			summaries[tenantID] = summary
		}
		summary.add(aggMap)
	}
	if len(summaries) == 0 {
		return nil
	}

	env := serviceEnvelopeColumns(event)
	chBatch, err := periscopeingestdb.PrepareAPIEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}
	defer chBatch.Close()

	for tenantID, summary := range summaries {
		detailsJSON, err := json.Marshal(summary)
		if err != nil {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
			}
			return fmt.Errorf("failed to marshal api_request_batch audit details: %w", err)
		}

		if err := chBatch.Append(periscopeingestdb.APIEventRow{
			EventID: parseUUID(event.EventID), TenantID: tenantID, EventType: event.EventType, Source: event.Source,
			UserID: optionalUUID(event.UserID), ResourceType: event.ResourceType, ResourceID: optionalString(event.ResourceID),
			Details: string(detailsJSON), Timestamp: batchTimestamp, ClusterID: event.SourceClusterID,
			SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
			StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
		}); err != nil {
			if h.metrics != nil {
				h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
			}
			return err
		}
	}

	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "success").Inc()
	}

	return nil
}

// processTenantCreated handles tenant_created events for acquisition attribution.
func (h *AnalyticsHandler) processTenantCreated(ctx context.Context, event kafka.ServiceEvent) error {
	if !isValidUUIDString(event.TenantID) {
		return nil
	}
	attr := getMap(event.Data, "attribution")
	if attr == nil {
		return nil
	}
	signupChannel := getString(attr, "signup_channel")
	if signupChannel == "" {
		return nil
	}
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "attempt").Inc()
	}
	eventDataJSON, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("failed to marshal tenant_created event data: %w", err)
	}
	env := serviceEnvelopeColumns(event)
	chBatch, err := periscopeingestdb.PrepareTenantAcquisitionEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	defer chBatch.Close()
	if err := chBatch.Append(periscopeingestdb.TenantAcquisitionEventRow{
		Timestamp: event.Timestamp, TenantID: parseUUID(event.TenantID), UserID: optionalUUID(event.UserID),
		SignupChannel: signupChannel, SignupMethod: getString(attr, "signup_method"),
		UTMSource: optionalString(getString(attr, "utm_source")), UTMMedium: optionalString(getString(attr, "utm_medium")),
		UTMCampaign: optionalString(getString(attr, "utm_campaign")), UTMContent: optionalString(getString(attr, "utm_content")),
		UTMTerm: optionalString(getString(attr, "utm_term")), HTTPReferer: optionalString(getString(attr, "http_referer")),
		LandingPage: optionalString(getString(attr, "landing_page")), ReferralCode: optionalString(getString(attr, "referral_code")),
		IsAgent: boolToUInt8(getBool(attr, "is_agent")), EventData: string(eventDataJSON), SourceRegion: env.sourceRegion,
		StreamOriginRegion: env.streamOriginRegion, StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "error").Inc()
		}
		return err
	}
	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("tenant_acquisition_events", "success").Inc()
	}
	return nil
}

// processServiceEventAudit inserts service events into the api_events audit table.
func (h *AnalyticsHandler) processServiceEventAudit(ctx context.Context, event kafka.ServiceEvent) error {
	if !isValidUUIDString(event.TenantID) {
		h.writeIngestError(ctx, kafka.AnalyticsEvent{
			EventID:               event.EventID,
			EventType:             event.EventType,
			Timestamp:             event.Timestamp,
			Source:                event.Source,
			TenantID:              event.TenantID,
			Data:                  event.Data,
			SourceRegion:          event.SourceRegion,
			SourceClusterID:       event.SourceClusterID,
			StreamOriginRegion:    event.StreamOriginRegion,
			StreamOriginClusterID: event.StreamOriginClusterID,
			SchemaVersion:         event.SchemaVersion,
		}, "", "missing_or_invalid_tenant_id_service_event", nil)
		h.logger.WithFields(logging.Fields{
			"event_type": event.EventType,
			"event_id":   event.EventID,
			"tenant_id":  event.TenantID,
		}).Warn("Dropping service event audit write: missing or invalid tenant_id")
		if h.metrics != nil {
			h.metrics.AnalyticsEvents.WithLabelValues(event.EventType, "dropped").Inc()
		}
		return errDropped
	}

	data := sanitizeServiceEventData(event)

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "attempt").Inc()
	}

	detailsJSON, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal service event details: %w", err)
	}

	env := serviceEnvelopeColumns(event)
	chBatch, err := periscopeingestdb.PrepareAPIEvent(ctx, h.clickhouse)
	if err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}
	defer chBatch.Close()

	if err := chBatch.Append(periscopeingestdb.APIEventRow{
		EventID: parseUUID(event.EventID), TenantID: parseUUID(event.TenantID), EventType: event.EventType, Source: event.Source,
		UserID: optionalUUID(event.UserID), ResourceType: event.ResourceType, ResourceID: optionalString(event.ResourceID),
		Details: string(detailsJSON), Timestamp: event.Timestamp, ClusterID: event.SourceClusterID,
		SourceRegion: env.sourceRegion, StreamOriginRegion: env.streamOriginRegion,
		StreamOriginClusterID: env.streamOriginClusterID, SchemaVersion: env.schemaVersion,
	}); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if err := chBatch.Send(); err != nil {
		if h.metrics != nil {
			h.metrics.ClickHouseInserts.WithLabelValues("api_events", "error").Inc()
		}
		return err
	}

	if h.metrics != nil {
		h.metrics.ClickHouseInserts.WithLabelValues("api_events", "success").Inc()
	}

	return nil
}

func sanitizeServiceEventData(event kafka.ServiceEvent) map[string]interface{} {
	switch event.EventType {
	case "message_received", "message_updated":
		return allowlistEventData(event.Data, []string{"conversation_id", "message_id", "sender", "timestamp"})
	case "conversation_created", "conversation_updated":
		return allowlistEventData(event.Data, []string{"conversation_id", "status", "subject", "timestamp"})
	default:
		return event.Data
	}
}

func allowlistEventData(data map[string]interface{}, keys []string) map[string]interface{} {
	if data == nil {
		return nil
	}

	out := make(map[string]interface{}, len(keys))
	for _, key := range keys {
		if val, ok := data[key]; ok {
			out[key] = val
		}
	}

	return out
}

func getMap(data map[string]interface{}, key string) map[string]interface{} {
	if data == nil {
		return nil
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(map[string]interface{}); ok {
			return cast
		}
	}
	return nil
}

func getString(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(string); ok {
			return cast
		}
	}
	return ""
}

func getBool(data map[string]interface{}, key string) bool {
	if data == nil {
		return false
	}
	if value, ok := data[key]; ok {
		if cast, ok := value.(bool); ok {
			return cast
		}
	}
	return false
}

func boolToUInt8(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

// Helper functions for optional pointer fields
func nilIfEmptyStringPtr(s *string) interface{} {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nilIfZeroInt32Ptr(v *int32) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func nilIfZeroInt64Ptr(v *int64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func nilIfZeroUint64Ptr(v *uint64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

// valueOrNilUint64Ptr returns the value if pointer is non-nil (preserves 0), nil otherwise.
// Use this for fields where 0 is a valid value (e.g., packet stats - HLS has 0 packets).
func valueOrNilUint64Ptr(v *uint64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func valueOrNilUint32Ptr(v *uint32) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func valueOrNilFloat32Ptr(v *float32) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func valueOrNilFloat64Ptr(v *float64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}

func nilIfZeroFloat64Ptr(v *float64) interface{} {
	if v == nil || *v == 0 {
		return nil
	}
	return *v
}

func boolToNullableUInt8(v *bool) interface{} {
	if v == nil {
		return nil
	}
	if *v {
		return uint8(1)
	}
	return uint8(0)
}
