// Package placementpolicy owns Commodore's tenant/stream placement persistence.
package placementpolicy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

var (
	ErrNotFound            = errors.New("placement scope not found")
	ErrRevisionConflict    = errors.New("placement revision conflict")
	ErrIdempotencyConflict = errors.New("placement idempotency key was used for another command")
	ErrInvalidInput        = errors.New("invalid placement input")
)

type Scope struct {
	TenantID string
	Kind     string
	ID       string
}

func (s Scope) Validate() error {
	for _, id := range []string{s.TenantID, s.ID} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return fmt.Errorf("placement requires canonical nonzero scope UUIDs")
		}
	}
	if (s.Kind != "tenant" && s.Kind != "stream") || (s.Kind == "tenant" && s.ID != s.TenantID) {
		return fmt.Errorf("invalid placement scope")
	}
	return nil
}

type Snapshot struct {
	Scope                Scope
	Own                  *placementpb.PolicySet
	Parent               *placementpb.PolicySet
	Active               *placementpb.PolicySet
	ActiveParentRevision uint64
	Status               string
	UpdatedAt            time.Time
}

type ApplyInput struct {
	Scope                  Scope
	ExpectedRevision       uint64
	ExpectedParentRevision uint64
	Policy                 *placementpb.PolicySet
	ActorID                string
	IdempotencyKey         string
	ReviewDigest           string
	AcknowledgedWarnings   []string
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func ValidateIdempotencyKey(key string) error {
	if !validIdentifier(key, 128) {
		return fmt.Errorf("%w: invalid idempotency key", ErrInvalidInput)
	}
	return nil
}

// PreviousPolicy reconstructs the base of an immutable committed command. Scope
// ownership and current caller authorization must be checked before loading it.
func PreviousPolicy(change commodoredb.CommodoreMediaPlacementChange) (*placementpb.PolicySet, error) {
	if change.Revision <= 0 {
		return nil, fmt.Errorf("invalid committed placement revision")
	}
	return decodePolicy(change.PreviousPolicyPayload, change.Revision-1)
}

// Read uses one snapshot and never initializes rows or emits refresh obligations.
func (s *Store) Read(ctx context.Context, scope Scope) (snapshot Snapshot, err error) {
	return s.read(ctx, scope, false)
}

// ReadArtifactParent retains the last stream overlay after parent deletion. The
// caller must derive this scope from an owned artifact's persisted parent, not a
// viewer-supplied stream ID. Public stream management continues to use Read.
func (s *Store) ReadArtifactParent(ctx context.Context, scope Scope) (Snapshot, error) {
	if scope.Kind != "stream" {
		return Snapshot{}, ErrInvalidInput
	}
	return s.read(ctx, scope, true)
}

func (s *Store) read(ctx context.Context, scope Scope, allowRetained bool) (snapshot Snapshot, err error) {
	if err = scope.Validate(); err != nil {
		return Snapshot{}, err
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		retained := false
		if scope.Kind == "stream" {
			exists, queryErr := q.MediaPlacementStreamExists(ctx, commodoredb.MediaPlacementStreamExistsParams{TenantID: scope.TenantID, StreamID: scope.ID})
			if queryErr != nil {
				return queryErr
			}
			if !exists && !allowRetained {
				return ErrNotFound
			}
			retained = !exists
		}
		var own commodoredb.CommodoreMediaPlacementPolicy
		var queryErr error
		if retained {
			// An absent row is not evidence of an unrestricted deleted stream.
			own, queryErr = q.GetMediaPlacementPolicy(ctx, commodoredb.GetMediaPlacementPolicyParams{
				TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID,
			})
			if errors.Is(queryErr, sql.ErrNoRows) {
				return ErrNotFound
			}
		} else {
			own, queryErr = readScope(ctx, q, scope)
		}
		if queryErr != nil {
			return queryErr
		}
		parent := commodoredb.CommodoreMediaPlacementPolicy{}
		if scope.Kind == "stream" {
			parent, queryErr = readScope(ctx, q, Scope{TenantID: scope.TenantID, Kind: "tenant", ID: scope.TenantID})
			if queryErr != nil {
				return queryErr
			}
		}
		snapshot, queryErr = decodeSnapshot(scope, own, parent)
		if queryErr != nil {
			return queryErr
		}
		if own.Revision > 0 {
			change, changeErr := q.GetMediaPlacementRevision(ctx, commodoredb.GetMediaPlacementRevisionParams{TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID, Revision: own.Revision})
			if changeErr != nil {
				return changeErr
			}
			snapshot.Status = change.RolloutStatus
		}
		return nil
	})
	return snapshot, err
}

