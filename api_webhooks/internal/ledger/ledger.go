// Package ledger is Bosun's PostgreSQL state: tenant webhook endpoints and
// their encrypted signing secrets, the public events received for delivery,
// the deliveries with their leases and attempts, and the notification and
// domain event outboxes. Every tenant-facing query filters by tenant_id.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
)

// Limits and schedules of the delivery ledger.
const (
	// MaxEndpointsPerTenant is enforced by the webhook_tenants check
	// constraint; it is repeated here for error messages.
	MaxEndpointsPerTenant = 10
	// Lease is how long a claimed delivery belongs to its worker.
	Lease = 30 * time.Second
	// MaxInFlightPerEndpoint caps the leased deliveries of one endpoint.
	MaxInFlightPerEndpoint = 5
	// AutoDisableFailures and AutoDisableAfter together decide when a failing
	// endpoint is disabled: at least this many consecutive failed attempts,
	// the first of them at least this long ago, with no success in between.
	AutoDisableFailures = 20
	AutoDisableAfter    = 5 * 24 * time.Hour
	// PreviousSecretValidity is how long a rotated-out secret keeps signing.
	PreviousSecretValidity = 24 * time.Hour
	// Retention is how long events, deliveries, and attempts are kept.
	Retention = 30 * 24 * time.Hour
	// TestInterval is the minimum time between test deliveries of an
	// endpoint.
	TestInterval = 10 * time.Second
	// ReplayRangeLimit is the most deliveries one range replay returns to
	// pending.
	ReplayRangeLimit = 1000
	// APIVersionV1 is the only public event package major.
	APIVersionV1 = "v1"
	// AllEventTypes subscribes an endpoint to every public event type.
	AllEventTypes = "*"
	// TestEventType is the type of the synthetic event a test delivery sends.
	TestEventType = "webhook.test"
)

// RetrySchedule is the delay before each retry: the first retry waits 30
// seconds, the last 24 hours, about 3 days in total. A delivery whose attempt
// fails with no retry left becomes failed.
var RetrySchedule = []time.Duration{
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute,
	time.Hour,
	2 * time.Hour,
	4 * time.Hour,
	8 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
	24 * time.Hour,
}

// MaxAttempts is the first attempt plus one per retry.
var MaxAttempts = len(RetrySchedule) + 1

// RetryDelay returns the delay before the retry that follows attempt number
// attempts (1-based), with up to 10 percent jitter either way, and false when
// no retry remains.
func RetryDelay(attempts int, jitter func() float64) (time.Duration, bool) {
	if attempts < 1 || attempts > len(RetrySchedule) {
		return 0, false
	}
	base := RetrySchedule[attempts-1]
	if jitter == nil {
		jitter = rand.Float64
	}
	factor := 0.9 + 0.2*jitter()
	return time.Duration(float64(base) * factor), true
}

// Endpoint statuses and disabled reasons as stored.
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"

	DisabledByUser    = "user"
	DisabledByFailing = "failing"
)

// Delivery statuses and kinds as stored.
const (
	DeliveryPending   = "pending"
	DeliverySucceeded = "succeeded"
	DeliveryFailed    = "failed"
	DeliverySkipped   = "skipped"

	KindEvent = "event"
	KindTest  = "test"
)

var (
	// ErrNotFound means the object does not exist for the caller's tenant.
	ErrNotFound = errors.New("not found")
	// ErrEndpointLimit means the tenant already has MaxEndpointsPerTenant
	// endpoints.
	ErrEndpointLimit = fmt.Errorf("a tenant can have at most %d webhook endpoints", MaxEndpointsPerTenant)
	// ErrTestRateLimited means the endpoint was tested within TestInterval.
	ErrTestRateLimited = errors.New("the endpoint was tested less than 10 seconds ago")
	// ErrPrecondition means the object is not in a state that allows the
	// operation; the wrapped message says why.
	ErrPrecondition = errors.New("precondition failed")
	// ErrSigningKeyUnusable means the endpoint has no signing secret that
	// decrypts and parses; retrying the same rows cannot succeed.
	ErrSigningKeyUnusable = errors.New("no usable signing secret")
)

// Store is Bosun's ledger. Cipher encrypts signing secrets.
type Store struct {
	DB     *sql.DB
	Cipher fieldcrypt.FieldCipher
}

// Endpoint is a tenant's webhook endpoint.
type Endpoint struct {
	ID                      string
	TenantID                string
	URL                     string
	Description             string
	EventTypes              []string
	APIVersion              string
	Status                  string
	DisabledReason          string
	DisabledAt              *time.Time
	ConsecutiveFailures     int
	FailingSince            *time.Time
	LastSuccessAt           *time.Time
	LastFailureAt           *time.Time
	PreviousSecretExpiresAt *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// Delivery is one delivery of an event, or a test delivery, to an endpoint.
type Delivery struct {
	ID             string
	TenantID       string
	EndpointID     string
	EventID        string
	EventType      string
	Kind           string
	Status         string
	Attempts       int
	NextAttemptAt  *time.Time
	LastStatusCode int
	LastErrorClass string
	DeliveredAt    *time.Time
	ReplayCount    int
	LastReplayedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Attempt is one HTTP attempt of a delivery.
type Attempt struct {
	ID              string
	AttemptNumber   int
	StatusCode      int
	ErrorClass      string
	LatencyMS       int
	ResponseExcerpt string
	AttemptedAt     time.Time
}

// inTx runs fn in a transaction and commits it.
func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if fnErr := fn(tx); fnErr != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(fnErr, rollbackErr)
		}
		return fnErr
	}
	return tx.Commit()
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}
