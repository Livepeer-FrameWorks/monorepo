package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pkgoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Relay timing. Domain events feed webhooks and realtime delivery, so the poll
// period is short; a claim that returns rows is followed immediately by the
// next claim until the outbox is drained.
const (
	batchSize          = 100
	pollPeriod         = 2 * time.Second
	lease              = 60 * time.Second
	publishTimeout     = 15 * time.Second
	alertAfterAttempts = 12
	maxErrorLength     = 1000
	// Completed rows are kept for a week for inspection, then deleted in
	// bounded batches.
	completedRetention = 7 * 24 * time.Hour
	pruneBatch         = 1000
	pruneEvery         = time.Hour
)

var backoff = pkgoutbox.Config{BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute}

// Execer is satisfied by *sql.Tx, *sql.DB, and sqlc DBTX values.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Enqueue inserts ev into schema's outbox through exec, which must be the
// transaction that commits the state change ev describes, so the event exists
// exactly when the change does.
func Enqueue(ctx context.Context, exec Execer, schema string, ev events.Event) error {
	if err := validateSchema(schema); err != nil {
		return err
	}
	spec, ok := events.Lookup(ev.Type)
	if !ok {
		return fmt.Errorf("%w: %q", events.ErrUnknownType, ev.Type)
	}
	if _, _, err := events.Validate(ev.Envelope()); err != nil {
		return fmt.Errorf("outbox: %w", err)
	}
	scope, tenant := "tenant", sql.NullString{String: ev.TenantID, Valid: true}
	if spec.Scope == eventspb.Scope_SCOPE_PLATFORM {
		scope, tenant = "platform", sql.NullString{}
	}
	tokenHash := ""
	if ev.Actor.TokenHash != 0 {
		tokenHash = strconv.FormatUint(ev.Actor.TokenHash, 10)
	}
	_, err := exec.ExecContext(ctx, `INSERT INTO `+schema+`.domain_event_outbox (
		event_id, event_type, source, aggregate_type, aggregate_id, aggregate_version,
		scope, tenant_id, actor_auth_type, actor_user_id, actor_token_hash, occurred_at, payload
	) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8::uuid, $9, $10, $11, $12, $13)`,
		ev.ID, ev.Type, ev.Source, spec.Aggregate, ev.AggregateID, ev.AggregateVersion,
		scope, tenant, ev.Actor.AuthType, ev.Actor.UserID, tokenHash, ev.Time.UTC(), ev.Data)
	if err != nil {
		return fmt.Errorf("outbox: insert %s: %w", ev.Type, err)
	}
	return nil
}

// Publisher delivers a batch to Decklog. It returns nil only after Decklog
// acknowledged every event, which Decklog does after Kafka acknowledged every
// record.
type Publisher interface {
	PublishDomainEvents(ctx context.Context, batch *eventspb.DomainEventBatch) error
}

// Relay drains one service's outbox to a Publisher. Every replica of the
// service may run one: claims are leased with a per-claim token and use SKIP
// LOCKED, and settlement only applies while the row still carries the claim's
// token.
type Relay struct {
	db        *sql.DB
	schema    string
	publisher Publisher
	logger    logging.Logger
	wake      chan struct{}
}

// NewRelay returns a relay for schema's domain_event_outbox.
func NewRelay(db *sql.DB, schema string, publisher Publisher, logger logging.Logger) (*Relay, error) {
	if err := validateSchema(schema); err != nil {
		return nil, err
	}
	if db == nil || publisher == nil || logger == nil {
		return nil, errors.New("outbox: relay needs a database, a publisher, and a logger")
	}
	return &Relay{db: db, schema: schema, publisher: publisher, logger: logger, wake: make(chan struct{}, 1)}, nil
}

// Notify wakes the relay after a producer committed new rows, so delivery does
// not wait for the next poll.
func (r *Relay) Notify() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Run delivers rows until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(pollPeriod)
	defer ticker.Stop()
	nextPrune := time.Now()
	for {
		for {
			n, err := r.DispatchOnce(ctx)
			if err != nil {
				r.logger.WithError(err).WithField("schema", r.schema).Warn("Domain event outbox dispatch failed")
				break
			}
			if n == 0 || ctx.Err() != nil {
				break
			}
		}
		if time.Now().After(nextPrune) {
			if _, err := r.PruneCompleted(ctx); err != nil {
				r.logger.WithError(err).WithField("schema", r.schema).Warn("Domain event outbox prune failed")
			}
			nextPrune = time.Now().Add(pruneEvery)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
	}
}

type claimedRow struct {
	env        *eventspb.DomainEvent
	attempts   int
	enqueuedAt time.Time
}

// DispatchOnce claims up to one batch, publishes it, and settles every
// claimed row. It returns how many rows it claimed.
func (r *Relay) DispatchOnce(ctx context.Context) (int, error) {
	// Include claim latency and per-row fallback in one budget. No publish may
	// still be running when another replica can reclaim these rows.
	ctx, cancel := context.WithTimeout(ctx, lease-5*time.Second)
	defer cancel()
	token := uuid.Must(uuid.NewV7()).String()
	var rows []claimedRow
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var claimErr error
		rows, claimErr = r.claim(ctx, token)
		return claimErr
	})
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	batch := &eventspb.DomainEventBatch{Events: make([]*eventspb.DomainEvent, len(rows))}
	for i, row := range rows {
		batch.Events[i] = row.env
	}
	if pubErr := r.publish(ctx, batch); pubErr == nil {
		r.complete(ctx, token, rows)
		return len(rows), nil
	} else if len(rows) == 1 {
		r.fail(ctx, token, rows[0], pubErr)
		return 1, nil
	}
	// One rejected event fails the whole batch; publishing the rows one by one
	// keeps it from holding back the other aggregates in the batch.
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return len(rows), err
		}
		single := &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{row.env}}
		if pubErr := r.publish(ctx, single); pubErr != nil {
			r.fail(ctx, token, row, pubErr)
			continue
		}
		r.complete(ctx, token, []claimedRow{row})
	}
	return len(rows), nil
}

