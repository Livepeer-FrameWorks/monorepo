package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	billingOutboxBaseBackoff        = 2 * time.Second
	billingOutboxMaxBackoff         = 1 * time.Hour
	billingOutboxBatchSize          = 32
	billingOutboxPollPeriod         = 30 * time.Second
	billingOutboxLease              = 60 * time.Second
	billingOutboxAlertAfterAttempts = 12
)

func billingOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        billingOutboxBaseBackoff,
		MaxBackoff:         billingOutboxMaxBackoff,
		BatchSize:          billingOutboxBatchSize,
		PollPeriod:         billingOutboxPollPeriod,
		Lease:              billingOutboxLease,
		AlertAfterAttempts: billingOutboxAlertAfterAttempts,
	}
}

// billingOutboxRow is the payload shape pulled out of
// purser.billing_event_outbox for dispatch. We marshal the
// pb.BillingEvent oneof variant as protojson at enqueue time so dispatch
// can reassemble the pb.ServiceEvent without re-querying schema.
type billingOutboxRow struct {
	id           string
	eventType    string
	tenantID     string
	userID       string
	resourceType string
	resourceID   string
	billingJSON  []byte
	attempts     int
	createdAt    time.Time
}

// EnqueueBillingEventTx writes a billing-event outbox row inside the
// caller's transaction. A failed INSERT rolls back with the caller's tx.
// Callers without a tx use enqueueBillingEvent below.
func (s *PurserServer) EnqueueBillingEventTx(
	ctx context.Context,
	exec purserdb.DBTX,
	eventType, tenantID, userID, resourceType, resourceID string,
	payload *ipcpb.BillingEvent,
) (string, error) {
	if payload == nil {
		payload = &ipcpb.BillingEvent{}
	}
	if payload.TenantId == "" {
		payload.TenantId = tenantID
	}
	billingJSON, err := protojson.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal billing event: %w", err)
	}
	id, err := purserdb.New(exec).EnqueueBillingEventOutbox(ctx, purserdb.EnqueueBillingEventOutboxParams{
		EventType: eventType, TenantID: tenantID, UserID: userID,
		ResourceType: resourceType, ResourceID: resourceID, BillingEvent: billingJSON,
	})
	if err != nil {
		return "", fmt.Errorf("insert billing event outbox row: %w", err)
	}
	return id.String(), nil
}

// enqueueBillingEvent writes the outbox row in its own short transaction.
// Use EnqueueBillingEventTx when the caller already holds a transaction.
func (s *PurserServer) enqueueBillingEvent(
	ctx context.Context,
	eventType, tenantID, userID, resourceType, resourceID string,
	payload *ipcpb.BillingEvent,
) {
	if s.db == nil || tenantID == "" {
		return
	}
	if _, err := s.EnqueueBillingEventTx(ctx, s.db, eventType, tenantID, userID, resourceType, resourceID, payload); err != nil {
		s.logger.WithError(err).WithField("event_type", eventType).
			Warn("Failed to enqueue billing event outbox row")
	}
}

type billingOutboxStore struct {
	server *PurserServer
}

func (st *billingOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[billingOutboxRow], error) {
	rows, err := st.server.claimBillingOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[billingOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[billingOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *billingOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	st.server.markBillingOutboxCompleted(ctx, id)
	return nil
}

func (st *billingOutboxStore) RecordFailure(ctx context.Context, id string, currentAttempts int, _ []string, cause error, _ time.Duration) error {
	st.server.recordBillingOutboxFailure(ctx, id, currentAttempts, cause)
	return nil
}

type billingOutboxDispatcher struct {
	server *PurserServer
}

func (d *billingOutboxDispatcher) Dispatch(ctx context.Context, row billingOutboxRow) ([]string, error) {
	return d.server.dispatchBillingOutboxRow(ctx, row)
}

// runBillingOutboxWorker polls purser.billing_event_outbox and dispatches
// pending rows to Decklog. Safe to run on every Purser replica — SKIP LOCKED
// + lease-window claim makes the work distributable.
func (s *PurserServer) runBillingOutboxWorker(ctx context.Context) {
	if s.decklogClient == nil {
		s.logger.Info("billing event outbox worker disabled: no decklog client")
		return
	}
	cfg := billingOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[billingOutboxRow]{
		Config:     cfg,
		Store:      &billingOutboxStore{server: s},
		Dispatcher: &billingOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "purser billing event",
	}
	worker.Run(ctx)
}

func (s *PurserServer) claimBillingOutboxBatch(ctx context.Context) ([]billingOutboxRow, error) {
	var out []billingOutboxRow
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		rows, qerr := queries.ClaimBillingEventOutboxCandidates(ctx, purserdb.ClaimBillingEventOutboxCandidatesParams{
			LeaseMilliseconds: billingOutboxLease.Milliseconds(), BatchSize: billingOutboxBatchSize,
		})
		if qerr != nil {
			return qerr
		}

		batch := make([]billingOutboxRow, 0, billingOutboxBatchSize)
		ids := make([]uuid.UUID, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
			batch = append(batch, billingOutboxRow{
				id: row.ID.String(), eventType: row.EventType, tenantID: row.TenantID.String(),
				userID: row.UserID, resourceType: row.ResourceType, resourceID: row.ResourceID,
				billingJSON: row.BillingEvent, attempts: int(row.Attempts), createdAt: row.CreatedAt,
			})
		}

		if len(ids) > 0 {
			if uerr := queries.MarkBillingEventOutboxClaimed(ctx, ids); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *PurserServer) markBillingOutboxCompleted(ctx context.Context, id string) {
	if err := purserdb.New(s.db).CompleteBillingEventOutbox(ctx, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark billing event outbox row completed")
	}
}

func (s *PurserServer) recordBillingOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := purserdb.New(s.db).FailBillingEventOutbox(ctx, purserdb.FailBillingEventOutboxParams{
		ID: id, LastError: sql.NullString{String: msg, Valid: true},
	}); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record billing event outbox failure")
	}
	nextAttempts := attempts + 1
	if nextAttempts >= billingOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  nextAttempts,
			"cause":     msg,
		}).Error("Billing event outbox row failing repeatedly — Decklog reachability degraded")
	}
}

// dispatchBillingOutboxRow reassembles the pb.ServiceEvent from the outbox
// row and forwards it through the decklog client (which auto-stamps envelope
// v2 fields per P0.C).
func (s *PurserServer) dispatchBillingOutboxRow(ctx context.Context, row billingOutboxRow) ([]string, error) {
	if s.decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	payload := &ipcpb.BillingEvent{}
	if len(row.billingJSON) > 0 {
		if err := protojson.Unmarshal(row.billingJSON, payload); err != nil {
			return nil, fmt.Errorf("unmarshal billing event payload: %w", err)
		}
	}
	if payload.TenantId == "" {
		payload.TenantId = row.tenantID
	}
	event := &ipcpb.ServiceEvent{
		EventType:    row.eventType,
		Timestamp:    timestamppb.New(row.createdAt),
		Source:       "purser",
		TenantId:     row.tenantID,
		UserId:       row.userID,
		ResourceType: row.resourceType,
		ResourceId:   row.resourceID,
		Payload:      &ipcpb.ServiceEvent_BillingEvent{BillingEvent: payload},
	}
	_ = ctx // decklog client manages its own context via authContext()
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}
