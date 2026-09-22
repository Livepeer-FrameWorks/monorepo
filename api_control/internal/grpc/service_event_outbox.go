package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

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

	// commodoreEventSource is the CloudEvents source of every event Commodore
	// emits, and DomainEventSchema holds its domain_event_outbox.
	commodoreEventSource = "commodore"
	DomainEventSchema    = "commodore"
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
	id         string
	eventID    string
	payload    []byte
	attempts   int
	createdAt  time.Time
	leaseToken string
}

// domainEvent is the typed domain event that a legacy service event is
// dual-written with. aggregateID identifies the instance of the message's
// registered aggregate, for example the stream ID for stream events.
type domainEvent struct {
	msg         proto.Message
	aggregateID string
}

// enqueueEventTx writes the service event, and its domain event when there is
// one, through exec. exec must be the transaction that commits the state
// change the event describes, so the event exists exactly when the change
// does; a failed insert fails that transaction. Both rows carry one event ID,
// so a consumer reading the legacy and the domain stream sees one fact.
func (s *CommodoreServer) enqueueEventTx(ctx context.Context, exec commodoredb.DBTX, legacy *ipcpb.ServiceEvent, domain *domainEvent) error {
	if legacy == nil {
		return errors.New("nil service event")
	}
	if ctxkeys.IsDemoMode(ctx) {
		return nil
	}
	if legacy.GetTenantId() == "" {
		return fmt.Errorf("service event %s has no tenant", legacy.GetEventType())
	}
	occurredAt := time.Now().UTC()
	if ts := legacy.GetTimestamp(); ts.IsValid() {
		occurredAt = ts.AsTime()
	}
	if domain == nil {
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("generate event id: %w", err)
		}
		legacy.EventId = id.String()
	} else {
		if s.tokenHasher == nil {
			return errors.New("domain event actor hasher is not configured")
		}
		ev, err := events.New(commodoreEventSource, legacy.GetTenantId(), domain.aggregateID, domain.msg,
			events.WithActor(events.ActorFromContext(ctx, s.tokenHasher)), events.WithTime(occurredAt))
		if err != nil {
			return fmt.Errorf("build domain event for %s: %w", legacy.GetEventType(), err)
		}
		if err := eventoutbox.Enqueue(ctx, exec, DomainEventSchema, ev); err != nil {
			return err
		}
		legacy.EventId = ev.ID
	}
	_, err := s.EnqueueServiceEventTx(ctx, exec, legacy)
	return err
}

// enqueueEventOwnTx records an event whose only durable effect is the event
// itself, such as a failed sign-in, in its own transaction. The caller fails
// the operation when it returns an error.
// requestActor names the caller of the current RPC on a request Commodore
// sends with its own service credentials, so the called service attributes
// the domain event it records to that caller. The token hash is computed here,
// with the shared usage hash secret, so the raw API token ID never leaves
// Commodore. Without a hasher (never in a running Commodore, which refuses to
// start without one) the request names no principal.
func (s *CommodoreServer) requestActor(ctx context.Context) *commonpb.RequestActor {
	if s.tokenHasher == nil {
		return nil
	}
	return events.RequestActor(events.ActorFromContext(ctx, s.tokenHasher))
}

func (s *CommodoreServer) enqueueEventOwnTx(ctx context.Context, legacy *ipcpb.ServiceEvent, domain *domainEvent) error {
	if ctxkeys.IsDemoMode(ctx) {
		return nil
	}
	return s.withEventTx(ctx, func(tx *sql.Tx) error {
		return s.enqueueEventTx(ctx, tx, legacy, domain)
	})
}

// withEventTx runs fn in a transaction and commits it when fn succeeds.
func (s *CommodoreServer) withEventTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if s.db == nil {
		return errors.New("database not configured")
	}
	return database.WithRetryablePostgresTx(ctx, s.db, nil, fn)
}

