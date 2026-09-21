package ledger

import (
	"context"
	"errors"
	"time"

	pkgoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"

	"github.com/google/uuid"
)

// Notification is an email to send to a tenant's billing contact.
type Notification struct {
	ID          string
	TenantID    string
	EndpointID  string
	Kind        string
	EndpointURL string
}

// NotificationStore is the pkg/outbox store of webhook_notification_outbox.
// Claims carry a lease token and settlement is fenced on it.
type NotificationStore struct {
	Store *Store
}

var (
	_ pkgoutbox.Store[Notification] = (*NotificationStore)(nil)
	_ pkgoutbox.TokenFencedStore    = (*NotificationStore)(nil)
)

// errUnfencedSettlement is returned by the plain settlement methods, which the
// worker never calls because every claim carries a lease token.
var errUnfencedSettlement = errors.New("notification outbox settlement requires a lease token")

// ClaimBatch leases up to batchSize due notifications.
func (n *NotificationStore) ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) ([]pkgoutbox.Claim[Notification], error) {
	token := uuid.Must(uuid.NewV7()).String()
	rows, err := n.Store.DB.QueryContext(ctx, `
		WITH picked AS (
			SELECT tenant_id, id FROM bosun.webhook_notification_outbox
			WHERE completed_at IS NULL AND next_attempt_at <= now()
			ORDER BY next_attempt_at, id
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE bosun.webhook_notification_outbox o
		SET lease_token = $2, claimed_at = now(), next_attempt_at = now() + make_interval(secs => $3)
		FROM picked
		WHERE o.tenant_id = picked.tenant_id AND o.id = picked.id
		RETURNING o.id, o.tenant_id, o.endpoint_id, o.kind, o.endpoint_url, o.attempts`,
		batchSize, token, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []pkgoutbox.Claim[Notification]
	for rows.Next() {
		var c pkgoutbox.Claim[Notification]
		if err := rows.Scan(&c.Payload.ID, &c.Payload.TenantID, &c.Payload.EndpointID, &c.Payload.Kind, &c.Payload.EndpointURL, &c.Attempts); err != nil {
			return nil, err
		}
		c.ID = c.Payload.ID
		c.LeaseToken = token
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkCompleted is not used; see MarkCompletedToken.
func (n *NotificationStore) MarkCompleted(context.Context, string) error {
	return errUnfencedSettlement
}

// RecordFailure is not used; see RecordFailureToken.
func (n *NotificationStore) RecordFailure(context.Context, string, int, []string, error, time.Duration) error {
	return errUnfencedSettlement
}

// MarkCompletedToken completes a notification the token still owns.
func (n *NotificationStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	_, err := n.Store.DB.ExecContext(ctx, `
		UPDATE bosun.webhook_notification_outbox
		SET completed_at = now(), lease_token = NULL, last_error = NULL
		WHERE id = $1 AND lease_token = $2 AND completed_at IS NULL`, id, leaseToken)
	return err
}

// RecordFailureToken schedules a retry of a notification the token still
// owns.
func (n *NotificationStore) RecordFailureToken(ctx context.Context, id string, currentAttempts int, _ []string, cause error, backoff time.Duration, leaseToken string) error {
	message := ""
	if cause != nil {
		message = truncateExcerpt(cause.Error())
	}
	_, err := n.Store.DB.ExecContext(ctx, `
		UPDATE bosun.webhook_notification_outbox
		SET attempts = $3, last_error = $4, lease_token = NULL, next_attempt_at = now() + make_interval(secs => $5)
		WHERE id = $1 AND lease_token = $2 AND completed_at IS NULL`,
		id, leaseToken, currentAttempts+1, message, backoff.Seconds())
	return err
}
