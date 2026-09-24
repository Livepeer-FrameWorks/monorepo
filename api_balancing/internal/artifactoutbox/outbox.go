// Package artifactoutbox delivers Foghorn artifact, federation, and storage
// facts to Decklog through a durable outbox. Producers call the Enqueue
// helpers; a drain worker dispatches with exponential backoff. The Transition
// helpers also write the transition's domain event in the same transaction
// (see internal/domainevents).
package artifactoutbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	baseBackoff        = 2 * time.Second
	maxBackoff         = 1 * time.Hour
	batchSize          = 32
	pollPeriod         = 30 * time.Second
	lease              = 60 * time.Second
	alertAfterAttempts = 12

	kindClipLifecycle    = "clip_lifecycle"
	kindDVRLifecycle     = "dvr_lifecycle"
	kindVodLifecycle     = "vod_lifecycle"
	kindFederationEvent  = "federation_event"
	kindArtifactNodeCopy = "artifact_node_copy"
	kindStorageSnapshot  = "storage_snapshot"
	kindArtifactDeleted  = "artifact_deleted"
)

func config() outbox.Config {
	return outbox.Config{
		BaseBackoff:        baseBackoff,
		MaxBackoff:         maxBackoff,
		BatchSize:          batchSize,
		PollPeriod:         pollPeriod,
		Lease:              lease,
		AlertAfterAttempts: alertAfterAttempts,
	}
}

// pkg-level dependencies (set at startup via Init). Keeps Enqueue helpers
// callable without threading the same handles through every producer
// (mirrors api_billing/internal/handlers init pattern).
var (
	db            *sql.DB
	logger        logging.Logger
	decklogClient *decklogclient.BatchedClient
)

// Init wires the package-level dependencies. Call once at process start.
// Safe to call with a nil decklogClient — the worker logs "disabled" and Enqueue
// calls still write outbox rows, so capture continues and delivery waits for a
// client. The package-level db is only used by non-transactional enqueue helpers
// and the drain worker; every Tx helper writes through the caller's transaction.
func Init(database *sql.DB, log logging.Logger, dc *decklogclient.BatchedClient) {
	db = database
	logger = log
	decklogClient = dc
}

// RunWorker drains foghorn.artifact_event_outbox to Decklog. Safe to run on
// every Foghorn replica — SKIP LOCKED + lease distribute work without
// leader election.
func RunWorker(ctx context.Context) {
	if db == nil {
		return
	}
	if decklogClient == nil {
		if logger != nil {
			logger.Info("foghorn artifact event outbox worker disabled: no decklog client")
		}
		return
	}
	cfg := config()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[outboxRow]{
		Config:     cfg,
		Store:      store{},
		Dispatcher: dispatcher{},
		Logger:     logger,
		AlertLabel: "foghorn artifact event",
	}
	worker.Run(ctx)
}

// ErrLifecycleMissingTenant is returned when an artifact-lifecycle event is enqueued without a tenant.
// A lifecycle event MUST be attributable — accepting an empty tenant (coerced to NULL in the outbox
// row) is fail-open, so the enqueue is rejected. On a Tx variant this rolls back the caller's state
// transition, keeping state and its analytics event consistent.
var ErrLifecycleMissingTenant = errors.New("artifactoutbox: lifecycle event requires a tenant")

// ErrNilLifecyclePayload is returned when a lifecycle enqueue is called with a nil payload. A nil
// payload is a programmer error (e.g. an event builder that returned nil for unresolved identity):
// silently returning success would let the caller's state transaction commit WITHOUT its required
// analytics event, so it is rejected — on a Tx variant this rolls the state transition back.
var ErrNilLifecyclePayload = errors.New("artifactoutbox: nil lifecycle payload")

// EnqueueClipLifecycleTx writes a clip lifecycle row in tx for a transition that
// is not a domain fact (queued, progress, deletion).
func EnqueueClipLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.ClipLifecycleData) error {
	return EnqueueClipTransitionTx(ctx, tx, data, nil)
}

