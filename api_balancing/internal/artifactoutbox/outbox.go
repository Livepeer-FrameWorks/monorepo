// Package artifactoutbox delivers Foghorn artifact, federation, and storage
// facts to Decklog through a durable outbox. Producers call the Enqueue
// helpers; a drain worker dispatches with exponential backoff.
package artifactoutbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/encoding/protojson"
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
// and the drain worker; the state-coupled node-copy path always supplies its own tx.
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

// EnqueueClipLifecycle writes a clip-lifecycle event to the outbox. The
// drain worker dispatches to Decklog with exponential backoff. Use
// EnqueueClipLifecycleTx when the caller already holds a transaction —
// the INSERT then rolls back with the caller's tx on failure.
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

func EnqueueClipLifecycle(data *ipcpb.ClipLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("clip %s: %w", data.GetClipHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(context.Background(), nil, kindClipLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetClipHash(), data)
}

func EnqueueClipLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.ClipLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("clip %s: %w", data.GetClipHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(ctx, tx, kindClipLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetClipHash(), data)
}

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

func EnqueueDVRLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.DVRLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("dvr %s: %w", data.GetDvrHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(ctx, tx, kindDVRLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetDvrHash(), data)
}

// EnqueueVodLifecycle leaves stream_id blank — VOD uploads aren't always
// associated with a live stream.
func EnqueueVodLifecycle(data *ipcpb.VodLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("vod %s: %w", data.GetVodHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(context.Background(), nil, kindVodLifecycle, data.GetTenantId(),
		"", data.GetVodHash(), data)
}

func EnqueueVodLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.VodLifecycleData) error {
	if data == nil {
		return ErrNilLifecyclePayload
	}
	if data.GetTenantId() == "" {
		return fmt.Errorf("vod %s: %w", data.GetVodHash(), ErrLifecycleMissingTenant)
	}
	return enqueue(ctx, tx, kindVodLifecycle, data.GetTenantId(),
		"", data.GetVodHash(), data)
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

// EnqueueArtifactNodeCopyTx writes a per-(artifact, node) local-copy transition to
// the outbox within the caller's transaction, so the durable state change and its
// telemetry commit atomically. tenantID rides the ServiceEvent envelope; the outbox
// row id becomes the event_id (stable dedupe key) at dispatch.
func EnqueueArtifactNodeCopyTx(ctx context.Context, tx execContext, tenantID string, data *ipcpb.ArtifactNodeCopyEvent) error {
	if data == nil {
		return nil
	}
	return enqueue(ctx, tx, kindArtifactNodeCopy, tenantID, "", data.GetArtifactHash(), data)
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

// EnqueueClipLifecycleLogged enqueues to the durable outbox but LOGS an enqueue failure instead of
// returning it — for callers that cannot propagate the error (no enclosing transaction to fail).
// Delivery is still durable once the row is written; only the enqueue-write failure (Init never wired,
// DB outage) is degraded to a log line so the outbox-bypass case stays observable.
func EnqueueClipLifecycleLogged(data *ipcpb.ClipLifecycleData) {
	if err := EnqueueClipLifecycle(data); err != nil && logger != nil {
		logger.WithError(err).Warn("artifactoutbox: enqueue clip lifecycle")
	}
}

func EnqueueDVRLifecycleLogged(data *ipcpb.DVRLifecycleData) {
	if err := EnqueueDVRLifecycle(data); err != nil && logger != nil {
		logger.WithError(err).Warn("artifactoutbox: enqueue dvr lifecycle")
	}
}

func EnqueueVodLifecycleLogged(data *ipcpb.VodLifecycleData) {
	if err := EnqueueVodLifecycle(data); err != nil && logger != nil {
		logger.WithError(err).Warn("artifactoutbox: enqueue vod lifecycle")
	}
}

// execContext is the subset of *sql.Tx / *sql.DB enqueue needs so callers
// can share their transaction with the outbox INSERT.
type execContext interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
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
	tid := tenantID
	_, err = target.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_event_outbox
			(event_kind, tenant_id, stream_id, artifact_id, payload)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5::jsonb)
	`, kind, tid, streamID, artifactID, database.JSONText(body))
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
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		batch, err := claimBatchOnce(ctx)
		if err != nil {
			return err
		}
		out = batch
		return nil
	})
	return out, err
}

func claimBatchOnce(ctx context.Context) ([]outboxRow, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	out, err := func() ([]outboxRow, error) {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id::text, event_kind, COALESCE(tenant_id::text, ''), stream_id, artifact_id,
			       payload::text, attempts, created_at
			FROM foghorn.artifact_event_outbox
			WHERE completed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < NOW() - $1::interval)
			  AND (next_retry_at IS NULL OR next_retry_at <= NOW())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(lease.Seconds())), batchSize)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()

		batch := make([]outboxRow, 0, batchSize)
		for rows.Next() {
			var (
				r        outboxRow
				payloadT string
			)
			if scanErr := rows.Scan(&r.id, &r.eventKind, &r.tenantID, &r.streamID,
				&r.artifactID, &payloadT, &r.attempts, &r.createdAt); scanErr != nil {
				return nil, scanErr
			}
			r.payload = []byte(payloadT)
			batch = append(batch, r)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return nil, rowsErr
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if _, uerr := tx.ExecContext(ctx, `
				UPDATE foghorn.artifact_event_outbox
				SET claimed_at = NOW()
				WHERE id = ANY($1::uuid[])
			`, idArray(ids)); uerr != nil {
				return nil, uerr
			}
		}
		return batch, nil
	}()
	if err != nil {
		return nil, err
	}
	if cerr := tx.Commit(); cerr != nil {
		return nil, cerr
	}
	return out, nil
}

func markCompleted(ctx context.Context, id string) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_event_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
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
	if _, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_event_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL, next_retry_at = NOW() + $4::interval
		WHERE id = $1::uuid
	`, id, newAttempts, msg, fmt.Sprintf("%d milliseconds", backoff.Milliseconds())); err != nil {
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

func idArray(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	out := "{"
	for i, id := range ids {
		if i > 0 {
			out += ","
		}
		out += id
	}
	out += "}"
	return out
}
