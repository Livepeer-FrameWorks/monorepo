package placementpolicy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

const (
	SystemActorBootstrap     = "system:bootstrap"
	SystemActorDataMigration = "system:data-migration"
)

// SystemApplyInput is a placement change Commodore derives itself (stream
// create/update source location, bootstrap, data migration) rather than one a
// user reviewed through the placement API.
type SystemApplyInput struct {
	Scope   Scope
	ActorID string
	// Update derives verb updates from the locked own policy; nil or empty means
	// the scope already expresses the intended rules.
	Update func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error)
	// Validate runs under the locks against the parent and the resulting own
	// policy, including when Update produced no change.
	Validate func(locked Snapshot, next *placementpb.PolicySet) error
}

type SystemApplyResult struct {
	Changed  bool
	Previous *placementpb.PolicySet
	Policy   *placementpb.PolicySet
	Parent   *placementpb.PolicySet
	Receipt  commodoredb.CommodoreMediaPlacementChange
}

// ApplySystem runs inside the caller's transaction so the placement change
// commits with the stream write that motivated it. It takes the same locks,
// revision CAS, immutable receipt and authority-refresh obligation as a reviewed
// apply. The review digest is computed from the command itself, and the
// idempotency key is derived from scope, base revisions and resulting payload,
// so a retry of the same transaction converges on one receipt.
func ApplySystem(ctx context.Context, exec commodoredb.DBTX, input SystemApplyInput) (SystemApplyResult, error) {
	if err := input.Scope.Validate(); err != nil {
		return SystemApplyResult{}, err
	}
	if exec == nil || input.Update == nil || !validIdentifier(input.ActorID, 255) {
		return SystemApplyResult{}, fmt.Errorf("system placement apply requires an executor, actor and update")
	}
	q := commodoredb.New(exec)
	parentRow, ownRow, err := lockScopes(ctx, q, input.Scope)
	if err != nil {
		return SystemApplyResult{}, err
	}
	snapshot, err := decodeSnapshot(input.Scope, ownRow, parentRow)
	if err != nil {
		return SystemApplyResult{}, err
	}
	result := SystemApplyResult{Previous: snapshot.Own, Policy: snapshot.Own, Parent: snapshot.Parent}
	updates, err := input.Update(snapshot.Own)
	if err != nil {
		return SystemApplyResult{}, err
	}
	next := snapshot.Own
	if len(updates) != 0 {
		if next, err = placement.ApplyUpdates(snapshot.Own, updates); err != nil {
			return SystemApplyResult{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
		}
	}
	if input.Validate != nil {
		if err = input.Validate(snapshot, next); err != nil {
			return SystemApplyResult{}, err
		}
	}
	if len(updates) == 0 || samePolicyContent(snapshot.Own, next) {
		return result, nil
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(next)
	if err != nil {
		return SystemApplyResult{}, err
	}
	command := ApplyInput{
		Scope: input.Scope, ExpectedRevision: snapshot.Own.GetRevision(), ExpectedParentRevision: snapshot.Parent.GetRevision(),
		Policy: next, ActorID: input.ActorID,
	}
	command.IdempotencyKey = systemIdempotencyKey(command, encoded)
	tenant, stream := next, (*placementpb.PolicySet)(nil)
	if input.Scope.Kind == "stream" {
		tenant, stream = snapshot.Parent, next
	}
	policyDigest, err := placement.PolicySetsDigest(tenant, stream)
	if err != nil {
		return SystemApplyResult{}, err
	}
	if command.ReviewDigest, err = systemReviewDigest(command, policyDigest); err != nil {
		return SystemApplyResult{}, err
	}
	requestDigest, err := commandDigest(command, encoded)
	if err != nil {
		return SystemApplyResult{}, err
	}
	receipt, err := commitChange(ctx, q, command, encoded, requestDigest, ownRow)
	if err != nil {
		return SystemApplyResult{}, err
	}
	result.Changed, result.Policy, result.Receipt = true, next, receipt
	return result, nil
}

func lockScopes(ctx context.Context, q *commodoredb.Queries, scope Scope) (parentRow, ownRow commodoredb.CommodoreMediaPlacementPolicy, err error) {
	tenantScope := Scope{TenantID: scope.TenantID, Kind: "tenant", ID: scope.TenantID}
	if parentRow, err = lockScope(ctx, q, tenantScope); err != nil {
		return parentRow, ownRow, err
	}
	if scope.Kind != "stream" {
		return commodoredb.CommodoreMediaPlacementPolicy{}, parentRow, nil
	}
	if _, err = q.LockMediaPlacementStream(ctx, commodoredb.LockMediaPlacementStreamParams{TenantID: scope.TenantID, StreamID: scope.ID}); err != nil {
		if errors.Is(err, errNoRows) {
			return parentRow, ownRow, ErrNotFound
		}
		return parentRow, ownRow, err
	}
	ownRow, err = lockScope(ctx, q, scope)
	return parentRow, ownRow, err
}

// commitChange writes a validated command against rows the caller locked: the
// revision CAS, the immutable receipt, superseding older pending receipts and
// the authority refresh obligation. An existing receipt for the same key is
// returned when the command is identical and refused otherwise.
func commitChange(ctx context.Context, q *commodoredb.Queries, input ApplyInput, encoded []byte, requestDigest [32]byte, ownRow commodoredb.CommodoreMediaPlacementPolicy) (commodoredb.CommodoreMediaPlacementChange, error) {
	previous, err := q.GetMediaPlacementChange(ctx, commodoredb.GetMediaPlacementChangeParams{TenantID: input.Scope.TenantID, ScopeKind: input.Scope.Kind, ScopeID: input.Scope.ID, IdempotencyKey: input.IdempotencyKey})
	if err == nil {
		if !equalDigest(previous.RequestSha256, requestDigest) {
			return commodoredb.CommodoreMediaPlacementChange{}, ErrIdempotencyConflict
		}
		return previous, nil
	}
	if !errors.Is(err, errNoRows) {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	tenant, stream := input.Policy, (*placementpb.PolicySet)(nil)
	if input.Scope.Kind == "stream" {
		parent, parentErr := readScope(ctx, q, Scope{TenantID: input.Scope.TenantID, Kind: "tenant", ID: input.Scope.TenantID})
		if parentErr != nil {
			return commodoredb.CommodoreMediaPlacementChange{}, parentErr
		}
		if tenant, parentErr = decodePolicy(parent.PolicyPayload, parent.Revision); parentErr != nil {
			return commodoredb.CommodoreMediaPlacementChange{}, parentErr
		}
		stream = input.Policy
	}
	policyDigest, err := placement.PolicySetsDigest(tenant, stream)
	if err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	changed, err := q.UpdateMediaPlacementPolicy(ctx, commodoredb.UpdateMediaPlacementPolicyParams{
		TenantID: input.Scope.TenantID, ScopeKind: input.Scope.Kind, ScopeID: input.Scope.ID,
		Revision: int64(input.Policy.GetRevision()), ParentRevision: int64(input.ExpectedParentRevision), PolicyPayload: encoded, ExpectedRevision: int64(input.ExpectedRevision),
	})
	if err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	if changed != 1 {
		return commodoredb.CommodoreMediaPlacementChange{}, ErrRevisionConflict
	}
	receipt, err := q.InsertMediaPlacementChange(ctx, commodoredb.InsertMediaPlacementChangeParams{
		TenantID: input.Scope.TenantID, ScopeKind: input.Scope.Kind, ScopeID: input.Scope.ID, IdempotencyKey: input.IdempotencyKey,
		RequestSha256: requestDigest[:], Revision: int64(input.Policy.GetRevision()), ParentRevision: int64(input.ExpectedParentRevision),
		PolicyDigest: policyDigest, PreviousPolicyPayload: ownRow.PolicyPayload, PolicyPayload: encoded, ActorID: input.ActorID,
		ReviewDigest: input.ReviewDigest,
	})
	if err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	if err = q.SupersedeMediaPlacementChanges(ctx, commodoredb.SupersedeMediaPlacementChangesParams{TenantID: input.Scope.TenantID, ScopeKind: input.Scope.Kind, ScopeID: input.Scope.ID, Revision: receipt.Revision}); err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	reason := "media_placement_changed"
	if input.Scope.Kind == "stream" {
		reason = "media_object:live_stream:" + input.Scope.ID + ":media_placement_changed"
	}
	if _, err = q.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
		SourceService: "commodore", SourceEventID: fmt.Sprintf("placement:%s:%s:%d", input.Scope.Kind, input.Scope.ID, receipt.Revision), TenantID: input.Scope.TenantID, Reason: reason,
	}); err != nil {
		return commodoredb.CommodoreMediaPlacementChange{}, err
	}
	return receipt, nil
}

// samePolicyContent compares two canonical policy sets without their revisions.
func samePolicyContent(a, b *placementpb.PolicySet) bool {
	left, right := proto.CloneOf(a), proto.CloneOf(b)
	if left == nil {
		left = &placementpb.PolicySet{}
	}
	if right == nil {
		right = &placementpb.PolicySet{}
	}
	left.Revision, right.Revision = 0, 0
	return proto.Equal(left, right)
}

func systemIdempotencyKey(command ApplyInput, payload []byte) string {
	sum := sha256.New()
	for _, part := range []string{"frameworks/placement/system-command/v1", command.Scope.TenantID, command.Scope.Kind, command.Scope.ID, command.ActorID,
		strconv.FormatUint(command.ExpectedRevision, 10), strconv.FormatUint(command.ExpectedParentRevision, 10)} {
		sum.Write([]byte(part))
		sum.Write([]byte{0})
	}
	sum.Write(payload)
	return "system-" + hex.EncodeToString(sum.Sum(nil))
}

func systemReviewDigest(command ApplyInput, policyDigest string) (string, error) {
	encoded, err := json.Marshal(struct {
		TenantID               string
		ActorID                string
		ScopeKind              string
		ScopeID                string
		ExpectedRevision       uint64
		ExpectedParentRevision uint64
		PolicyDigest           string
	}{command.Scope.TenantID, command.ActorID, command.Scope.Kind, command.Scope.ID, command.ExpectedRevision, command.ExpectedParentRevision, policyDigest})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("frameworks/placement/system-review/v1\x00"), encoded...))
	return hex.EncodeToString(sum[:]), nil
}

// StreamPolicies reads the stored own policies of the listed streams for one
// tenant. Streams without a row are absent from the result.
func StreamPolicies(ctx context.Context, exec commodoredb.DBTX, tenantID string, streamIDs []string) (map[string]*placementpb.PolicySet, error) {
	out := make(map[string]*placementpb.PolicySet, len(streamIDs))
	if len(streamIDs) == 0 {
		return out, nil
	}
	rows, err := commodoredb.New(exec).ListStreamMediaPlacementPolicies(ctx, commodoredb.ListStreamMediaPlacementPoliciesParams{TenantID: tenantID, StreamIds: streamIDs})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		policy, decodeErr := decodePolicy(row.PolicyPayload, row.Revision)
		if decodeErr != nil {
			return nil, decodeErr
		}
		out[row.StreamID] = policy
	}
	return out, nil
}