// EnqueueClipTransitionTx writes the clip lifecycle row and, when fact is set,
// the domain event of the transition, both in tx and under one event ID.
// opts apply to the domain event (the actor of a requested transition).
func EnqueueClipTransitionTx(ctx context.Context, tx execContext, data *ipcpb.ClipLifecycleData, fact proto.Message, opts ...events.Option) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("clip %s: %w", data.GetClipHash(), ErrLifecycleMissingTenant)
	}
	return enqueueTransition(ctx, tx, kindClipLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetClipHash(), data, fact, opts...)
}

// EnqueueDVRLifecycle writes a DVR lifecycle row in its own statement. It serves
// a DVR start rejected before any artifact row exists: there is no state change
// to commit with, so the row is analytics only and carries no domain event.
func EnqueueDVRLifecycle(data *ipcpb.DVRLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("dvr %s: %w", data.GetDvrHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(context.Background(), nil, kindDVRLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetDvrHash(), data)
}

// EnqueueDVRLifecycleTx writes a DVR lifecycle row in tx for a transition that
// is not a domain fact (started, recording, deletion).
func EnqueueDVRLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.DVRLifecycleData) error {
	return EnqueueDVRTransitionTx(ctx, tx, data, nil)
}

// EnqueueDVRTransitionTx writes the DVR lifecycle row and, when fact is set,
// the domain event of the transition, both in tx and under one event ID.
func EnqueueDVRTransitionTx(ctx context.Context, tx execContext, data *ipcpb.DVRLifecycleData, fact proto.Message) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("dvr %s: %w", data.GetDvrHash(), ErrLifecycleMissingTenant)
	}
	return enqueueTransition(ctx, tx, kindDVRLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetDvrHash(), data, fact)
}

// EnqueueVodLifecycleTx writes a VOD lifecycle row in tx for a transition that
// is not a domain fact. VOD rows leave stream_id blank: uploads have no source
// stream.
func EnqueueVodLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.VodLifecycleData) error {
	return EnqueueVodTransitionTx(ctx, tx, data, nil)
}

// EnqueueVodTransitionTx writes the VOD lifecycle row and, when fact is set,
// the domain event of the transition, both in tx and under one event ID.
// opts apply to the domain event (the actor of a requested transition).
func EnqueueVodTransitionTx(ctx context.Context, tx execContext, data *ipcpb.VodLifecycleData, fact proto.Message, opts ...events.Option) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("vod %s: %w", data.GetVodHash(), ErrLifecycleMissingTenant)
	}
	return enqueueTransition(ctx, tx, kindVodLifecycle, data.GetTenantId(),
		"", data.GetVodHash(), data, fact, opts...)
}

func EnqueueFederationEvent(data *ipcpb.FederationEventData) error {
	if data == nil {
		return nil
	}
	return enqueue(context.Background(), nil, kindFederationEvent, data.GetTenantId(),
		data.GetStreamId(), "", data)
}

func EnqueueFederationEventTx(ctx context.Context, tx execContext, data *ipcpb.FederationEventData) error {
	if data == nil {
		return nil
	}
	return enqueue(ctx, tx, kindFederationEvent, data.GetTenantId(),
		data.GetStreamId(), "", data)
}

// EnqueueArtifactNodeCopyTx writes a per-(artifact, node) local-copy transition
// within the caller's transaction, as the legacy artifact_node_copy service event
// and the internal artifact.node_copy_changed domain event under one event ID.
// tenantID rides both envelopes.
func EnqueueArtifactNodeCopyTx(ctx context.Context, tx execContext, tenantID string, data *ipcpb.ArtifactNodeCopyEvent) error {
	if data == nil {
		return nil
	}
	fact := &internalv1.ArtifactNodeCopyChanged{
		ArtifactHash: data.GetArtifactHash(),
		NodeId:       data.GetNodeId(),
		Role:         data.GetRole(),
		Transition:   nodeCopyTransition(data.GetTransition()),
		IsComplete:   data.GetIsComplete(),
		SizeBytes:    data.GetSizeBytes(),
	}
	return enqueueTransition(ctx, tx, kindArtifactNodeCopy, tenantID, "", data.GetArtifactHash(), data, fact)
}

