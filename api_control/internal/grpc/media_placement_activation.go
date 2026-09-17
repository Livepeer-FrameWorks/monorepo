package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	placementRolloutReasonUnattested = "cell has not attested placement enforcement"
	placementRolloutReasonLegacy     = "placement authority has not been issued to this cell yet"
	placementRolloutReasonPending    = "signed authority not yet acknowledged by this cell"
)

// placementRolloutView is the activation evidence for one authority: every cell
// the current version targets, whether that cell acknowledged it, and whether
// the cell attested enforcement. Revision and parent come from the signed payload
// the cells actually hold, never from the saved intent.
type placementRolloutView struct {
	Version  int64
	Schema   int32
	Tenant   *mediapb.TenantAuthority
	Object   *mediapb.MediaObjectAuthority
	Rollout  *placementpb.Rollout
	Complete bool
	Blocked  bool
}

// placementAuthorityForScope maps a policy scope to the signed authority whose
// acknowledgements carry that scope's policy.
func placementAuthorityForScope(scope placementpolicy.Scope) (kind, authorityID string, ok bool) {
	switch scope.Kind {
	case "tenant":
		return "tenant", scope.TenantID, true
	case "stream":
		return "media_object", sharedauthority.LiveStreamAuthorityID(scope.ID), true
	default:
		return "", "", false
	}
}

