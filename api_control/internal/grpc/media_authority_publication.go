package grpc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

type mediaObjectParentContextKey struct{}

func retireMediaAuthorityTargets(ctx context.Context, queries *commodoredb.Queries, kind, id string, targets []string) (int64, error) {
	return queries.RetireMediaAuthorityTargets(ctx, commodoredb.RetireMediaAuthorityTargetsParams{
		AuthorityKind: kind, AuthorityID: id, ActiveCells: append([]string{}, targets...),
	})
}

func lockMediaAuthorityTargetHorizons(ctx context.Context, queries *commodoredb.Queries, kind, id string) (map[string]sql.NullTime, error) {
	rows, err := queries.LockMediaAuthorityTargetHorizons(ctx, commodoredb.LockMediaAuthorityTargetHorizonsParams{AuthorityKind: kind, AuthorityID: id})
	if err != nil {
		return nil, err
	}
	horizons := make(map[string]sql.NullTime, len(rows))
	for _, row := range rows {
		horizons[row.CellID] = row.CorrectionUntil
	}
	return horizons, nil
}

// A correction is bounded per recipient. Active cells retain their full lease;
// with no active targets the version itself need only last through corrections.
func mediaAuthorityRecipientValidity(until time.Time, ceiling sql.NullTime) time.Time {
	if ceiling.Valid && ceiling.Time.Before(until) {
		return mediaAuthorityInstant(ceiling.Time)
	}
	return until
}

func mediaAuthorityCorrectionOnlyValidity(until time.Time, targets []string, horizons map[string]sql.NullTime) time.Time {
	if len(targets) != 0 {
		return until
	}
	var latest time.Time
	for _, horizon := range horizons {
		if horizon.Valid && horizon.Time.After(latest) {
			latest = horizon.Time
		}
	}
	if latest.After(time.Now()) && latest.Before(until) {
		return mediaAuthorityInstant(latest)
	}
	return until
}

func withMediaObjectParent(ctx context.Context, tenant *mediaauthoritypb.TenantAuthority) context.Context {
	return context.WithValue(ctx, mediaObjectParentContextKey{}, proto.CloneOf(tenant))
}

