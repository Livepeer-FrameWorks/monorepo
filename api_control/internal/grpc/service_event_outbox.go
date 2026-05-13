package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	commodoreServiceOutboxBaseBackoff        = 2 * time.Second
	commodoreServiceOutboxMaxBackoff         = 1 * time.Hour
	commodoreServiceOutboxBatchSize          = 32
	commodoreServiceOutboxPollPeriod         = 30 * time.Second
	commodoreServiceOutboxLease              = 60 * time.Second
	commodoreServiceOutboxAlertAfterAttempts = 12
)

func commodoreServiceOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        commodoreServiceOutboxBaseBackoff,
		MaxBackoff:         commodoreServiceOutboxMaxBackoff,
		BatchSize:          commodoreServiceOutboxBatchSize,
		PollPeriod:         commodoreServiceOutboxPollPeriod,
		Lease:              commodoreServiceOutboxLease,
		AlertAfterAttempts: commodoreServiceOutboxAlertAfterAttempts,
	}
}

type commodoreServiceOutboxRow struct {
	id        string
	payload   []byte
	attempts  int
	createdAt time.Time
}

// EnqueueServiceEventTx writes the outbox row inside the caller's
// transaction. A failed INSERT rolls back with the caller's tx.
func (s *CommodoreServer) EnqueueServiceEventTx(
	ctx context.Context,
	exec interface {
		QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	},
	event *pb.ServiceEvent,
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
		INSERT INTO commodore.service_event_outbox
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
func (s *CommodoreServer) enqueueServiceEvent(ctx context.Context, event *pb.ServiceEvent) {
	if s.db == nil || event == nil || event.GetTenantId() == "" {
		return
	}
	if _, err := s.EnqueueServiceEventTx(ctx, s.db, event); err != nil {
		s.logger.WithError(err).WithField("event_type", event.GetEventType()).
			Warn("Failed to enqueue commodore service event outbox row")
	}
}

type commodoreServiceOutboxStore struct {
	server *CommodoreServer
}

func (st *commodoreServiceOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[commodoreServiceOutboxRow], error) {
	rows, err := st.server.claimCommodoreServiceOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[commodoreServiceOutboxRow], 0, len(rows))
	for _, r := range rows {
		claims = append(claims, outbox.Claim[commodoreServiceOutboxRow]{
			ID:       r.id,
			Attempts: r.attempts,
			Payload:  r,
		})
	}
	return claims, nil
}

func (st *commodoreServiceOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	st.server.markCommodoreServiceOutboxCompleted(ctx, id)
	return nil
}

func (st *commodoreServiceOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration) error {
	st.server.recordCommodoreServiceOutboxFailure(ctx, id, attempts, cause)
	return nil
}

type commodoreServiceOutboxDispatcher struct {
	server *CommodoreServer
}

func (d *commodoreServiceOutboxDispatcher) Dispatch(ctx context.Context, row commodoreServiceOutboxRow) ([]string, error) {
	return d.server.dispatchCommodoreServiceOutboxRow(ctx, row)
}

// runServiceEventOutboxWorker drains commodore.service_event_outbox to
// Decklog. Safe to run on every Commodore replica — SKIP LOCKED + lease
// makes work distributable.
func (s *CommodoreServer) runServiceEventOutboxWorker(ctx context.Context) {
	if s.decklogClient == nil {
		s.logger.Info("commodore service event outbox worker disabled: no decklog client")
		return
	}
	cfg := commodoreServiceOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[commodoreServiceOutboxRow]{
		Config:     cfg,
		Store:      &commodoreServiceOutboxStore{server: s},
		Dispatcher: &commodoreServiceOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "commodore service event",
	}
	worker.Run(ctx)
}

func (s *CommodoreServer) claimCommodoreServiceOutboxBatch(ctx context.Context) ([]commodoreServiceOutboxRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after Commit

	out, err := func() ([]commodoreServiceOutboxRow, error) {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT id::text, payload::text, attempts, created_at
			FROM commodore.service_event_outbox
			WHERE completed_at IS NULL
			  AND (claimed_at IS NULL OR claimed_at < NOW() - $1::interval)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		`, fmt.Sprintf("%d seconds", int(commodoreServiceOutboxLease.Seconds())), commodoreServiceOutboxBatchSize)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()

		batch := make([]commodoreServiceOutboxRow, 0, commodoreServiceOutboxBatchSize)
		for rows.Next() {
			var (
				r           commodoreServiceOutboxRow
				payloadText string
			)
			if scanErr := rows.Scan(&r.id, &payloadText, &r.attempts, &r.createdAt); scanErr != nil {
				return nil, scanErr
			}
			r.payload = []byte(payloadText)
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
				UPDATE commodore.service_event_outbox
				SET claimed_at = NOW()
				WHERE id = ANY($1::uuid[])
			`, commodoreServiceOutboxIDArray(ids)); uerr != nil {
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

func (s *CommodoreServer) markCommodoreServiceOutboxCompleted(ctx context.Context, id string) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.service_event_outbox
		SET completed_at = NOW(), last_error = NULL
		WHERE id = $1::uuid
	`, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark commodore service event outbox row completed")
	}
}

func (s *CommodoreServer) recordCommodoreServiceOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE commodore.service_event_outbox
		SET attempts = $2, last_error = $3, claimed_at = NULL
		WHERE id = $1::uuid
	`, id, attempts, msg); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to record commodore service event outbox failure")
	}
	if attempts >= commodoreServiceOutboxAlertAfterAttempts {
		s.logger.WithFields(logging.Fields{
			"outbox_id": id,
			"attempts":  attempts,
			"cause":     msg,
		}).Error("Commodore service event outbox row failing repeatedly — Decklog reachability degraded")
	}
}

func (s *CommodoreServer) dispatchCommodoreServiceOutboxRow(_ context.Context, row commodoreServiceOutboxRow) ([]string, error) {
	if s.decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	event := &pb.ServiceEvent{}
	if err := protojson.Unmarshal(row.payload, event); err != nil {
		return nil, fmt.Errorf("unmarshal service event payload: %w", err)
	}
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}

func commodoreServiceOutboxIDArray(ids []string) string {
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
