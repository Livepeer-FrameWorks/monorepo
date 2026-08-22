package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

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
	exec commodoredb.DBTX,
	event *ipcpb.ServiceEvent,
) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	id, err := commodoredb.New(exec).EnqueueServiceEvent(ctx, commodoredb.EnqueueServiceEventParams{
		EventType: event.GetEventType(), TenantID: event.GetTenantId(), UserID: event.GetUserId(),
		ResourceType: event.GetResourceType(), ResourceID: event.GetResourceId(), Payload: string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("insert service event outbox row: %w", err)
	}
	return id, nil
}

// enqueueServiceEvent writes the outbox row in its own short transaction.
// Use EnqueueServiceEventTx when the caller already holds a transaction.
func (s *CommodoreServer) enqueueServiceEvent(ctx context.Context, event *ipcpb.ServiceEvent) {
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
	var out []commodoreServiceOutboxRow
	err := database.WithRetryablePostgresTxWithHook(ctx, s.db, nil, func(error, int) {
		s.recycleIdlePostgresConns()
	}, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		rows, qerr := queries.ClaimServiceEventOutboxBatch(ctx, commodoredb.ClaimServiceEventOutboxBatchParams{
			LeaseInterval: fmt.Sprintf("%d seconds", int(commodoreServiceOutboxLease.Seconds())),
			BatchSize:     commodoreServiceOutboxBatchSize,
		})
		if qerr != nil {
			return qerr
		}

		batch := make([]commodoreServiceOutboxRow, 0, commodoreServiceOutboxBatchSize)
		for _, row := range rows {
			batch = append(batch, commodoreServiceOutboxRow{
				id: row.ID, payload: []byte(row.Payload), attempts: int(row.Attempts), createdAt: row.CreatedAt,
			})
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if uerr := queries.MarkServiceEventOutboxClaimed(ctx, ids); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *CommodoreServer) markCommodoreServiceOutboxCompleted(ctx context.Context, id string) {
	if err := commodoredb.New(s.db).CompleteServiceEventOutbox(ctx, id); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark commodore service event outbox row completed")
	}
}

func (s *CommodoreServer) recordCommodoreServiceOutboxFailure(ctx context.Context, id string, attempts int, cause error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := commodoredb.New(s.db).FailServiceEventOutbox(ctx, commodoredb.FailServiceEventOutboxParams{
		Attempts: int32(attempts), LastError: sql.NullString{String: msg, Valid: true}, ID: id,
	}); err != nil {
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
	event := &ipcpb.ServiceEvent{}
	if err := protojson.Unmarshal(row.payload, event); err != nil {
		return nil, fmt.Errorf("unmarshal service event payload: %w", err)
	}
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}