// EnqueueArtifactDeletedTx writes the artifact_deleted service event of a
// requested clip, recording, or upload deletion in the transaction that marks the
// artifact deleted. The event registry defines no domain type for a deletion, so
// it stays on service_events; the outbox row ID is its event ID.
func EnqueueArtifactDeletedTx(ctx context.Context, tx execContext, tenantID, userID string, artifactType ipcpb.ArtifactEvent_ArtifactType, artifactID, streamID string) error {
	if tx == nil {
		return errors.New("artifactoutbox: artifact_deleted needs the deletion transaction")
	}
	if tenantID == "" {
		return fmt.Errorf("artifact %s deletion: %w", artifactID, ErrLifecycleMissingTenant)
	}
	ev := &ipcpb.ServiceEvent{
		EventType:    kindArtifactDeleted,
		Source:       domainevents.Source,
		TenantId:     tenantID,
		UserId:       userID,
		ResourceType: "artifact",
		ResourceId:   artifactID,
		Payload: &ipcpb.ServiceEvent_ArtifactEvent{ArtifactEvent: &ipcpb.ArtifactEvent{
			ArtifactType: artifactType,
			ArtifactId:   artifactID,
			StreamId:     streamID,
			Status:       "deleted",
		}},
	}
	return enqueue(ctx, tx, kindArtifactDeleted, tenantID, streamID, artifactID, ev)
}

func nodeCopyTransition(t ipcpb.ArtifactNodeCopyEvent_Transition) internalv1.NodeCopyTransition {
	switch t {
	case ipcpb.ArtifactNodeCopyEvent_GAINED:
		return internalv1.NodeCopyTransition_NODE_COPY_TRANSITION_GAINED
	case ipcpb.ArtifactNodeCopyEvent_LOST:
		return internalv1.NodeCopyTransition_NODE_COPY_TRANSITION_LOST
	case ipcpb.ArtifactNodeCopyEvent_UPDATED:
		return internalv1.NodeCopyTransition_NODE_COPY_TRANSITION_UPDATED
	default:
		return internalv1.NodeCopyTransition_NODE_COPY_TRANSITION_UNSPECIFIED
	}
}

// EnqueueStorageSnapshot durably records an authoritative periodic storage
// observation. The snapshot's node owner supplies the trigger-envelope tenant;
// individual usage tenants remain in StorageSnapshot.Usage.
func EnqueueStorageSnapshot(data *ipcpb.StorageSnapshot) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return errors.New("artifactoutbox: storage snapshot requires an owner tenant")
	}
	return enqueue(context.Background(), nil, kindStorageSnapshot, data.GetTenantId(), "", data.GetNodeId(), data)
}

// execContext is the subset of *sql.Tx / *sql.DB enqueue needs so callers
// can share their transaction with the outbox INSERT.
type execContext interface {
	foghorndb.DBTX
}

// ErrDomainEventNeedsTx is returned when a transition with a domain event is
// enqueued without the transaction of its state change.
var ErrDomainEventNeedsTx = errors.New("artifactoutbox: a domain event needs the state-change transaction")

// enqueueTransition writes the legacy row and, when fact is set, the domain
// event into foghorn.domain_event_outbox. The legacy row then takes the event's
// ID, so consumers of either stream see one identity for the fact. The domain
// event is keyed by the artifact the fact names and, except for a node-copy
// change, carries that artifact's revision as its aggregate version.
func enqueueTransition(ctx context.Context, tx execContext, kind, tenantID, streamID, artifactID string, payload any, fact proto.Message, factOpts ...events.Option) error {
	if fact == nil {
		return enqueue(ctx, tx, kind, tenantID, streamID, artifactID, payload)
	}
	ev, err := enqueueArtifactFact(ctx, tx, tenantID, fact, factOpts...)
	if err != nil {
		return err
	}
	body, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	if err := foghorndb.New(tx).EnqueueArtifactEventWithID(ctx, foghorndb.EnqueueArtifactEventWithIDParams{
		ID:         ev.ID,
		EventKind:  kind,
		TenantID:   tenantID,
		StreamID:   streamID,
		ArtifactID: artifactID,
		Payload:    body,
	}); err != nil {
		return fmt.Errorf("insert artifact event outbox row: %w", err)
	}
	return nil
}