// Access revocations cannot leave a previous allow usable because a dependency
// failed. Unchanged or widened playback access retains its valid copy.
func (s *CommodoreServer) revokeChangedAccessOnCompileFailure(ctx context.Context, authorityID string, kind mediaauthoritypb.MediaObjectKind, desired *mediaauthoritypb.PlaybackPolicy, desiredLive *mediaauthoritypb.LiveStreamAuthority, compileErr error) error {
	class, _ := classifyAuthorityCompileError(compileErr)
	if compileErr == nil || class == authorityCompileSuperseded {
		return compileErr
	}
	// A failed dependency may have consumed the compile deadline. Give the
	// safety write a bounded budget of its own, retaining generation and parent
	// fences so an overtaken compile still cannot publish a stale revocation.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	current, err := commodoredb.New(s.db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: authorityID})
	if errors.Is(err, sql.ErrNoRows) {
		return compileErr
	}
	if err != nil {
		s.observeMediaAuthorityRevocationCheckFailure(authorityID, "previous_read", err)
		return compileErr
	}
	previous := &mediaauthoritypb.MediaObjectAuthority{}
	if err = proto.Unmarshal(current.Payload, previous); err != nil {
		s.observeMediaAuthorityRevocationCheckFailure(authorityID, "previous_decode", err)
		return compileErr
	}
	if previous.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		return compileErr
	}
	accessChanged := playbackAccessRevoked(previous.GetPlaybackPolicy(), desired)
	if previousLive := previous.GetLiveStream(); desiredLive != nil && previousLive != nil {
		accessChanged = accessChanged || previousLive.GetIngestMode() != desiredLive.GetIngestMode() ||
			(len(previousLive.GetPublishingCredentialSha256()) > 0 && !bytes.Equal(previousLive.GetPublishingCredentialSha256(), desiredLive.GetPublishingCredentialSha256()))
	}
	if source, ok := ctx.Value(playbackAccessSourceKey{}).(playbackAccessSource); ok && !accessChanged {
		if desired == nil {
			desired = source.localPolicy(previous.GetPlaybackPolicy())
			accessChanged = playbackAccessRevoked(previous.GetPlaybackPolicy(), desired)
		}
		oldKind := previous.GetPlaybackPolicy().GetKind()
		if desired == nil && source.requiresAuth && oldKind == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC {
			accessChanged = true
		} else if !accessChanged && (desired == nil || (oldKind == mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK && desired.GetKind() == oldKind)) {
			revision, readErr := commodoredb.New(s.db).GetCurrentMediaAuthorityPlaybackSourceRevision(ctx, authorityID)
			if readErr != nil {
				s.observeMediaAuthorityRevocationCheckFailure(authorityID, "playback_source_read", readErr)
			} else {
				accessChanged = revision != "" && revision != source.revision
			}
		}
	}
	if !accessChanged && previous.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE && sharedauthority.IsPlacementSchema(previous.GetSchemaVersion()) {
		scope := placementpolicy.Scope{TenantID: previous.GetTenantId(), Kind: "stream"}
		if live := previous.GetLiveStream(); live != nil {
			scope.ID = live.GetStreamId()
		} else {
			scope.ID = previous.GetArtifact().GetParentStreamId()
		}
		if scope.ID != "" {
			store := placementpolicy.NewStore(s.db)
			var snapshot placementpolicy.Snapshot
			if previous.GetArtifact() != nil {
				snapshot, err = store.ReadArtifactParent(ctx, scope)
			} else {
				snapshot, err = store.Read(ctx, scope)
			}
			if err != nil {
				s.observeMediaAuthorityRevocationCheckFailure(authorityID, "placement_read", err)
				return compileErr
			}
			accessChanged = placementAccessChanged(previous.GetMediaPlacement(), snapshot.Own)
		}
	}
	if !accessChanged {
		return compileErr
	}
	if err = s.compileDeniedMediaObjectAuthority(ctx, authorityID, kind, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE, "media-access-change-pending"); err != nil {
		if errors.Is(err, errMediaAuthorityCompileSuperseded) {
			return err
		}
		s.observeMediaAuthorityRevocationCheckFailure(authorityID, "deny_publish", err)
		return &authorityCompileError{class: authorityCompileTransient, code: "deny_publish_failed", err: err}
	}
	return compileErr
}

func (s *CommodoreServer) observeMediaAuthorityRevocationCheckFailure(authorityID, stage string, err error) {
	if s.metrics != nil && s.metrics.MediaAuthorityRevocationCheckFailures != nil {
		s.metrics.MediaAuthorityRevocationCheckFailures.WithLabelValues(stage).Inc()
	}
	if s.logger != nil {
		s.logger.WithError(err).WithField("authority_id", authorityID).WithField("stage", stage).
			Error("Media authority revocation check could not complete; retaining the original compile retry")
	}
}

// Causes for publishing a new authority version.
const (
	mediaAuthorityCauseContent  = "content"
	mediaAuthorityCauseTargets  = "targets"
	mediaAuthorityCauseValidity = "validity"
	mediaAuthorityCauseRenewal  = "renewal"
)

// mediaAuthorityPublication is the outcome of comparing a compiled authority
// with the version currently published for the same identity.
type mediaAuthorityPublication struct {
	publish    bool
	cause      string
	hasCurrent bool
	current    commodoredb.GetCurrentMediaAuthorityPublicationRow
	// horizon is set when the authority is not in use and some cell may still
	// hold a valid copy: the latest instant any version is valid until.
	horizon time.Time
	// inheritedValidUntil is what a publication of an authority not in use
	// carries: the horizon, or less when the compiled authority allows less (a
	// new quote or a shortened grant). Never longer than the horizon, so
	// correcting a copy does not keep an unused object in cells.
	inheritedValidUntil time.Time
}

// correcting reports an authority not in use whose current version runs out
// before an older one some cell may still hold. That cell decides on the older
// version until it receives a newer one, so the current version is kept valid,
// and delivered, until the older one has run out too.
func (d mediaAuthorityPublication) correcting(validUntil time.Time) bool {
	return !d.horizon.IsZero() && validUntil.Before(d.horizon)
}

