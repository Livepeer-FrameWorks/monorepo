package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"frameworks/api_tenants/internal/serviceeventoutbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"

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
	id         string
	eventID    string
	payload    []byte
	attempts   int
	createdAt  time.Time
	leaseToken string
}

// EnqueueServiceEventTx writes the service event, and the domain event its
// type maps to, inside the caller's transaction, so both become durable
// exactly when the state mutation that justifies them commits. A failed
// INSERT rolls back with the caller's tx.
func (s *QuartermasterServer) EnqueueServiceEventTx(
	ctx context.Context,
	exec quartermasterdb.DBTX,
	event *ipcpb.ServiceEvent,
) (string, error) {
	if event == nil {
		return "", errors.New("nil service event")
	}
	domain, err := serviceeventoutbox.DomainFor(event)
	if err != nil {
		return "", err
	}
	return s.enqueueServiceEventWithDomainTx(ctx, exec, event, domain)
}

// enqueueServiceEventWithDomainTx writes the service event and domain (nil
// for none) inside the caller's transaction, attributed to the gRPC caller.
func (s *QuartermasterServer) enqueueServiceEventWithDomainTx(
	ctx context.Context,
	exec quartermasterdb.DBTX,
	event *ipcpb.ServiceEvent,
	domain *serviceeventoutbox.Domain,
) (string, error) {
	var actor events.Actor
	if domain != nil {
		if s.eventTokenHasher == nil {
			return "", errors.New("domain event actor attribution needs the usage hash secret")
		}
		actor = events.ActorFromContext(ctx, s.eventTokenHasher)
	}
	return serviceeventoutbox.EnqueueWithDomain(ctx, exec, event, domain, actor)
}

// requestActor is the principal a service caller named on its request, or,
// when it named none, the gRPC caller itself.
func (s *QuartermasterServer) requestActor(ctx context.Context, named *commonpb.RequestActor) (events.Actor, error) {
	if actor, ok := events.ActorFromRequest(named); ok {
		return actor, nil
	}
	if s.eventTokenHasher == nil {
		return events.Actor{}, errors.New("domain event actor attribution needs the usage hash secret")
	}
	return events.ActorFromContext(ctx, s.eventTokenHasher), nil
}

// emitClusterEventAsTx writes a cluster service event and its domain event
// (derived from the service event when domain is nil) inside the caller's
// transaction, both attributed to actor.
func (s *QuartermasterServer) emitClusterEventAsTx(ctx context.Context, tx *sql.Tx, actor events.Actor, event *ipcpb.ServiceEvent, domain *serviceeventoutbox.Domain) error {
	if ctxkeys.IsDemoMode(ctx) {
		return nil
	}
	if tx == nil {
		return errors.New("cluster event requires the mutation's transaction")
	}
	if domain == nil {
		derived, err := serviceeventoutbox.DomainFor(event)
		if err != nil {
			return err
		}
		domain = derived
	}
	event.UserId = actor.UserID
	_, err := serviceeventoutbox.EnqueueWithDomain(ctx, tx, event, domain, actor)
	return err
}

func serviceEventScope(event *ipcpb.ServiceEvent) (string, error) {
	return serviceeventoutbox.Scope(event)
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
			ID:         r.id,
			Attempts:   r.attempts,
			Payload:    r,
			LeaseToken: r.leaseToken,
		})
	}
	return claims, nil
}

func (st *qmOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	return st.MarkCompletedToken(ctx, id, "")
}

func (st *qmOutboxStore) RecordFailure(ctx context.Context, id string, attempts int, failedTargets []string, cause error, backoff time.Duration) error {
	return st.RecordFailureToken(ctx, id, attempts, failedTargets, cause, backoff, "")
}

// MarkCompletedToken and RecordFailureToken settle only while the row still
// carries the claim's lease token, so a worker whose lease lapsed cannot settle
// a row a peer has re-claimed.
func (st *qmOutboxStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	st.server.markQMOutboxCompleted(ctx, id, leaseToken)
	return nil
}

func (st *qmOutboxStore) RecordFailureToken(ctx context.Context, id string, attempts int, _ []string, cause error, _ time.Duration, leaseToken string) error {
	st.server.recordQMOutboxFailure(ctx, id, attempts, cause, leaseToken)
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
		queries := quartermasterdb.New(tx)
		rows, qerr := queries.ClaimServiceEventOutboxBatch(ctx, quartermasterdb.ClaimServiceEventOutboxBatchParams{
			LeaseInterval: fmt.Sprintf("%d seconds", int(qmOutboxLease.Seconds())), BatchSize: qmOutboxBatchSize,
		})
		if qerr != nil {
			return qerr
		}

		leaseToken := uuid.NewString()
		batch := make([]qmOutboxRow, 0, qmOutboxBatchSize)
		for _, row := range rows {
			batch = append(batch, qmOutboxRow{
				id: row.ID, eventID: row.EventID, payload: []byte(row.Payload), attempts: int(row.Attempts), createdAt: row.CreatedAt, leaseToken: leaseToken,
			})
		}
		if len(batch) > 0 {
			ids := make([]string, 0, len(batch))
			for _, r := range batch {
				ids = append(ids, r.id)
			}
			if uerr := queries.MarkServiceEventOutboxClaimed(ctx, quartermasterdb.MarkServiceEventOutboxClaimedParams{
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

func (s *QuartermasterServer) markQMOutboxCompleted(ctx context.Context, id, leaseToken string) {
	if err := quartermasterdb.New(s.db).CompleteServiceEventOutbox(ctx, quartermasterdb.CompleteServiceEventOutboxParams{
		ID: id, LeaseToken: leaseToken,
	}); err != nil {
		s.logger.WithError(err).WithField("outbox_id", id).
			Warn("Failed to mark quartermaster service event outbox row completed")
	}
}

func (s *QuartermasterServer) recordQMOutboxFailure(ctx context.Context, id string, attempts int, cause error, leaseToken string) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := quartermasterdb.New(s.db).FailServiceEventOutbox(ctx, quartermasterdb.FailServiceEventOutboxParams{
		ID: id, Attempts: int32(attempts), LastError: msg, LeaseToken: leaseToken,
	}); err != nil {
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

// serviceEventForDispatch decodes row's event with the ID every dispatch of
// the row sends: the stored event_id, else the ID in the payload, else the
// row ID for rows written before event_id existed. The ID never changes
// between attempts, so consumers can drop a redelivery after a lost
// acknowledgement.
func serviceEventForDispatch(row qmOutboxRow) (*ipcpb.ServiceEvent, error) {
	event := &ipcpb.ServiceEvent{}
	if err := protojson.Unmarshal(row.payload, event); err != nil {
		return nil, fmt.Errorf("unmarshal service event payload: %w", err)
	}
	switch {
	case row.eventID != "":
		event.EventId = row.eventID
	case event.GetEventId() == "":
		event.EventId = row.id
	}
	return event, nil
}

func (s *QuartermasterServer) dispatchQMOutboxRow(ctx context.Context, row qmOutboxRow) ([]string, error) {
	if s.decklogClient == nil {
		return nil, errors.New("decklog client not configured")
	}
	event, err := serviceEventForDispatch(row)
	if err != nil {
		return nil, err
	}
	_ = ctx // decklog client manages its own context
	if err := s.decklogClient.SendServiceEvent(event); err != nil {
		return []string{"decklog"}, err
	}
	return nil, nil
}
