package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_incidents/internal/database/lookoutdb"
	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"

	"github.com/google/uuid"
)

// maxDeliveryAttempts bounds how often one operator-channel row (email, Slack,
// Discord) is attempted. With Lookout's delivery backoff (5s doubling, capped at
// 10 minutes) the final attempt runs about two hours after the first, which
// outlasts a transient outage; a destination still rejecting the delivery by
// then, such as a webhook answering 4xx, settles the row as failed instead of
// retrying forever. Kafka rows are exempt in FailDelivery.
const maxDeliveryAttempts = 20

// Delivery is one claimed outbox row.
type Delivery struct {
	OutboxID   string
	TenantID   string
	IncidentID string
	EventID    string
	Channel    string
	Payload    json.RawMessage
}

// ChannelChecker reports whether an operator channel is configured.
type ChannelChecker interface {
	Enabled(channel string) bool
}

var (
	errLeaseLost          = errors.New("lookout delivery lease no longer owned")
	errLeaseTokenRequired = errors.New("lookout delivery settlement requires a lease token")
)

// Store is the token-fenced outbox store for lookout.notification_outbox.
type Store struct {
	DB       *sql.DB
	Channels ChannelChecker
	Metrics  *incidents.Metrics
	Logger   logging.Logger
}

var (
	_ outbox.Store[Delivery]  = (*Store)(nil)
	_ outbox.TokenFencedStore = (*Store)(nil)
)

// ClaimBatch leases due rows under a fresh token each.
func (s *Store) ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) (claims []outbox.Claim[Delivery], err error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	q := lookoutdb.New(tx)
	candidates, err := q.ClaimDeliveryCandidates(ctx, lookoutdb.ClaimDeliveryCandidatesParams{
		LeaseMilliseconds: lease.Milliseconds(),
		BatchSize:         int32(batchSize),
	})
	if err != nil {
		return nil, fmt.Errorf("select delivery outbox: %w", err)
	}
	claims = make([]outbox.Claim[Delivery], 0, len(candidates))
	for _, candidate := range candidates {
		token := uuid.NewString()
		affected, leaseErr := q.LeaseDelivery(ctx, lookoutdb.LeaseDeliveryParams{
			LeaseToken: token,
			ID:         candidate.ID,
			TenantID:   candidate.TenantID,
		})
		if leaseErr != nil {
			return nil, fmt.Errorf("lease delivery outbox: %w", leaseErr)
		}
		if affected != 1 {
			return nil, errors.New("lease delivery outbox: selected row disappeared")
		}
		claims = append(claims, outbox.Claim[Delivery]{
			ID:         claimID(candidate.TenantID.String, candidate.ID),
			Attempts:   int(candidate.Attempts),
			LeaseToken: token,
			Payload: Delivery{
				OutboxID:   candidate.ID,
				TenantID:   candidate.TenantID.String,
				IncidentID: candidate.IncidentID,
				EventID:    candidate.EventID,
				Channel:    candidate.Channel,
				Payload:    candidate.Payload,
			},
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

// MarkCompleted is never used: every claim carries a lease token.
func (s *Store) MarkCompleted(context.Context, string) error {
	return errLeaseTokenRequired
}

// RecordFailure is never used: every claim carries a lease token.
func (s *Store) RecordFailure(context.Context, string, int, []string, error, time.Duration) error {
	return errLeaseTokenRequired
}

// MarkCompletedToken settles a delivered row and, for operator channels that
// are still configured, records a notified timeline event in the same
// transaction.
func (s *Store) MarkCompletedToken(ctx context.Context, id, leaseToken string) (err error) {
	tenantID, outboxID, err := parseClaimID(id)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	q := lookoutdb.New(tx)
	row, err := q.CompleteDelivery(ctx, lookoutdb.CompleteDeliveryParams{
		ID:         outboxID,
		TenantID:   nullTenant(tenantID),
		LeaseToken: leaseToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errLeaseLost
	}
	if err != nil {
		return fmt.Errorf("complete delivery: %w", err)
	}
	if row.Channel != incidents.ChannelKafka && (s.Channels == nil || s.Channels.Enabled(row.Channel)) {
		body, marshalErr := json.Marshal(map[string]string{"channel": row.Channel})
		if marshalErr != nil {
			return marshalErr
		}
		if _, err := q.InsertIncidentEvent(ctx, lookoutdb.InsertIncidentEventParams{
			IncidentID: row.IncidentID,
			Kind:       incidents.EventNotified,
			Body:       body,
		}); err != nil {
			return fmt.Errorf("record notified event: %w", err)
		}
	}
	return tx.Commit()
}

// RecordFailureToken records a failed attempt for a row the caller still
// leases. The attempt that reaches maxDeliveryAttempts settles the row as
// failed with its last error; otherwise the row is retried after backoff.
func (s *Store) RecordFailureToken(ctx context.Context, id string, _ int, _ []string, cause error, backoff time.Duration, leaseToken string) error {
	tenantID, outboxID, err := parseClaimID(id)
	if err != nil {
		return err
	}
	message := "delivery failed"
	if cause != nil {
		message = cause.Error()
	}
	row, err := lookoutdb.New(s.DB).FailDelivery(ctx, lookoutdb.FailDeliveryParams{
		BackoffMilliseconds: backoff.Milliseconds(),
		LastError:           sql.NullString{String: message, Valid: true},
		MaxAttempts:         maxDeliveryAttempts,
		ID:                  outboxID,
		TenantID:            nullTenant(tenantID),
		LeaseToken:          leaseToken,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errLeaseLost
	}
	if err != nil {
		return fmt.Errorf("record delivery failure: %w", err)
	}
	if row.Failed {
		s.Metrics.ObserveDelivery(row.Channel, "failed")
		if s.Logger != nil {
			s.Logger.WithField("channel", row.Channel).
				WithField("incident_id", row.IncidentID).
				WithField("attempts", row.Attempts).
				WithField("last_error", message).
				Error("Lookout delivery failed permanently after reaching its attempt limit")
		}
	}
	return nil
}

// claimID carries the row's tenant so settlement statements stay tenant-filtered;
// platform rows have an empty tenant segment.
func claimID(tenantID, id string) string {
	return tenantID + "/" + id
}

func parseClaimID(value string) (tenantID, id string, err error) {
	tenantID, id, ok := strings.Cut(value, "/")
	if !ok || id == "" {
		return "", "", fmt.Errorf("invalid delivery claim id %q", value)
	}
	return tenantID, id, nil
}

func nullTenant(tenantID string) sql.NullString {
	return sql.NullString{String: tenantID, Valid: tenantID != ""}
}