func placementRolloutFor(ctx context.Context, queries *commodoredb.Queries, kind, authorityID string) (*placementRolloutView, error) {
	rows, err := queries.ListCurrentMediaAuthorityRollout(ctx, commodoredb.ListCurrentMediaAuthorityRolloutParams{AuthorityKind: kind, AuthorityID: authorityID})
	if err != nil {
		return nil, fmt.Errorf("read placement rollout: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	view := &placementRolloutView{Version: rows[0].AuthorityVersion, Schema: rows[0].PayloadSchemaVersion,
		Rollout: &placementpb.Rollout{ExistingSessionsRetained: true, PendingRecipients: []*placementpb.RolloutRecipient{}}}
	switch kind {
	case "tenant":
		view.Tenant = &mediapb.TenantAuthority{}
		if decodeErr := proto.Unmarshal(rows[0].Payload, view.Tenant); decodeErr != nil {
			return nil, fmt.Errorf("decode tenant placement rollout payload: %w", decodeErr)
		}
	case "media_object":
		view.Object = &mediapb.MediaObjectAuthority{}
		if decodeErr := proto.Unmarshal(rows[0].Payload, view.Object); decodeErr != nil {
			return nil, fmt.Errorf("decode object placement rollout payload: %w", decodeErr)
		}
	default:
		return nil, fmt.Errorf("unsupported placement rollout authority kind %q", kind)
	}
	placementIssued := view.Schema > 0 && sharedauthority.IsPlacementSchema(uint32(view.Schema))
	applied := 0
	for _, row := range rows {
		acknowledged := row.DeliveryStatus == "acknowledged"
		switch {
		case !row.CellReady:
			view.Blocked = true
			view.Rollout.PendingRecipients = append(view.Rollout.PendingRecipients, &placementpb.RolloutRecipient{Id: row.CellID, Name: row.CellID, Status: "blocked", Reason: placementRolloutReasonUnattested, AuthorityExpiresAt: timestamppb.New(row.ValidUntil)})
		case !placementIssued:
			view.Rollout.PendingRecipients = append(view.Rollout.PendingRecipients, &placementpb.RolloutRecipient{Id: row.CellID, Name: row.CellID, Status: "pending", Reason: placementRolloutReasonLegacy, AuthorityExpiresAt: timestamppb.New(row.ValidUntil)})
		case !acknowledged:
			view.Rollout.PendingRecipients = append(view.Rollout.PendingRecipients, &placementpb.RolloutRecipient{Id: row.CellID, Name: row.CellID, Status: "pending", Reason: placementRolloutReasonPending, AuthorityExpiresAt: timestamppb.New(row.ValidUntil)})
		default:
			applied++
		}
	}
	view.Rollout.RequiredRecipients = uint32(len(rows))
	view.Rollout.AppliedRecipients = uint32(applied)
	view.Complete = placementIssued && applied == len(rows)
	status := "pending"
	if view.Complete {
		status = "effective"
	} else if view.Blocked {
		status = "blocked"
	}
	if view.Rollout.Status, err = mediaPlacementRollout(status); err != nil {
		return nil, err
	}
	return view, nil
}

// reconcilePlacementActivation runs inside the delivery acknowledgement
// transaction. Once every cell the current authority targets has acknowledged
// a schema-2 payload, the policy revision that payload carries becomes the
// active revision and its change is effective; an unattested target cell marks
// the change blocked with the reason the owner will see.
func (s *CommodoreServer) reconcilePlacementActivation(ctx context.Context, queries *commodoredb.Queries, kind, authorityID string) error {
	// Acknowledgements for one authority version arrive concurrently, one
	// transaction per cell. The completeness question below is an aggregate over
	// every cell's delivery row, so without serializing here each transaction
	// reads the others' pre-acknowledgement state, every one of them concludes
	// the rollout is incomplete, and a change that every cell has acknowledged is
	// left pending with nothing left to wait for. Held for the rest of the
	// transaction, so the last acknowledgement to commit is the one that sees a
	// complete set and activates.
	//
	// This REQUIRES the caller's transaction to be READ COMMITTED, which is the
	// default both callers rely on. Under REPEATABLE READ the snapshot is fixed
	// before the lock is granted, so the waiter would still read the holder's
	// pre-acknowledgement state and the bug returns in full, silently.
	if err := queries.LockMediaAuthorityActivation(ctx, commodoredb.LockMediaAuthorityActivationParams{
		AuthorityKind: kind, AuthorityID: authorityID,
	}); err != nil {
		return fmt.Errorf("lock placement activation: %w", err)
	}
	view, err := placementRolloutFor(ctx, queries, kind, authorityID)
	if err != nil || view == nil || view.Schema <= 0 || !sharedauthority.IsPlacementSchema(uint32(view.Schema)) {
		return err
	}
	var tenantID, scopeKind, scopeID string
	var policy *placementpb.PolicySet
	var parentRevision uint64
	switch {
	case view.Tenant != nil:
		tenantID, scopeKind, scopeID, policy = view.Tenant.GetTenantId(), "tenant", view.Tenant.GetTenantId(), view.Tenant.GetMediaPlacement()
	case view.Object != nil && view.Object.GetObjectKind() == mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		tenantID, scopeKind, scopeID = view.Object.GetTenantId(), "stream", view.Object.GetLiveStream().GetStreamId()
		policy, parentRevision = view.Object.GetMediaPlacement(), view.Object.GetPlacementTenantRevision()
	default:
		return nil
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(scopeID) == "" || policy == nil {
		return nil
	}
	revision := policy.GetRevision()
	if !view.Complete {
		if revision == 0 {
			return nil
		}
		status, reason := "pending", ""
		if view.Blocked {
			status, reason = "blocked", placementRolloutReasonUnattested
		}
		_, rolloutErr := queries.SetMediaPlacementChangeRollout(ctx, commodoredb.SetMediaPlacementChangeRolloutParams{
			RolloutStatus: status, RolloutReason: reason, TenantID: tenantID, ScopeKind: scopeKind, ScopeID: scopeID, Revision: int64(revision),
		})
		return rolloutErr
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(policy)
	if err != nil {
		return fmt.Errorf("encode active placement policy: %w", err)
	}
	if _, err := queries.ActivateMediaPlacementPolicy(ctx, commodoredb.ActivateMediaPlacementPolicyParams{
		ActiveRevision: int64(revision), ActiveParentRevision: int64(parentRevision), ActivePolicyPayload: encoded,
		TenantID: tenantID, ScopeKind: scopeKind, ScopeID: scopeID,
	}); err != nil {
		return fmt.Errorf("activate placement policy: %w", err)
	}
	if revision == 0 {
		return nil
	}
	if _, err := queries.MarkMediaPlacementChangesEffective(ctx, commodoredb.MarkMediaPlacementChangesEffectiveParams{
		TenantID: tenantID, ScopeKind: scopeKind, ScopeID: scopeID, Revision: int64(revision),
	}); err != nil {
		return fmt.Errorf("mark placement changes effective: %w", err)
	}
	return nil
}

// attachPlacementRollout fills recipient evidence into a rollout built from the
// stored change status. Stored EFFECTIVE only survives while every current target
// cell still holds the acknowledged schema-2 authority; a cell targeted since is
// reported as pending or blocked instead of silently widening "effective".
func (s *CommodoreServer) attachPlacementRollout(ctx context.Context, scope placementpolicy.Scope, rollout *placementpb.Rollout) {
	if s == nil || s.db == nil || rollout == nil {
		return
	}
	kind, authorityID, ok := placementAuthorityForScope(scope)
	if !ok {
		return
	}
	view, err := placementRolloutFor(ctx, commodoredb.New(s.db), kind, authorityID)
	if err != nil {
		if s.logger != nil {
			s.logger.WithError(err).WithField("scope", scope.Kind).Warn("Placement rollout evidence unavailable")
		}
		return
	}
	if view == nil {
		return
	}
	rollout.RequiredRecipients, rollout.AppliedRecipients, rollout.PendingRecipients = view.Rollout.RequiredRecipients, view.Rollout.AppliedRecipients, view.Rollout.PendingRecipients
	if rollout.Status == placementpb.RolloutStatus_ROLLOUT_STATUS_EFFECTIVE && !view.Complete {
		rollout.Status = view.Rollout.Status
	}
}

const (
	// placementActivationBacklogMinAge keeps the sweep off changes that are
	// still converging normally. An acknowledgement-driven activation lands in
	// milliseconds, so anything older than this has missed its trigger.
	placementActivationBacklogMinAge = 15 * time.Second
	placementActivationBacklogBatch  = 64
	// placementActivationBacklogTimeout bounds one scope's re-derivation. The
	// sweep is sequential, so an unbounded wait on the activation lock or on a
	// policy row held by a concurrent apply would head-of-line block the rest of
	// the page for as long as that holder ran.
	placementActivationBacklogTimeout = 5 * time.Second
)

// placementSweepCursor is where the next backlog page starts. It is process
// local and advisory only: losing it on restart re-walks from the beginning,
// which costs one extra pass and converges the same.
type placementSweepCursor struct {
	tenantID  string
	createdAt time.Time
}

func placementSweepStart() placementSweepCursor {
	return placementSweepCursor{tenantID: "00000000-0000-0000-0000-000000000000", createdAt: time.Unix(0, 0).UTC()}
}

// processPlacementActivationBacklog converges placement changes that no
// acknowledgement is going to resolve.
//
// Activation is derived inside the delivery acknowledgement transaction, which
// keeps an effective revision from ever outrunning its deliveries. The cost of
// that design is that it only ever runs when an acknowledgement runs: if the
// process handling the last one dies after committing the acknowledgement, or a
// delivery fails and its retry acknowledges against a version that has since
// moved on, the change is left pending with every cell already acknowledged and
// nothing scheduled to look again. An operator sees a fully delivered policy
// reported as not yet effective, indefinitely.
//
// This re-runs the same derivation for those scopes. It cannot activate
// anything the acknowledgement path would not have: reconcilePlacementActivation
// re-reads the delivery rows under the same lock and reaches the same
// conclusion. It only supplies the missing trigger.
func (s *CommodoreServer) processPlacementActivationBacklog(ctx context.Context) {
	if !s.mediaAuthorityEnabled() || s.db == nil {
		return
	}
	s.placementSweepMu.Lock()
	cursor := s.placementSweepCursor
	s.placementSweepMu.Unlock()
	if cursor.tenantID == "" {
		cursor = placementSweepStart()
	}

	rows, err := commodoredb.New(s.db).ListAuthoritiesAwaitingActivation(ctx, commodoredb.ListAuthoritiesAwaitingActivationParams{
		MinAgeMs:       placementActivationBacklogMinAge.Milliseconds(),
		AfterTenantID:  cursor.tenantID,
		AfterCreatedAt: cursor.createdAt,
		MaxRows:        placementActivationBacklogBatch,
	})
	if err != nil {
		if s.logger != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Failed to list placement activations awaiting convergence")
		}
		return
	}
	// A short page means the end of the set: wrap, so a change that sorts after
	// a permanently blocked prefix is still reached on a later tick.
	next := placementSweepStart()
	if len(rows) == placementActivationBacklogBatch {
		last := rows[len(rows)-1]
		next = placementSweepCursor{tenantID: last.TenantID, createdAt: last.CreatedAt}
	}
	s.placementSweepMu.Lock()
	s.placementSweepCursor = next
	s.placementSweepMu.Unlock()

	// One scope may hold several unresolved changes; the derivation is per
	// authority, so visiting it once per page is enough.
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		kind, authorityID, ok := placementAuthorityForScope(placementpolicy.Scope{
			TenantID: row.TenantID, Kind: row.ScopeKind, ID: row.ScopeID,
		})
		if !ok || seen[kind+":"+authorityID] {
			continue
		}
		seen[kind+":"+authorityID] = true

		scopeCtx, cancel := context.WithTimeout(ctx, placementActivationBacklogTimeout)
		err := database.WithRetryablePostgresTx(scopeCtx, s.db, nil, func(tx *sql.Tx) error {
			return s.reconcilePlacementActivation(scopeCtx, commodoredb.New(tx), kind, authorityID)
		})
		cancel()
		if err != nil && s.logger != nil && ctx.Err() == nil {
			s.logger.WithError(err).WithFields(logging.Fields{
				"authority_kind": kind, "authority_id": authorityID,
			}).Warn("Failed to converge placement activation")
		}
	}
}