func (s *Store) Change(ctx context.Context, scope Scope, key string) (commodoredb.CommodoreMediaPlacementChange, error) {
	if err := scope.Validate(); err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	if err := ValidateIdempotencyKey(key); err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	return commodoredb.New(s.db).GetMediaPlacementChange(ctx, commodoredb.GetMediaPlacementChangeParams{TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID, IdempotencyKey: key})
}

// Apply is a single lock/CAS/audit/outbox transaction. validateReview executes
// against the locked parent and scope, but is skipped for an exact committed
// retry so a lost success response remains recoverable after its review expires.
// Caller authentication and current placement-write authorization are mandatory.
func (s *Store) Apply(ctx context.Context, input ApplyInput, validateReview func(Snapshot) error) (receipt commodoredb.CommodoreMediaPlacementChange, err error) {
	if err = input.Scope.Validate(); err != nil {
		return receipt, err
	}
	if validateReview == nil || !validIdentifier(input.ActorID, 255) || !validIdentifier(input.IdempotencyKey, 128) || len(input.ReviewDigest) != 64 {
		return receipt, fmt.Errorf("placement apply requires actor, idempotency key and checked review")
	}
	reviewHash, reviewHashErr := hex.DecodeString(input.ReviewDigest)
	if reviewHashErr != nil || len(reviewHash) != 32 || hex.EncodeToString(reviewHash) != input.ReviewDigest {
		return receipt, fmt.Errorf("placement apply requires a canonical review digest")
	}
	if input.ExpectedRevision >= math.MaxInt64 || input.ExpectedParentRevision > math.MaxInt64 || (input.Scope.Kind == "tenant" && input.ExpectedParentRevision != 0) {
		return receipt, fmt.Errorf("invalid expected placement revision")
	}
	canonical, canonicalErr := placement.CanonicalPolicySet(input.Policy)
	if canonicalErr != nil {
		return receipt, canonicalErr
	}
	if canonical == nil || canonical.GetRevision() != input.ExpectedRevision+1 {
		return receipt, fmt.Errorf("placement command must contain the next scoped revision")
	}
	input.Policy = canonical
	input.AcknowledgedWarnings = slices.Clone(input.AcknowledgedWarnings)
	slices.Sort(input.AcknowledgedWarnings)
	input.AcknowledgedWarnings = slices.Compact(input.AcknowledgedWarnings)
	encoded, encodeErr := proto.MarshalOptions{Deterministic: true}.Marshal(canonical)
	if encodeErr != nil {
		return receipt, encodeErr
	}
	requestDigest, digestErr := commandDigest(input, encoded)
	if digestErr != nil {
		return receipt, digestErr
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		parentRow, ownRow, lockErr := lockScopes(ctx, q, input.Scope)
		if lockErr != nil {
			return lockErr
		}
		previous, queryErr := q.GetMediaPlacementChange(ctx, commodoredb.GetMediaPlacementChangeParams{TenantID: input.Scope.TenantID, ScopeKind: input.Scope.Kind, ScopeID: input.Scope.ID, IdempotencyKey: input.IdempotencyKey})
		if queryErr == nil {
			if !equalDigest(previous.RequestSha256, requestDigest) {
				return ErrIdempotencyConflict
			}
			receipt = previous
			return nil
		}
		if !errors.Is(queryErr, sql.ErrNoRows) {
			return queryErr
		}
		if ownRow.Revision != int64(input.ExpectedRevision) || parentRow.Revision != int64(input.ExpectedParentRevision) {
			return ErrRevisionConflict
		}
		snapshot, snapshotErr := decodeSnapshot(input.Scope, ownRow, parentRow)
		if snapshotErr != nil {
			return snapshotErr
		}
		if reviewErr := validateReview(snapshot); reviewErr != nil {
			return reviewErr
		}
		input.Policy = canonical
		var commitErr error
		receipt, commitErr = commitChange(ctx, q, input, encoded, requestDigest, ownRow)
		return commitErr
	})
	if err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	return receipt, err
}

