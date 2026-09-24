package handlers

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
)

// artifactProjection places one artifact lifecycle event type in
// artifact_events and artifact_state_current_v2. content_type and stage use the
// values Foghorn's lifecycle rows on analytics_events carry for the same
// transition, so both copies of a fact (which share its event ID) describe it
// alike and existing stage filters keep matching.
type artifactProjection struct {
	contentType string
	stage       string
}

var artifactProjections = map[string]artifactProjection{
	"clip.requested": {contentType: "clip", stage: "requested"},
	"clip.ready":     {contentType: "clip", stage: "done"},
	"clip.failed":    {contentType: "clip", stage: "failed"},
	// recording.stopped is not projected: the recording's artifact state moves
	// on at recording.ready or recording.failed, and no lifecycle row carries a
	// finalizing stage. It still reaches api_events.
	"recording.started": {contentType: "dvr", stage: "recording"},
	"recording.ready":   {contentType: "dvr", stage: "stopped"},
	"recording.failed":  {contentType: "dvr", stage: "failed"},
	"upload.created":    {contentType: "vod", stage: "requested"},
	"upload.completed":  {contentType: "vod", stage: "processing"},
	"upload.aborted":    {contentType: "vod", stage: "deleted"},
	"upload.ready":      {contentType: "vod", stage: "completed"},
	"upload.failed":     {contentType: "vod", stage: "failed"},
}

// Domain event outcomes counted in PeriscopeMetrics.DomainEvents.
const (
	domainEventProcessed   = "processed"
	domainEventUnknownType = "unknown_type"
	domainEventInvalid     = "invalid"
	domainEventError       = "error"
)

// HandleDomainEventMessage projects one domain.events record, read from the
// local topic or a mirrored copy, into ClickHouse:
//   - every public tenant event and every audited internal one into
//     api_events, with its actor;
//   - artifact lifecycle events into artifact_events (event_id = ce_id) and
//     artifact_state_current_v2.
//
// The same event can arrive more than once (local and mirrored copies,
// redelivery) and, while producers dual-write, as a legacy row with the same
// ID; every table it lands in collapses those copies on the event ID.
//
// A type this binary does not register is counted and logged, not stored: the
// producer's registry is newer, and the record is skipped so the partition
// keeps moving. An undecodable record returns an error, which the consumer
// wrapper sends to the DLQ; a ClickHouse error returns as-is, so a transient one
// is retried in place.
func (h *AnalyticsHandler) HandleDomainEventMessage(ctx context.Context, msg kafka.Message) error {
	headers := make([]events.Header, 0, len(msg.Headers))
	for key, value := range msg.Headers {
		headers = append(headers, events.Header{Key: key, Value: []byte(value)})
	}
	rec, err := events.ParseRecord(msg.Key, headers, msg.Value)
	if err != nil {
		eventType := msg.Headers[events.HeaderType]
		fields := logging.Fields{
			"event_type": eventType,
			"event_id":   msg.Headers[events.HeaderID],
			"topic":      msg.Topic,
			"partition":  msg.Partition,
			"offset":     msg.Offset,
		}
		if errors.Is(err, events.ErrUnknownType) {
			h.countDomainEvent(eventType, domainEventUnknownType)
			h.logger.WithError(err).WithFields(fields).Error("Skipping domain event of a type this Periscope Ingest does not register; upgrade Periscope Ingest")
			return nil
		}
		h.countDomainEvent(eventType, domainEventInvalid)
		return fmt.Errorf("parse domain event: %w", err)
	}

	if err := h.writeDomainAuditEvent(ctx, rec); err != nil {
		h.countDomainEvent(rec.Type, domainEventError)
		return err
	}
	if projection, ok := artifactProjections[rec.Type]; ok {
		if err := h.writeDomainArtifactEvent(ctx, rec, projection); err != nil {
			h.countDomainEvent(rec.Type, domainEventError)
			return err
		}
	}
	h.countDomainEvent(rec.Type, domainEventProcessed)
	return nil
}

func (h *AnalyticsHandler) countDomainEvent(eventType, status string) {
	if h.metrics == nil || h.metrics.DomainEvents == nil {
		return
	}
	h.metrics.DomainEvents.WithLabelValues(eventType, status).Inc()
}

var domainDetailsJSON = protojson.MarshalOptions{UseProtoNames: true}

// auditedInternalTypes are the internal event types that record a change to
// the tenant's own account, billing, cluster access, or webhook endpoints, so
// they belong in the tenant's audit log. Every other internal type stays out
// of api_events: artifact.node_copy_changed names a storage node, and
// recording.chapter_ready is a processing step, not a tenant action. A new
// internal type is not audited until it is listed here.
var auditedInternalTypes = map[string]bool{
	"tenant.created":                 true,
	"tenant.updated":                 true,
	"tenant.deleted":                 true,
	"tenant.cluster_assigned":        true,
	"tenant.cluster_unassigned":      true,
	"cluster.invite_created":         true,
	"cluster.invite_revoked":         true,
	"cluster.subscription_requested": true,
	"cluster.subscription_approved":  true,
	"cluster.subscription_rejected":  true,
	"billing.payment_created":        true,
	"billing.subscription_created":   true,
	"billing.subscription_updated":   true,
	"webhook.endpoint_auto_disabled": true,
}

