package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

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
	exec interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
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
	var id string
	row := exec.QueryRowContext(ctx, `
		INSERT INTO purser.billing_event_outbox
			(event_type, tenant_id, user_id, resource_type, resource_id, billing_event)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::jsonb)
		RETURNING id
	`, eventType, tenantID, userID, resourceType, resourceID, database.JSONText(billingJSON))
	if scanErr := row.Scan(&id); scanErr != nil {
		return "", fmt.Errorf("insert billing event outbox row: %w", scanErr)
	}
	return id, nil
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
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id::text, event_type, tenant_id::text, user_id, resource_type, resource_id,
			       billing_event::text, attempts, created_at
			FROM purser.billing_event_outbox
			WHERE completed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < NOW() - $1::interval)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(billingOutboxLease.Seconds())), billingOutboxBatchSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()

		batch := make([]billingOutboxRow, 0, billingOutboxBatchSize)
		for rows.Next() {
			var (
				r           billingOutboxRow
				billingText string
			)
			if scanErr := rows.Scan(&r.id, &r.eventType, &r.tenantID, &r.userID,
				&r.resourceType, &r.resourceID, &billingText, &r.attempts, &r.createdAt); scanErr != nil {
				return scanErr
			}
			r.billingJSON = []byte(billingText)
			batch = append(batch, r)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}

		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if _, uerr := tx.ExecContext(ctx, `
				UPDATE purser.billing_event_outbox
				SET claimed_at = NOW()
				WHERE id = ANY($1::uuid[])
			`, asPGUUIDArray(ids)); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *PurserServer) markBillingOutboxCompleted(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE purser.billing_event_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark billing event outbox row completed")
	}
}

func (s *PurserServer) recordBillingOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE purser.billing_event_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL
		WHERE id = $1::uuid
	`, id, attempts, msg); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record billing event outbox failure")
	}
	if attempts >= billingOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  attempts,
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

// asPGUUIDArray adapts a string slice into the {a,b,c} literal Postgres
// expects for a uuid[] parameter.
func asPGUUIDArray(ids []string) string {
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