// keepsRenewal reports whether a published version of validUntil needs a
// renewal: every version of an authority in use does, and a version of one not
// in use, or a tombstone, does while it is still correcting an older copy.
func (d mediaAuthorityPublication) keepsRenewal(ctx context.Context, validUntil time.Time, tombstone bool) bool {
	if tombstone {
		return d.correcting(validUntil)
	}
	return mediaAuthorityInUse(ctx) || d.correcting(validUntil)
}

// decideMediaAuthorityPublication runs inside the publishing transaction, after
// the compile fence is locked, so the current version cannot move underneath it.
// A version is published only when it would say something new, reach different
// cells, shorten validity, or extend a validity that is due for renewal. Every
// other compile is a no-op: reconciliation and redundant events cost a read.
//
// contentDigest is nil when the content has no stable identity, which always
// publishes. A tombstone is terminal: cells keep refusing it past hard expiry,
// so an unchanged tombstone is re-issued only while it corrects: while a cell
// may still hold a valid version from before it (it was offline, or its
// database was restored) and the tombstone itself runs out first.
//
// An authority that is not in use (see withMediaAuthorityInUse) is not renewed
// for its own sake. While cells may still hold a valid copy, a change is
// published with at most that copy's validity, and the current version is
// renewed until it lasts as long as the copy it corrects; once the copies have
// run out there is nothing to correct and nothing is published until the
// authority is used again. A tombstone publishes regardless: it is what removes
// the object from cells still within their correction horizon.
func decideMediaAuthorityPublication(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string, target commodoredb.MediaAuthorityTarget, contentDigest []byte, targets []string, tombstone bool, validUntil, now time.Time) (mediaAuthorityPublication, error) {
	inUse := mediaAuthorityInUse(ctx) || tombstone
	current, err := queries.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{
		AuthorityKind: authorityKind, AuthorityID: authorityID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return mediaAuthorityPublication{publish: inUse, cause: mediaAuthorityCauseContent}, nil
	}
	if err != nil {
		return mediaAuthorityPublication{}, fmt.Errorf("load current %s authority publication: %w", authorityKind, err)
	}
	decision := mediaAuthorityPublication{hasCurrent: true, current: current}
	if tombstone {
		horizon, horizonErr := mediaAuthorityValidHorizon(ctx, queries, authorityKind, authorityID)
		if horizonErr != nil {
			return mediaAuthorityPublication{}, horizonErr
		}
		if horizon.After(now) {
			decision.horizon = mediaAuthorityInstant(horizon)
		}
	} else if !inUse {
		// Whether a correction still has somewhere to go, and how long it has to
		// last, is decided by every version, not the current one: a cell that
		// missed a shorter-lived replacement still holds the longer original.
		horizon, horizonErr := mediaAuthorityValidHorizon(ctx, queries, authorityKind, authorityID)
		if horizonErr != nil {
			return mediaAuthorityPublication{}, horizonErr
		}
		if !horizon.After(now.Add(mediaAuthorityCoolingFloor)) {
			return decision, nil
		}
		decision.horizon = mediaAuthorityInstant(horizon)
		decision.inheritedValidUntil = decision.horizon
		if compiled := mediaAuthorityInstant(validUntil); compiled.Before(decision.horizon) {
			decision.inheritedValidUntil = compiled
		}
	}
	if len(contentDigest) == 0 || !bytes.Equal(contentDigest, current.ContentDigest) {
		decision.publish, decision.cause = true, mediaAuthorityCauseContent
		return decision, nil
	}
	currentCells, err := queries.ListCurrentMediaAuthorityDeliveryCells(ctx, commodoredb.ListCurrentMediaAuthorityDeliveryCellsParams{
		AuthorityKind: authorityKind, AuthorityID: authorityID,
	})
	if err != nil {
		return mediaAuthorityPublication{}, fmt.Errorf("load current %s authority cells: %w", authorityKind, err)
	}
	// Both sides are ordered here, in byte order. The query's ORDER BY follows
	// the database collation, which need not agree with Go's string order, and a
	// disagreement would read as a target change on every compile.
	currentCells, targets = slices.Sorted(slices.Values(currentCells)), slices.Sorted(slices.Values(targets))
	switch {
	case !slices.Equal(currentCells, targets):
		decision.publish, decision.cause = true, mediaAuthorityCauseTargets
	case tombstone:
		if decision.correcting(current.ValidUntil) && validUntil.After(current.ValidUntil) &&
			!now.Before(mediaAuthorityRenewAt(target, current.IssuedAt, current.RefreshAfter, current.ValidUntil)) {
			decision.publish, decision.cause = true, mediaAuthorityCauseRenewal
		}
	case !inUse:
		// Unchanged, and not renewed for its own sake. A current version that
		// runs out before an older copy is renewed when due, even when it has
		// already expired: delivery never sends an expired version, so only a
		// new one reaches the cell holding the older copy.
		if decision.correcting(current.ValidUntil) && decision.inheritedValidUntil.After(current.ValidUntil) &&
			!now.Before(mediaAuthorityRenewAt(target, current.IssuedAt, current.RefreshAfter, current.ValidUntil)) {
			decision.publish, decision.cause = true, mediaAuthorityCauseRenewal
		}
	case validUntil.Before(current.ValidUntil):
		decision.publish, decision.cause = true, mediaAuthorityCauseValidity
	case !now.Before(mediaAuthorityRenewAt(target, current.IssuedAt, current.RefreshAfter, current.ValidUntil)) && validUntil.After(current.ValidUntil):
		decision.publish, decision.cause = true, mediaAuthorityCauseRenewal
	}
	return decision, nil
}