// writeDomainAuditEvent writes the api_events row of a public tenant event or
// an audited internal one (auditedInternalTypes). The table is
// tenant-partitioned, so platform-scoped events have no audit row, as on the
// service_events path.
func (h *AnalyticsHandler) writeDomainAuditEvent(ctx context.Context, rec events.Record) error {
	if rec.TenantID == "" {
		return nil
	}
	if !rec.Spec.Public() && !auditedInternalTypes[rec.Type] {
		return nil
	}
	details, err := domainDetailsJSON.Marshal(rec.Message)
	if err != nil {
		return fmt.Errorf("marshal %s details: %w", rec.Type, err)
	}
	batch, err := periscopeingestdb.PrepareAPIEvent(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer func() { _ = batch.Close() }()
	if appendErr := batch.Append(periscopeingestdb.APIEventRow{
		EventID: parseUUID(rec.ID), TenantID: parseUUID(rec.TenantID), EventType: rec.Type, Source: rec.Source,
		UserID: optionalUUID(rec.Actor.UserID), ResourceType: rec.Spec.Aggregate, ResourceID: optionalString(rec.AggregateID),
		Details: string(details), Timestamp: rec.Time, ClusterID: rec.SourceCluster, SourceRegion: rec.SourceRegion,
		ActorAuthType: rec.Actor.AuthType, ActorTokenHash: rec.Actor.TokenHash,
	}); appendErr != nil {
		return appendErr
	}
	return batch.Send()
}

// artifactFields are the payload fields the artifact lifecycle messages share.
type artifactFields struct {
	artifact   *publicv1.Artifact
	sizeBytes  *uint64
	durationMS *int64
	filename   *string
	reason     string
}

func artifactFieldsOf(rec events.Record) artifactFields {
	var fields artifactFields
	if m, ok := rec.Message.(interface{ GetArtifact() *publicv1.Artifact }); ok {
		fields.artifact = m.GetArtifact()
	}
	if m, ok := rec.Message.(interface{ GetSizeBytes() int64 }); ok && m.GetSizeBytes() > 0 {
		size := uint64(m.GetSizeBytes())
		fields.sizeBytes = &size
	}
	if m, ok := rec.Message.(interface{ GetDurationMs() int64 }); ok && m.GetDurationMs() > 0 {
		duration := m.GetDurationMs()
		fields.durationMS = &duration
	}
	if m, ok := rec.Message.(interface{ GetFilename() string }); ok {
		fields.filename = optionalString(m.GetFilename())
	}
	if m, ok := rec.Message.(interface {
		GetReason() publicv1.MediaFailureReason
	}); ok {
		fields.reason = strings.ToLower(strings.TrimPrefix(m.GetReason().String(), "MEDIA_FAILURE_REASON_"))
	}
	return fields
}

// writeDomainArtifactEvent writes an artifact lifecycle event to
// artifact_events and artifact_state_current_v2. The artifact ID is the
// aggregate ID, which is the artifact hash that keys the legacy rows too.
func (h *AnalyticsHandler) writeDomainArtifactEvent(ctx context.Context, rec events.Record, projection artifactProjection) error {
	fields := artifactFieldsOf(rec)
	streamID := uuid.Nil
	if fields.artifact != nil {
		streamID = parseUUID(fields.artifact.GetStreamId())
	}
	tenantID := parseUUID(rec.TenantID)

	eventBatch, err := periscopeingestdb.PrepareDomainArtifactEvent(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer func() { _ = eventBatch.Close() }()
	if appendErr := eventBatch.Append(periscopeingestdb.DomainArtifactEventRow{
		Timestamp: rec.Time, TenantID: tenantID, StreamID: streamID, RequestID: rec.AggregateID,
		Stage: projection.stage, ContentType: projection.contentType, Filename: fields.filename,
		SizeBytes: fields.sizeBytes, Message: optionalString(fields.reason), SourceRegion: rec.SourceRegion,
		EventID: rec.ID,
	}); appendErr != nil {
		return appendErr
	}
	if sendErr := eventBatch.Send(); sendErr != nil {
		return sendErr
	}

	aggregateVersion := uint64(0)
	if rec.AggregateVersion > 0 {
		aggregateVersion = uint64(rec.AggregateVersion)
	}
	stateBatch, err := periscopeingestdb.PrepareArtifactStateV2(ctx, h.clickhouse)
	if err != nil {
		return err
	}
	defer func() { _ = stateBatch.Close() }()
	if appendErr := stateBatch.Append(periscopeingestdb.ArtifactStateV2Row{
		TenantID: tenantID, ArtifactID: rec.AggregateID, ContentType: projection.contentType, StreamID: streamID,
		Stage: projection.stage, EventType: rec.Type, FailureReason: fields.reason, Filename: fields.filename,
		SizeBytes: fields.sizeBytes, DurationMS: fields.durationMS, UpdatedAt: rec.Time, EventID: rec.ID,
		AggregateVersion: aggregateVersion, Version: artifactStateVersion(rec.ID, rec.AggregateVersion),
	}); appendErr != nil {
		return appendErr
	}
	return stateBatch.Send()
}

// stampedVersionBit marks a version taken from the producer's aggregate
// version; it sorts above every version derived from an event ID.
const stampedVersionBit = uint64(1) << 63

// artifactStateVersion is the artifact_state_current_v2 replacement version of
// an event (see periscope.sql): a stamped aggregate version when the producer
// set one, else the UUIDv7 event ID's (unix_ms << 12) | rand_a, which the
// generator keeps strictly increasing within a process.
func artifactStateVersion(eventID string, aggregateVersion int64) uint64 {
	if aggregateVersion > 0 {
		return stampedVersionBit | uint64(aggregateVersion)
	}
	id, err := uuid.Parse(eventID)
	if err != nil || id.Version() != 7 {
		return 0
	}
	var ms [8]byte
	copy(ms[2:], id[0:6])
	randA := uint64(id[6]&0x0f)<<8 | uint64(id[7])
	return binary.BigEndian.Uint64(ms[:])<<12 | randA
}