// EnqueueArtifactFactTx writes the domain event of an artifact transition that
// has no legacy lifecycle row (recording.stopped), in tx.
func EnqueueArtifactFactTx(ctx context.Context, tx execContext, tenantID string, fact proto.Message) error {
	if fact == nil {
		return ErrNilLifecyclePayload
	}
	if tenantID == "" {
		return fmt.Errorf("%T: %w", fact, ErrLifecycleMissingTenant)
	}
	_, err := enqueueArtifactFact(ctx, tx, tenantID, fact)
	return err
}

// enqueueArtifactFact writes fact into foghorn.domain_event_outbox, keyed by
// the artifact it names. A lifecycle fact first advances that artifact's
// revision and carries it as the aggregate version.
func enqueueArtifactFact(ctx context.Context, tx execContext, tenantID string, fact proto.Message, factOpts ...events.Option) (events.Event, error) {
	if tx == nil {
		return events.Event{}, ErrDomainEventNeedsTx
	}
	aggregateID, err := factArtifact(fact)
	if err != nil {
		return events.Event{}, err
	}
	opts := append([]events.Option(nil), factOpts...)
	if lifecycleFact(fact) {
		revision, revErr := foghorndb.New(tx).BumpArtifactRevision(ctx, foghorndb.BumpArtifactRevisionParams{
			ArtifactHash: aggregateID, TenantID: tenantID,
		})
		if errors.Is(revErr, sql.ErrNoRows) {
			return events.Event{}, fmt.Errorf("artifactoutbox: %T: %w", fact, ErrArtifactNotVersioned)
		}
		if revErr != nil {
			return events.Event{}, fmt.Errorf("advance artifact revision: %w", revErr)
		}
		opts = append(opts, events.WithAggregateVersion(revision))
	}
	return domainevents.Enqueue(ctx, tx, tenantID, aggregateID, fact, opts...)
}

// ErrArtifactNotVersioned is returned when a lifecycle event names an artifact
// the tenant has no row for, so it has no revision to carry.
var ErrArtifactNotVersioned = errors.New("artifactoutbox: lifecycle event names no artifact row of its tenant")

// lifecycleFact reports whether fact advances its artifact's revision. A
// node-copy change is ordered per (artifact, node) by its copy version and is
// reported from node inventory, so it neither advances nor carries the
// artifact revision.
func lifecycleFact(fact proto.Message) bool {
	_, nodeCopy := fact.(*internalv1.ArtifactNodeCopyChanged)
	return !nodeCopy
}

// factArtifact returns the artifact hash that keys fact's aggregate.
func factArtifact(fact proto.Message) (string, error) {
	var hash string
	switch m := fact.(type) {
	case interface{ GetArtifact() *publicv1.Artifact }:
		hash = m.GetArtifact().GetArtifactId()
	case *internalv1.ArtifactNodeCopyChanged:
		hash = m.GetArtifactHash()
	default:
		return "", fmt.Errorf("artifactoutbox: %T is not an artifact event", fact)
	}
	if hash == "" {
		return "", fmt.Errorf("artifactoutbox: %T names no artifact", fact)
	}
	return hash, nil
}

func enqueue(ctx context.Context, tx execContext, kind, tenantID, streamID, artifactID string, payload any) error {
	// A supplied tx writes the outbox row in the caller's transaction, independent of
	// the package-global db — so state-coupled callers work regardless of Init order.
	// Only a non-transactional call before Init has no target, and that is a wiring
	// error the caller must see, not a silent no-op that drops the event. Check the
	// concrete db (not the interface) so a nil *sql.DB isn't boxed into a non-nil target.
	target := tx
	if target == nil {
		if db == nil {
			return errors.New("artifactoutbox: enqueue requires a transaction or an initialized DB")
		}
		target = db
	}
	body, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	// outbox has a nullable tenant_id; empty-string callers (federation events
	// from the system tenant) coerce to NULL via the NULLIF below.
	err = foghorndb.New(target).EnqueueArtifactEvent(ctx, foghorndb.EnqueueArtifactEventParams{
		EventKind:  kind,
		TenantID:   tenantID,
		StreamID:   streamID,
		ArtifactID: artifactID,
		Payload:    body,
	})
	if err != nil {
		return fmt.Errorf("insert artifact event outbox row: %w", err)
	}
	return nil
}