func (r *Relay) publish(ctx context.Context, batch *eventspb.DomainEventBatch) error {
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	return r.publisher.PublishDomainEvents(pctx, batch)
}

// claim leases the oldest due rows whose aggregate has no older incomplete
// row. A second row of an aggregate therefore waits until the first one is
// completed, even while the first is leased by another replica or backing off.
func (r *Relay) claim(ctx context.Context, token string) ([]claimedRow, error) {
	t := r.schema + ".domain_event_outbox"
	query := `UPDATE ` + t + ` AS o
SET claimed_at = now(), lease_token = $1::uuid
WHERE o.event_id IN (
    SELECT c.event_id
    FROM ` + t + ` AS c
    WHERE c.completed_at IS NULL
      AND c.next_attempt_at <= now()
      AND (c.claimed_at IS NULL OR c.claimed_at < now() - ($2::bigint * interval '1 millisecond'))
      AND NOT EXISTS (
          SELECT 1 FROM ` + t + ` AS p
          WHERE p.aggregate_type = c.aggregate_type
            AND p.aggregate_id = c.aggregate_id
            AND p.completed_at IS NULL
            AND (p.enqueued_at, p.event_id) < (c.enqueued_at, c.event_id))
    ORDER BY c.enqueued_at, c.event_id
    LIMIT $3
    FOR UPDATE SKIP LOCKED)
RETURNING o.event_id::text, o.event_type, o.source, o.aggregate_id, o.aggregate_version,
    COALESCE(o.tenant_id::text, ''), o.actor_auth_type, o.actor_user_id, o.actor_token_hash,
    o.occurred_at, o.payload, o.attempts, o.enqueued_at`
	res, err := r.db.QueryContext(ctx, query, token, lease.Milliseconds(), batchSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Close() }()
	var rows []claimedRow
	for res.Next() {
		var (
			row       claimedRow
			env       eventspb.DomainEvent
			actor     eventspb.Actor
			tokenHash string
			occurred  time.Time
		)
		if scanErr := res.Scan(&env.Id, &env.Type, &env.Source, &env.AggregateId, &env.AggregateVersion,
			&env.TenantId, &actor.AuthType, &actor.UserId, &tokenHash,
			&occurred, &env.Data, &row.attempts, &row.enqueuedAt); scanErr != nil {
			return nil, scanErr
		}
		if tokenHash != "" {
			if actor.TokenHash, err = strconv.ParseUint(tokenHash, 10, 64); err != nil {
				return nil, fmt.Errorf("row %s: actor_token_hash: %w", env.Id, err)
			}
		}
		env.Time = timestamppb.New(occurred)
		env.Actor = &actor
		row.env = &env
		rows = append(rows, row)
	}
	if err := res.Err(); err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].enqueuedAt.Equal(rows[j].enqueuedAt) {
			return rows[i].enqueuedAt.Before(rows[j].enqueuedAt)
		}
		return rows[i].env.Id < rows[j].env.Id
	})
	return rows, nil
}

func (r *Relay) complete(ctx context.Context, token string, rows []claimedRow) {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.env.Id
	}
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		_, execErr := r.db.ExecContext(ctx, `UPDATE `+r.schema+`.domain_event_outbox
SET completed_at = now(), lease_token = NULL, last_error = NULL
WHERE event_id = ANY($1::uuid[]) AND lease_token = $2::uuid AND completed_at IS NULL`, pq.Array(ids), token)
		return execErr
	})
	if err != nil {
		// The rows stay leased; after the lease they are claimed and
		// published again with the same event IDs.
		r.logger.WithError(err).WithField("schema", r.schema).Warn("Domain event outbox completion failed")
	}
}

func (r *Relay) fail(ctx context.Context, token string, row claimedRow, cause error) {
	msg := cause.Error()
	if len(msg) > maxErrorLength {
		msg = msg[:maxErrorLength]
	}
	wait := pkgoutbox.ComputeBackoff(backoff, row.attempts)
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		_, execErr := r.db.ExecContext(ctx, `UPDATE `+r.schema+`.domain_event_outbox
SET attempts = attempts + 1, last_error = $3, claimed_at = NULL, lease_token = NULL,
    next_attempt_at = now() + ($4::bigint * interval '1 millisecond')
WHERE event_id = $1::uuid AND lease_token = $2::uuid AND completed_at IS NULL`,
			row.env.Id, token, msg, wait.Milliseconds())
		return execErr
	})
	fields := logging.Fields{
		"schema": r.schema, "event_id": row.env.Id, "event_type": row.env.Type,
		"attempts": row.attempts + 1, "cause": msg,
	}
	if err != nil {
		r.logger.WithError(err).WithFields(fields).Warn("Domain event outbox failure could not be recorded")
		return
	}
	if row.attempts+1 >= alertAfterAttempts {
		r.logger.WithFields(fields).Error("Domain event outbox row keeps failing; its aggregate's later events are held back until it is delivered")
	}
}

// PruneCompleted deletes up to one batch of rows completed longer than the
// retention ago and returns how many it deleted.
func (r *Relay) PruneCompleted(ctx context.Context) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM `+r.schema+`.domain_event_outbox
WHERE event_id IN (
    SELECT event_id FROM `+r.schema+`.domain_event_outbox
    WHERE completed_at IS NOT NULL AND completed_at < now() - ($1::bigint * interval '1 millisecond')
    LIMIT $2)`, completedRetention.Milliseconds(), pruneBatch)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
