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
)

const (
	qmOutboxBaseBackoff        = 2 * time.Second
	qmOutboxMaxBackoff         = 1 * time.Hour
	qmOutboxBatchSize          = 32
	qmOutboxPollPeriod         = 30 * time.Second
	qmOutboxLease              = 60 * time.Second
	qmOutboxAlertAfterAttempts = 12
)

func qmOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        qmOutboxBaseBackoff,
		MaxBackoff:         qmOutboxMaxBackoff,
		BatchSize:          qmOutboxBatchSize,
		PollPeriod:         qmOutboxPollPeriod,
		Lease:              qmOutboxLease,
		AlertAfterAttempts: qmOutboxAlertAfterAttempts,
	}
}

type qmOutboxRow struct {
	id        string
	payload   []byte
	attempts  int
	createdAt time.Time
}

// EnqueueServiceEventTx writes the outbox row inside the caller's
// transaction. A failed INSERT rolls back with the caller's tx.
func (s *QuartermasterServer) EnqueueServiceEventTx(
	ctx context.Context,
	exec interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	event *ipcpb.ServiceEvent,
) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	var id string
	row := exec.QueryRowContext(ctx, `
		INSERT INTO quartermaster.service_event_outbox
			(event_type, tenant_id, user_id, resource_type, resource_id, payload)
		VALUES ($1, $2::uuid, $3, $4, $5, $6::jsonb)
		RETURNING id
	`, event.GetEventType(), event.GetTenantId(), event.GetUserId(),
		event.GetResourceType(), event.GetResourceId(), payload)
	if scanErr := row.Scan(&id); scanErr != nil {
		return "", fmt.Errorf("insert service event outbox row: %w", scanErr)
	}
	return id, nil
}

// enqueueServiceEvent writes the outbox row in its own short transaction.
// Use EnqueueServiceEventTx when the caller already holds a transaction.
func (s *QuartermasterServer) enqueueServiceEvent(ctx context.Context, event *ipcpb.ServiceEvent) {
	if s.db == nil || event == nil || event.GetTenantId() == "" {
		return
	}
	if _, err := s.EnqueueServiceEventTx(ctx, s.db, event); err != nil {
		s.logger.WithError(err).WithField("event_type", event.GetEventType()).
			Warn("Failed to enqueue service event outbox row")
	}
}

type qmOutboxStore struct {
	server *QuartermasterServer
}

func (st *qmOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[qmOutboxRow], error) {
	rows, err := st.server.claimQMOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[qmOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[qmOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *qmOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	st.server.markQMOutboxCompleted(ctx, id)
	return nil
}

func (st *qmOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration) error {
	st.server.recordQMOutboxFailure(ctx, id, attempts, cause)
	return nil
}

type qmOutboxDispatcher struct {
	server *QuartermasterServer
}

func (d *qmOutboxDispatcher) Dispatch(ctx context.Context, row qmOutboxRow) ([]string, error) {
	return d.server.dispatchQMOutboxRow(ctx, row)
}

// runServiceEventOutboxWorker drains quartermaster.service_event_outbox to
// Decklog. Safe to run on every Quartermaster replica — SKIP LOCKED + lease
// makes work distributable.
func (s *QuartermasterServer) runServiceEventOutboxWorker(ctx context.Context) {
	if s.decklogClient == nil {
		s.logger.Info("quartermaster service event outbox worker disabled: no decklog client")
		return
	}
	cfg := qmOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[qmOutboxRow]{
		Config:     cfg,
		Store:      &qmOutboxStore{server: s},
		Dispatcher: &qmOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "quartermaster service event",
	}
	worker.Run(ctx)
}

func (s *QuartermasterServer) claimQMOutboxBatch(ctx context.Context) ([]qmOutboxRow, error) {
	var out []qmOutboxRow
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id::text, payload::text, attempts, created_at
			FROM quartermaster.service_event_outbox
			WHERE completed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < NOW() - $1::interval)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(qmOutboxLease.Seconds())), qmOutboxBatchSize)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()

		batch := make([]qmOutboxRow, 0, qmOutboxBatchSize)
		for rows.Next() {
			var (
				r           qmOutboxRow
				payloadText string
			)
			if scanErr := rows.Scan(&r.id, &payloadText, &r.attempts, &r.createdAt); scanErr != nil {
				return scanErr
			}
			r.payload = []byte(payloadText)
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
				UPDATE quartermaster.service_event_outbox
				SET claimed_at = NOW()
				WHERE id = ANY($1::uuid[])
			`, qmOutboxIDArray(ids)); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *QuartermasterServer) markQMOutboxCompleted(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.service_event_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark quartermaster service event outbox row completed")
	}
}

func (s *QuartermasterServer) recordQMOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE quartermaster.service_event_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL
		WHERE id = $1::uuid
	`, id, attempts, msg); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record quartermaster service event outbox failure")
	}
	if attempts >= qmOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  attempts,
			"cause":     msg,
		}).Error("Quartermaster service event outbox row failing repeatedly — Decklog reachability degraded")
	}
}

func (s *QuartermasterServer) dispatchQMOutboxRow(ctx context.Context, row qmOutboxRow) ([]string, error) {
	if s.decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	event := &ipcpb.ServiceEvent{}
	if err := protojson.Unmarshal(row.payload, event); err != nil {
		return nil, fmt.Errorf("unmarshal service event payload: %w", err)
	}
	_ = ctx // decklog client manages its own context
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}

func qmOutboxIDArray(ids []string) string {
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