// EnqueueServiceEventTx writes the legacy outbox row inside the caller's
// transaction; event.EventId is stored as the row's event_id. A failed INSERT
// rolls back with the caller's tx.
func (s *CommodoreServer) EnqueueServiceEventTx(
	ctx context.Context,
	exec commodoredb.DBTX,
	event *ipcpb.ServiceEvent,
) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	if event.GetEventId() == "" {
		id, err := uuid.NewV7()
		if err != nil {
			return "", fmt.Errorf("generate event id: %w", err)
		}
		event.EventId = id.String()
	}
	payload, err := protojson.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal service event: %w", err)
	}
	id, err := commodoredb.New(exec).EnqueueServiceEvent(ctx, commodoredb.EnqueueServiceEventParams{
		EventID: event.GetEventId(), EventType: event.GetEventType(), TenantID: event.GetTenantId(), UserID: event.GetUserId(),
		ResourceType: event.GetResourceType(), ResourceID: event.GetResourceId(), Payload: string(payload),
	})
	if err != nil {
		return "", fmt.Errorf("insert service event outbox row: %w", err)
	}
	return id, nil
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
			ID:         r.id,
			Attempts:   r.attempts,
			Payload:    r,
			LeaseToken: r.leaseToken,
		})
	}
	return claims, nil
}

func (st *commodoreServiceOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	return st.MarkCompletedToken(ctx, id, "")
}

func (st *commodoreServiceOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, failedTargets []string, cause error, backoff time.Duration) error {
	return st.RecordFailureToken(ctx, id, attempts, failedTargets, cause, backoff, "")
}

// MarkCompletedToken and RecordFailureToken settle only while the row still
// carries the claim's lease token, so a worker whose lease lapsed cannot
// settle a row a peer has re-claimed.
func (st *commodoreServiceOutboxStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	st.server.markCommodoreServiceOutboxCompleted(ctx, id, leaseToken)
	return nil
}

func (st *commodoreServiceOutboxStore) RecordFailureToken(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration, leaseToken string) error {
	st.server.recordCommodoreServiceOutboxFailure(ctx, id, attempts, cause, leaseToken)
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

		leaseToken := uuid.NewString()
		batch := make([]commodoreServiceOutboxRow, 0, commodoreServiceOutboxBatchSize)
		for _, row := range rows {
			batch = append(batch, commodoreServiceOutboxRow{
				id: row.ID, eventID: row.EventID, payload: []byte(row.Payload), attempts: int(row.Attempts),
				createdAt: row.CreatedAt, leaseToken: leaseToken,
			})
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if uerr := queries.MarkServiceEventOutboxClaimed(ctx, commodoredb.MarkServiceEventOutboxClaimedParams{
				LeaseToken: leaseToken, Ids: ids,
			}); uerr != nil {
				return uerr
			}
		}
		out = batch
		return nil
	})
	return out, err
}

func (s *CommodoreServer) markCommodoreServiceOutboxCompleted(ctx context.Context, id, leaseToken string) {
	if err := commodoredb.New(s.db).CompleteServiceEventOutbox(ctx, commodoredb.CompleteServiceEventOutboxParams{
		ID: id, LeaseToken: leaseToken,
	}); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark commodore service event outbox row completed")
	}
}

func (s *CommodoreServer) recordCommodoreServiceOutboxFailure(ctx context.Context, id string, attempts int, cause error, leaseToken string) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := commodoredb.New(s.db).FailServiceEventOutbox(ctx, commodoredb.FailServiceEventOutboxParams{
		Attempts: int32(attempts), LastError: sql.NullString{String: msg, Valid: true}, ID: id, LeaseToken: leaseToken,
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

// dispatchCommodoreServiceOutboxRow sends the row under its stored event ID,
// so every attempt, including a redispatch after a lost acknowledgement,
// carries the same ID.
func (s *CommodoreServer) dispatchCommodoreServiceOutboxRow(_ context.Context, row commodoreServiceOutboxRow) ([]string, error) {
	if s.decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	event, err := decodeCommodoreServiceOutboxRow(row)
	if err != nil {
		return nil, err
	}
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}

func decodeCommodoreServiceOutboxRow(row commodoreServiceOutboxRow) (*ipcpb.ServiceEvent, error) {
	event := &ipcpb.ServiceEvent{}
	if err := protojson.Unmarshal(row.payload, event); err != nil {
		return nil, fmt.Errorf("unmarshal service event payload: %w", err)
	}
	if row.eventID == "" {
		return nil, fmt.Errorf("service event outbox row %s has no event id", row.id)
	}
	event.EventId = row.eventID
	return event, nil
}
