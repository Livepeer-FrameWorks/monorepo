package quartermasterdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

var (
	ErrConsentNotFound            = errors.New("owned media capacity not found")
	ErrConsentRevisionConflict    = errors.New("media capacity consent revision conflict")
	ErrConsentIdempotencyConflict = errors.New("media capacity consent idempotency conflict")
	ErrConsentInvalidInput        = errors.New("invalid media capacity consent input")
)

type MediaConsentScope struct {
	TenantID  string
	ClusterID string
}

func (s MediaConsentScope) Validate() error {
	id, err := uuid.Parse(s.TenantID)
	if err != nil || id == uuid.Nil || id.String() != s.TenantID || !consentIdentifier(s.ClusterID, 100) {
		return ErrConsentInvalidInput
	}
	return nil
}

type MediaConsent struct {
	// ClusterRecordID fences deletion/recreation of a cluster with the same public name.
	ClusterRecordID     string
	Revision            uint64
	AllowIngest         bool
	AllowServe          bool
	AllowExternalSource bool
}

type MediaConsentApply struct {
	Scope                MediaConsentScope
	ExpectedRevision     uint64
	Consent              MediaConsent
	ActorID              string
	IdempotencyKey       string
	ReviewDigest         string
	AcknowledgedWarnings []string
}

type MediaConsentStore struct{ db *sql.DB }

func NewMediaConsentStore(db *sql.DB) *MediaConsentStore { return &MediaConsentStore{db: db} }

// Read never initializes state. Every read checks current cluster ownership.
func (s *MediaConsentStore) Read(ctx context.Context, scope MediaConsentScope) (MediaConsent, error) {
	if err := scope.Validate(); err != nil {
		return MediaConsent{}, err
	}
	row, err := New(s.db).GetOwnedMediaCapacityConsent(ctx, GetOwnedMediaCapacityConsentParams(scope))
	if errors.Is(err, sql.ErrNoRows) {
		return MediaConsent{}, ErrConsentNotFound
	}
	if err != nil {
		return MediaConsent{}, err
	}
	if row.Revision < 0 {
		return MediaConsent{}, ErrConsentInvalidInput
	}
	return MediaConsent{row.ClusterRecordID, uint64(row.Revision), row.AllowIngest, row.AllowServe, row.AllowExternalSource}, nil
}

// Change checks current ownership even for an already committed command.
func (s *MediaConsentStore) Change(ctx context.Context, scope MediaConsentScope, key string) (QuartermasterMediaCapacityConsentChange, error) {
	if err := scope.Validate(); err != nil {
		return QuartermasterMediaCapacityConsentChange{}, err
	}
	if !consentIdentifier(key, 128) {
		return QuartermasterMediaCapacityConsentChange{}, ErrConsentInvalidInput
	}
	row, err := New(s.db).GetOwnedMediaCapacityConsentChange(ctx, GetOwnedMediaCapacityConsentChangeParams{TenantID: scope.TenantID, ClusterID: scope.ClusterID, IdempotencyKey: key})
	if errors.Is(err, sql.ErrNoRows) {
		return QuartermasterMediaCapacityConsentChange{}, ErrConsentNotFound
	}
	return row, err
}