// mediaAuthorityCellsToCorrect lists the cells a publication goes to besides its
// targets. A change has to reach every cell that may still hold a valid copy,
// even one that is no longer a target; a cell whose every copy has run out has
// nothing left to correct. A tombstone also fences routing pointers at every
// historical target still within its fixed correction horizon.
func mediaAuthorityCellsToCorrect(ctx context.Context, queries *commodoredb.Queries, authorityKind, authorityID string, tombstone bool) ([]string, error) {
	if tombstone {
		return queries.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{AuthorityKind: authorityKind, AuthorityID: authorityID})
	}
	return queries.ListMediaAuthorityHoldingCells(ctx, commodoredb.ListMediaAuthorityHoldingCellsParams{AuthorityKind: authorityKind, AuthorityID: authorityID})
}

// publishedMediaAuthorityValidity is the validity a decided publication carries:
// the compiled one, or the current version's when the authority is not in use.
// refresh_after keeps its place halfway through, so a cell that reads an unused
// authority late in its life asks for it again, which is what brings it back.
func publishedMediaAuthorityValidity(decision mediaAuthorityPublication, issuedAt, validUntil, refreshAfter time.Time) (time.Time, time.Time) {
	if decision.inheritedValidUntil.IsZero() {
		return validUntil, refreshAfter
	}
	inherited := mediaAuthorityInstant(decision.inheritedValidUntil)
	return inherited, mediaAuthorityRefreshAfter(issuedAt, inherited)
}

// keepMediaAuthorityRenewalScheduled runs when a compile published nothing. A
// live authority must always hold a renewal obligation, because a cell refuses
// a hard-expired authority outright, so every no-op leaves one behind:
//
//   - On a renewal lane the claimed row is this renewal. A due renewal that
//     cannot extend validity yet (the tenant authority that caps it has not
//     renewed) is moved later; otherwise it would complete and never run again.
//   - On any other lane the schedule is left alone if one is live, and restored
//     if it is missing or settled.
//
// A tombstone is terminal and keeps no schedule, so its renewal row completes,
// unless it is still correcting an older copy a cell may hold. An authority
// nobody uses is not renewed either, unless its current version is still
// correcting a longer-lived older copy: its renewal goes dormant, and stays
// dormant until the authority is used or published again.
func keepMediaAuthorityRenewalScheduled(ctx context.Context, queries *commodoredb.Queries, target commodoredb.MediaAuthorityTarget, tenantID string, decision mediaAuthorityPublication, tombstone bool, now time.Time) error {
	if !decision.hasCurrent {
		return nil
	}
	if tombstone && !decision.correcting(decision.current.ValidUntil) {
		return nil
	}
	if !decision.keepsRenewal(ctx, decision.current.ValidUntil, tombstone) {
		markMediaAuthorityDormant(ctx)
		return nil
	}
	current := decision.current
	next := mediaAuthorityRenewAt(target, current.IssuedAt, current.RefreshAfter, current.ValidUntil)
	if !isMediaAuthorityDeadlineLane(mediaAuthorityObligationLane(ctx)) {
		if err := ensureMediaAuthorityRenewal(ctx, queries, target, tenantID, current.AuthorityVersion, next, current.ValidUntil, true); err != nil {
			return fmt.Errorf("ensure %s renewal: %w", target.Key, err)
		}
		return nil
	}
	if retry := mediaAuthorityRenewalRetryAt(target.Key, now, current.IssuedAt, current.ValidUntil); retry.After(next) {
		next = retry
	}
	if err := queries.ScheduleMediaAuthorityRenewal(ctx, target, tenantID, current.AuthorityVersion, next); err != nil {
		return fmt.Errorf("reschedule %s renewal: %w", target.Key, err)
	}
	return nil
}