func readScope(ctx context.Context, q *commodoredb.Queries, scope Scope) (commodoredb.CommodoreMediaPlacementPolicy, error) {
	row, err := q.GetMediaPlacementPolicy(ctx, commodoredb.GetMediaPlacementPolicyParams{TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID})
	if errors.Is(err, sql.ErrNoRows) {
		return commodoredb.CommodoreMediaPlacementPolicy{}, nil
	}
	return row, err
}

func lockScope(ctx context.Context, q *commodoredb.Queries, scope Scope) (commodoredb.CommodoreMediaPlacementPolicy, error) {
	if err := q.EnsureMediaPlacementPolicy(ctx, commodoredb.EnsureMediaPlacementPolicyParams{TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID}); err != nil {
		return commodoredb.CommodoreMediaPlacementPolicy{}, err
	}
	return q.LockMediaPlacementPolicy(ctx, commodoredb.LockMediaPlacementPolicyParams{TenantID: scope.TenantID, ScopeKind: scope.Kind, ScopeID: scope.ID})
}

func decodeSnapshot(scope Scope, own, parent commodoredb.CommodoreMediaPlacementPolicy) (Snapshot, error) {
	result := Snapshot{Scope: scope, ActiveParentRevision: uint64(own.ActiveParentRevision), Status: "not_configured"}
	if own.Revision > 0 {
		result.UpdatedAt = own.UpdatedAt
	}
	var err error
	if own.ParentRevision < 0 || own.ActiveParentRevision < 0 || own.ActiveRevision > own.Revision {
		return Snapshot{}, fmt.Errorf("corrupt placement revision state")
	}
	result.Own, err = decodePolicy(own.PolicyPayload, own.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	result.Parent, err = decodePolicy(parent.PolicyPayload, parent.Revision)
	if err != nil {
		return Snapshot{}, err
	}
	result.Active, err = decodePolicy(own.ActivePolicyPayload, own.ActiveRevision)
	if err != nil {
		return Snapshot{}, err
	}
	// The all-zero database default is absence of activation evidence, not an
	// acknowledgement that the default policy has reached serving cells.
	if own.ActiveRevision == 0 && own.ActiveParentRevision == 0 {
		result.Active = nil
	}
	return result, nil
}

func decodePolicy(encoded []byte, revision int64) (*placementpb.PolicySet, error) {
	if revision < 0 || len(encoded) > 1<<20 {
		return nil, fmt.Errorf("corrupt placement policy")
	}
	policy := &placementpb.PolicySet{}
	if err := proto.Unmarshal(encoded, policy); err != nil {
		return nil, fmt.Errorf("decode placement: %w", err)
	}
	if policy.GetRevision() != uint64(revision) {
		return nil, fmt.Errorf("placement revision/payload mismatch")
	}
	return placement.CanonicalPolicySet(policy)
}

var errNoRows = sql.ErrNoRows

func equalDigest(stored []byte, digest [32]byte) bool {
	return bytes.Equal(stored, digest[:])
}

func validIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value
}

func commandDigest(input ApplyInput, payload []byte) ([32]byte, error) {
	encoded, err := json.Marshal(struct {
		Scope                  Scope
		ExpectedRevision       uint64
		ExpectedParentRevision uint64
		ActorID                string
		ReviewDigest           string
		Warnings               []string
		Payload                []byte
	}{input.Scope, input.ExpectedRevision, input.ExpectedParentRevision, input.ActorID, input.ReviewDigest, input.AcknowledgedWarnings, payload})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(append([]byte("frameworks/placement/apply/v1\x00"), encoded...)), nil
}