// Apply commits CAS, immutable receipt and subscriber refresh obligations together.
// Authentication and owner-management permission are caller responsibilities;
// the transaction independently fences ownership against concurrent transfer.
// An exact retry can recover success after its review expires, but not after losing ownership.
func (s *MediaConsentStore) Apply(ctx context.Context, input MediaConsentApply, validateReview func(MediaConsent) error) (receipt QuartermasterMediaCapacityConsentChange, err error) {
	if err = input.Scope.Validate(); err != nil {
		return receipt, err
	}
	if validateReview == nil || !consentIdentifier(input.ActorID, 255) || !consentIdentifier(input.IdempotencyKey, 128) || !consentDigest(input.ReviewDigest) || input.ExpectedRevision >= math.MaxInt64 || input.Consent.Revision != input.ExpectedRevision+1 || len(input.AcknowledgedWarnings) > 128 {
		return receipt, ErrConsentInvalidInput
	}
	input.AcknowledgedWarnings = slices.Clone(input.AcknowledgedWarnings)
	for _, warning := range input.AcknowledgedWarnings {
		if !consentIdentifier(warning, 128) {
			return receipt, ErrConsentInvalidInput
		}
	}
	slices.Sort(input.AcknowledgedWarnings)
	input.AcknowledgedWarnings = slices.Compact(input.AcknowledgedWarnings)
	if len(input.AcknowledgedWarnings) == 0 {
		input.AcknowledgedWarnings = nil
	}
	// The idempotency key addresses the receipt; it is not part of its command identity.
	command := input
	command.IdempotencyKey = ""
	requestDigest, err := mediaConsentHash("apply", command)
	if err != nil {
		return receipt, err
	}
	digest, err := MediaConsentDigest(input.Scope, input.Consent)
	if err != nil {
		return receipt, err
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := New(tx)
		current, lockErr := q.LockOwnedMediaCapacityConsent(ctx, LockOwnedMediaCapacityConsentParams{TenantID: input.Scope.TenantID, ClusterID: input.Scope.ClusterID})
		if errors.Is(lockErr, sql.ErrNoRows) {
			return ErrConsentNotFound
		}
		if lockErr != nil {
			return lockErr
		}
		if current.ClusterRecordID != input.Consent.ClusterRecordID {
			return ErrConsentRevisionConflict
		}
		previous, readErr := q.GetOwnedMediaCapacityConsentChange(ctx, GetOwnedMediaCapacityConsentChangeParams{TenantID: input.Scope.TenantID, ClusterID: input.Scope.ClusterID, IdempotencyKey: input.IdempotencyKey})
		if readErr == nil {
			if !bytes.Equal(previous.RequestSha256, requestDigest[:]) {
				return ErrConsentIdempotencyConflict
			}
			receipt = previous
			return nil
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		if current.Revision != int64(input.ExpectedRevision) {
			return ErrConsentRevisionConflict
		}
		if reviewErr := validateReview(MediaConsent{current.ClusterRecordID, uint64(current.Revision), current.AllowIngest, current.AllowServe, current.AllowExternalSource}); reviewErr != nil {
			return reviewErr
		}
		changed, updateErr := q.UpdateOwnedMediaCapacityConsent(ctx, UpdateOwnedMediaCapacityConsentParams{
			TenantID: input.Scope.TenantID, ClusterID: input.Scope.ClusterID, ExpectedRevision: int64(input.ExpectedRevision),
			AllowIngest: input.Consent.AllowIngest, AllowServe: input.Consent.AllowServe, AllowExternalSource: input.Consent.AllowExternalSource,
		})
		if updateErr != nil {
			return updateErr
		}
		if changed != 1 {
			return ErrConsentRevisionConflict
		}
		receipt, readErr = q.InsertMediaCapacityConsentChange(ctx, InsertMediaCapacityConsentChangeParams{
			ClusterRecordID: current.ClusterRecordID,
			TenantID:        input.Scope.TenantID, ClusterID: input.Scope.ClusterID, IdempotencyKey: input.IdempotencyKey,
			RequestSha256: requestDigest[:], Revision: int64(input.Consent.Revision), ConsentDigest: digest, ReviewDigest: input.ReviewDigest,
			PreviousAllowIngest: current.AllowIngest, PreviousAllowServe: current.AllowServe, PreviousAllowExternalSource: current.AllowExternalSource,
			AllowIngest: input.Consent.AllowIngest, AllowServe: input.Consent.AllowServe, AllowExternalSource: input.Consent.AllowExternalSource, ActorID: input.ActorID,
		})
		return readErr
	})
	if err != nil {
		return QuartermasterMediaCapacityConsentChange{}, err
	}
	return receipt, nil
}

func MediaConsentDigest(scope MediaConsentScope, consent MediaConsent) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	id, parseErr := uuid.Parse(consent.ClusterRecordID)
	if parseErr != nil || id == uuid.Nil || id.String() != consent.ClusterRecordID || consent.Revision > math.MaxInt64 {
		return "", ErrConsentInvalidInput
	}
	hash, err := mediaConsentHash("state", struct {
		Scope   MediaConsentScope
		Consent MediaConsent
	}{scope, consent})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash[:]), nil
}

func mediaConsentHash(kind string, value any) ([32]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("frameworks/media-capacity-consent/"+kind+"/v1\x00"), encoded...)), nil
}

func consentIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func consentDigest(value string) bool {
	hash, err := hex.DecodeString(value)
	return err == nil && len(hash) == 32 && hex.EncodeToString(hash) == value
}