// ensureMediaAuthorityRenewal restores a missing or settled renewal. reviveDormant
// is set only by a caller that found the authority in use; the repair pass
// leaves a dormant renewal alone.
func ensureMediaAuthorityRenewal(ctx context.Context, queries *commodoredb.Queries, target commodoredb.MediaAuthorityTarget, tenantID string, version int64, renewAt, expiresAt time.Time, reviveDormant bool) error {
	lane := commodoredb.MediaAuthorityLaneObjectDeadline
	if target.Kind == commodoredb.MediaAuthorityTargetTenant {
		lane = commodoredb.MediaAuthorityLaneTenantDeadline
	}
	return queries.EnsureMediaAuthorityRenewal(ctx, commodoredb.EnsureMediaAuthorityRenewalParams{
		TargetKey: target.Key, Lane: lane, TenantID: tenantID, TargetKind: target.Kind,
		BoundVersion: sql.NullInt64{Int64: version, Valid: version > 0}, NextAttemptAt: renewAt.UTC(),
		ExpiresAt: sql.NullTime{Time: expiresAt.UTC(), Valid: !expiresAt.IsZero()}, ReviveDormant: reviveDormant,
	})
}

// mediaAuthorityInstant normalizes an envelope time to the precision the
// database stores. The signed envelope, the version row, and the publication
// decision must all see the same instant: PostgreSQL rounds a nanosecond value
// to microseconds, so an un-normalized validity capped at a fixed instant would
// compare as shrinking or extending by a microsecond on every compile and
// publish a version each time.
func mediaAuthorityInstant(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
}

// mediaAuthorityShortLease reports an envelope whose whole validity is a minute
// or less: a placement-schema authority bounded by a commercial quote. Such a
// delivery must reach its cell within seconds, so it is queued for the
// short-lease delivery worker instead of the ordinary one.
func mediaAuthorityShortLease(schemaVersion uint32, issuedAt, validUntil time.Time) bool {
	return sharedauthority.IsPlacementSchema(schemaVersion) && !validUntil.After(issuedAt.Add(time.Minute))
}

func (s *CommodoreServer) observeMediaAuthorityPublished(ctx context.Context, authorityKind, cause string, early bool) {
	markMediaAuthorityPublished(ctx)
	if s.metrics != nil && s.metrics.MediaAuthorityVersionsPublished != nil {
		s.metrics.MediaAuthorityVersionsPublished.WithLabelValues(authorityKind, cause).Inc()
	}
	if early && s.metrics != nil && s.metrics.MediaAuthorityEarlyRenewals != nil {
		s.metrics.MediaAuthorityEarlyRenewals.WithLabelValues(authorityKind).Inc()
	}
}

// renewedEarly reports a renewal published while the version it replaces had
// used less than a quarter of its validity. Renewal is due at a third (see
// mediaAuthorityRenewAt), so this is a scheduling defect, not load.
func (d mediaAuthorityPublication) renewedEarly(now time.Time) bool {
	if !d.publish || d.cause != mediaAuthorityCauseRenewal || !d.hasCurrent {
		return false
	}
	lifetime := d.current.ValidUntil.Sub(d.current.IssuedAt)
	return lifetime > 0 && now.Sub(d.current.IssuedAt) < lifetime/4
}