// marshalPayload accepts the typed proto payload and serializes via protojson.
// Type switch enumerates the five state-coupled Foghorn message kinds; any
// other type is a programmer error and surfaces explicitly.
func marshalPayload(payload any) ([]byte, error) {
	switch m := payload.(type) {
	case *ipcpb.ClipLifecycleData:
		return protojson.Marshal(m)
	case *ipcpb.DVRLifecycleData:
		return protojson.Marshal(m)
	case *ipcpb.VodLifecycleData:
		return protojson.Marshal(m)
	case *ipcpb.FederationEventData:
		return protojson.Marshal(m)
	case *ipcpb.ArtifactNodeCopyEvent:
		return protojson.Marshal(m)
	case *ipcpb.StorageSnapshot:
		return protojson.Marshal(m)
	case *ipcpb.ServiceEvent:
		return protojson.Marshal(m)
	default:
		return nil, fmt.Errorf("unsupported artifact event payload type %T", payload)
	}
}

type outboxRow struct {
	id         string
	eventKind  string
	tenantID   string
	streamID   string
	artifactID string
	payload    []byte
	attempts   int
	createdAt  time.Time
}

type store struct{}

func (store) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[outboxRow], error) {
	rows, err := claimBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[outboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[outboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (store) MarkCompleted(ctx context.Context, id string) error {
	return markCompleted(ctx, id)
}

func (store) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, backoff time.Duration) error {
	return recordFailure(ctx, id, attempts, cause, backoff)
}

type dispatcher struct{}

func (dispatcher) Dispatch(ctx context.Context, row outboxRow) ([]string, error) {
	return dispatchRow(ctx, row)
}

func claimBatch(ctx context.Context) ([]outboxRow, error) {
	var out []outboxRow
	err := database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		out = nil
		rows, qerr := foghorndb.New(tx).ClaimArtifactEvents(ctx, foghorndb.ClaimArtifactEventsParams{
			LeaseSeconds: lease.Seconds(),
			BatchLimit:   batchSize,
		})
		if qerr != nil {
			return qerr
		}

		batch := make([]outboxRow, 0, len(rows))
		for _, row := range rows {
			batch = append(batch, outboxRow{
				id: row.ID, eventKind: row.EventKind, tenantID: row.TenantID,
				streamID: row.StreamID, artifactID: row.ArtifactID,
				payload: []byte(row.Payload), attempts: int(row.Attempts), createdAt: row.CreatedAt,
			})
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if uerr := foghorndb.New(tx).MarkArtifactEventsClaimed(ctx, ids); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func markCompleted(ctx context.Context, id string) error {
	if err := foghorndb.New(db).MarkArtifactEventCompleted(ctx, id); err != nil {
		if logger != nil {
			logger.WithError(err).WithField("outbox_id", id).
				Warn("Failed to mark foghorn artifact event outbox row completed")
		}
		return err
	}
	return nil
}

// recordFailure persists the INCREMENTED attempt count, the cause, releases the claim, and —
// critically — stamps next_retry_at = NOW() + backoff so the row drops out of the eligible set
// until its backoff elapses. The generic worker passes the pre-increment attempt count and the
// backoff it computed from it (ComputeBackoff); we persist attempts+1 so the counter actually
// grows (driving both the backoff schedule and the repeated-failure alert) instead of freezing
// at its inserted default.
func recordFailure(ctx context.Context, id string, attempts int, cause error, backoff time.Duration) error {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	newAttempts := attempts + 1
	if err := foghorndb.New(db).RecordArtifactEventFailure(ctx, foghorndb.RecordArtifactEventFailureParams{
		Attempts:          int32(newAttempts),
		LastError:         sql.NullString{String: msg, Valid: true},
		RetryMilliseconds: float64(backoff.Milliseconds()),
		ID:                id,
	}); err != nil {
		if logger != nil {
			logger.WithError(err).WithField("outbox_id", id).
				Warn("Failed to record foghorn artifact event outbox failure")
		}
		// Return the persistence error so the generic worker can retry: a lost failure-record write
		// would otherwise leave attempts/backoff frozen and re-deliver on the next claim.
		return err
	}
	if newAttempts >= alertAfterAttempts && logger != nil {
		logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  newAttempts,
			"cause":     msg,
		}).Error("Foghorn artifact event outbox row failing repeatedly — Decklog reachability degraded")
	}
	return nil
}

func dispatchRow(_ context.Context, row outboxRow) ([]string, error) {
	if decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	// sourceMs is the row's immutable created_at (the source transition time), stamped onto the event so
	// ingest can collapse artifact_state_current on a STABLE value instead of Decklog receipt time.
	// Because it lives on the outbox row it is identical across every at-least-once redelivery, so a
	// replayed older transition keeps its original time (best-effort: created_at is wall-clock, not a
	// source-owned monotonic revision, so concurrent same-artifact transitions can still tie/invert).
	sourceMs := row.createdAt.UnixMilli()
	switch row.eventKind {
	case kindClipLifecycle:
		data := &ipcpb.ClipLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal ClipLifecycleData: %w", err)
		}
		// row.id is the stable event_id (event-id-keyed projections dedupe on it); source_updated_at_ms
		// is the stable collapse key for artifact_state_current.
		data.SourceUpdatedAtMs = &sourceMs
		if err := decklogClient.SendClipLifecycleWithID(row.id, data); err != nil {
			return []string{"decklog"}, err
		}
	case kindDVRLifecycle:
		data := &ipcpb.DVRLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal DVRLifecycleData: %w", err)
		}
		data.SourceUpdatedAtMs = &sourceMs
		if err := decklogClient.SendDVRLifecycleWithID(row.id, data); err != nil {
			return []string{"decklog"}, err
		}
	case kindVodLifecycle:
		data := &ipcpb.VodLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal VodLifecycleData: %w", err)
		}
		data.SourceUpdatedAtMs = &sourceMs
		if err := decklogClient.SendVodLifecycleWithID(row.id, data); err != nil {
			return []string{"decklog"}, err
		}
	case kindFederationEvent:
		data := &ipcpb.FederationEventData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal FederationEventData: %w", err)
		}
		if err := decklogClient.SendFederationEvent(data); err != nil {
			return []string{"decklog"}, err
		}
	case kindArtifactNodeCopy:
		data := &ipcpb.ArtifactNodeCopyEvent{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal ArtifactNodeCopyEvent: %w", err)
		}
		// The outbox row id is the stable event_id — replays carry the same id so
		// the ClickHouse log dedupes.
		ev := &ipcpb.ServiceEvent{
			EventId:   row.id,
			EventType: "artifact_node_copy",
			Source:    "foghorn",
			TenantId:  row.tenantID,
			Payload:   &ipcpb.ServiceEvent_ArtifactNodeCopyEvent{ArtifactNodeCopyEvent: data},
		}
		if err := decklogClient.SendServiceEvent(ev); err != nil {
			return []string{"decklog"}, err
		}
	case kindArtifactDeleted:
		ev := &ipcpb.ServiceEvent{}
		if err := protojson.Unmarshal(row.payload, ev); err != nil {
			return nil, fmt.Errorf("unmarshal artifact_deleted ServiceEvent: %w", err)
		}
		// The row id and its commit time are fixed, so every redelivery carries the same identity.
		ev.EventId = row.id
		ev.Timestamp = timestamppb.New(row.createdAt)
		if err := decklogClient.SendServiceEvent(ev); err != nil {
			return []string{"decklog"}, err
		}
	case kindStorageSnapshot:
		data := &ipcpb.StorageSnapshot{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal StorageSnapshot: %w", err)
		}
		trigger := &ipcpb.MistTrigger{
			TriggerType: "STORAGE_SNAPSHOT",
			NodeId:      data.GetNodeId(),
			Timestamp:   data.GetTimestamp(),
			TenantId:    stringPointer(data.GetTenantId()),
			ClusterId:   stringPointer(data.GetStorageProviderClusterId()),
			EventId:     row.id,
			TriggerPayload: &ipcpb.MistTrigger_StorageSnapshot{
				StorageSnapshot: data,
			},
		}
		if err := decklogClient.SendTrigger(trigger); err != nil {
			return []string{"decklog"}, err
		}
	default:
		return nil, fmt.Errorf("unknown artifact event kind %q", row.eventKind)
	}
	return nil, nil
}

func stringPointer(value string) *string { return &value }
