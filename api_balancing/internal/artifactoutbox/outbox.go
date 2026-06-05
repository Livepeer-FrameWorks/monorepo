// Package artifactoutbox delivers Foghorn artifact-lifecycle (DVR / VOD
// / Clip) and federation peer-registry events to Decklog through a
// durable outbox. Producers call the Enqueue helpers; a drain worker
// dispatches with exponential backoff.
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

	kindClipLifecycle   = "clip_lifecycle"
	kindDVRLifecycle    = "dvr_lifecycle"
	kindVodLifecycle    = "vod_lifecycle"
	kindFederationEvent = "federation_event"
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
// Safe to call with a nil decklogClient — the worker will log "disabled"
// and Enqueue calls still write outbox rows for retention.
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
func EnqueueClipLifecycle(data *ipcpb.ClipLifecycleData) error {
	if data == nil {
		return nil
	}
	return enqueue(context.Background(), nil, kindClipLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetClipHash(), data)
}

func EnqueueClipLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.ClipLifecycleData) error {
	if data == nil {
		return nil
	}
	return enqueue(ctx, tx, kindClipLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetClipHash(), data)
}

func EnqueueDVRLifecycle(data *ipcpb.DVRLifecycleData) error {
	if data == nil {
		return nil
	}
	return enqueue(context.Background(), nil, kindDVRLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetDvrHash(), data)
}

func EnqueueDVRLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.DVRLifecycleData) error {
	if data == nil {
		return nil
	}
	return enqueue(ctx, tx, kindDVRLifecycle, data.GetTenantId(),
		data.GetStreamId(), data.GetDvrHash(), data)
}

// EnqueueVodLifecycle leaves stream_id blank — VOD uploads aren't always
// associated with a live stream.
func EnqueueVodLifecycle(data *ipcpb.VodLifecycleData) error {
	if data == nil {
		return nil
	}
	return enqueue(context.Background(), nil, kindVodLifecycle, data.GetTenantId(),
		"", data.GetVodHash(), data)
}

func EnqueueVodLifecycleTx(ctx context.Context, tx execContext, data *ipcpb.VodLifecycleData) error {
	if data == nil {
		return nil
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

// EnqueueClipLifecycleLogged is the fire-and-forget variant used from
// background goroutines. Enqueue failures land on the package logger so
// the outbox-bypass case (Init never wired, DB outage) is observable.
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
	if db == nil {
		return nil
	}
	body, err := marshalPayload(payload)
	if err != nil {
		return err
	}
	target := tx
	if target == nil {
		target = db
	}
	// outbox has a nullable tenant_id; empty-string callers (federation events
	// from the system tenant) coerce to NULL via the NULLIF below.
	tid := tenantID
	_, err = target.ExecContext(ctx, `
		INSERT INTO foghorn.artifact_event_outbox
			(event_kind, tenant_id, stream_id, artifact_id, payload)
		VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5::jsonb)
	`, kind, tid, streamID, artifactID, body)
	if err != nil {
		return fmt.Errorf("insert artifact event outbox row: %w", err)
	}
	return nil
}

// marshalPayload accepts the typed proto payload and serializes via protojson.
// Type switch enumerates the four state-coupled Foghorn message kinds; any
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
	markCompleted(ctx, id)
	return nil
}

func (store) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration) error {
	recordFailure(ctx, id, attempts, cause)
	return nil
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

func markCompleted(ctx context.Context, id string) {
	if _, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_event_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil && logger != nil {
		logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark foghorn artifact event outbox row completed")
	}
}

func recordFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE foghorn.artifact_event_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL
		WHERE id = $1::uuid
	`, id, attempts, msg); err != nil && logger != nil {
		logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record foghorn artifact event outbox failure")
	}
	if attempts >= alertAfterAttempts && logger != nil {
		logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  attempts,
			"cause":     msg,
		}).Error("Foghorn artifact event outbox row failing repeatedly — Decklog reachability degraded")
	}
}

func dispatchRow(_ context.Context, row outboxRow) ([]string, error) {
	if decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	switch row.eventKind {
	case kindClipLifecycle:
		data := &ipcpb.ClipLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal ClipLifecycleData: %w", err)
		}
		if err := decklogClient.SendClipLifecycle(data); err != nil {
			return []string{"decklog"}, err
		}
	case kindDVRLifecycle:
		data := &ipcpb.DVRLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal DVRLifecycleData: %w", err)
		}
		if err := decklogClient.SendDVRLifecycle(data); err != nil {
			return []string{"decklog"}, err
		}
	case kindVodLifecycle:
		data := &ipcpb.VodLifecycleData{}
		if err := protojson.Unmarshal(row.payload, data); err != nil {
			return nil, fmt.Errorf("unmarshal VodLifecycleData: %w", err)
		}
		if err := decklogClient.SendVodLifecycle(data); err != nil {
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
	default:
		return nil, fmt.Errorf("unknown artifact event kind %q", row.eventKind)
	}
	return nil, nil
}

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
