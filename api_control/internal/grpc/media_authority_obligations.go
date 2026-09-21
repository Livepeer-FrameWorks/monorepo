package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
)

const (
	mediaAuthorityDeadlineTimeout       = 12 * time.Second
	mediaAuthorityDeadlineLease         = 20 * time.Second
	tenantMediaAuthorityDeadlineTimeout = 25 * time.Second
	tenantMediaAuthorityDeadlineLease   = 35 * time.Second

	// A transient failure that survives this many attempts is parked. The
	// reconciler re-arms parked targets, so a long outage costs one attempt per
	// reconcile pass instead of one per backoff period.
	mediaAuthorityMaxTransientAttempts = 12
	// How long a renewal that lost the compile fence waits before it runs again.
	mediaAuthoritySupersededRenewalDelay = 30 * time.Second
	mediaAuthorityLegacyAdoptBatch       = 200
	mediaAuthorityLegacyAdoptIdleTicks   = 30
	mediaAuthorityRenewalRepairBatch     = 500
	mediaAuthorityRenewalRepairPasses    = 400
)

type authorityCompileClass int

const (
	authorityCompileTransient authorityCompileClass = iota
	authorityCompilePark
	authorityCompileSuperseded
	authorityCompileAwaitTenant
)

// authorityCompileError classifies why a compile did not publish. Unclassified
// errors are transient.
type authorityCompileError struct {
	class authorityCompileClass
	code  string
	err   error
}

func (e *authorityCompileError) Error() string { return e.err.Error() }
func (e *authorityCompileError) Unwrap() error { return e.err }

// parkAuthorityCompile marks a failure that retrying cannot fix: the source
// state or static configuration has to change first.
func parkAuthorityCompile(code string, err error) error {
	return &authorityCompileError{class: authorityCompilePark, code: code, err: err}
}

var (
	// errMediaAuthorityCompileSuperseded reports that a newer compile of the same
	// authority started first. That compile reads state at least as new, so this
	// one has nothing left to publish.
	errMediaAuthorityCompileSuperseded = errors.New("media-authority compile superseded by a newer compile")
	// errTenantAuthorityMissing reports a media object whose tenant has no
	// published authority to derive from.
	errTenantAuthorityMissing = errors.New("tenant has no current media authority")
)

func classifyAuthorityCompileError(err error) (authorityCompileClass, string) {
	var typed *authorityCompileError
	switch {
	case errors.As(err, &typed):
		return typed.class, typed.code
	case errors.Is(err, errMediaAuthorityCompileSuperseded):
		return authorityCompileSuperseded, "superseded"
	case errors.Is(err, errTenantAuthorityMissing):
		return authorityCompileAwaitTenant, "awaiting_tenant_authority"
	case errors.Is(err, sharedauthority.ErrMalformed), errors.Is(err, sharedauthority.ErrUnknownSchema):
		return authorityCompilePark, "malformed_authority"
	default:
		return authorityCompileTransient, "transient"
	}
}

type mediaAuthorityObligationLaneContextKey struct{}

// mediaAuthorityCompileOutcome lets the publishing transaction report back to
// the worker that claimed the obligation.
type mediaAuthorityCompileOutcome struct {
	published bool
	// dormant reports a compile that found the authority unused and published
	// nothing. On a renewal lane that ends the renewal until it is used again.
	dormant bool
}

type mediaAuthorityCompileOutcomeContextKey struct{}

func mediaAuthorityObligationLane(ctx context.Context) string {
	lane, _ := ctx.Value(mediaAuthorityObligationLaneContextKey{}).(string) //nolint:errcheck // absent means a compile outside the obligation worker
	return lane
}

func isMediaAuthorityDeadlineLane(lane string) bool {
	return lane == commodoredb.MediaAuthorityLaneObjectDeadline || lane == commodoredb.MediaAuthorityLaneTenantDeadline
}

func markMediaAuthorityDormant(ctx context.Context) {
	if outcome, ok := ctx.Value(mediaAuthorityCompileOutcomeContextKey{}).(*mediaAuthorityCompileOutcome); ok && outcome != nil {
		outcome.dormant = true
	}
}

func markMediaAuthorityPublished(ctx context.Context) {
	if outcome, ok := ctx.Value(mediaAuthorityCompileOutcomeContextKey{}).(*mediaAuthorityCompileOutcome); ok && outcome != nil {
		outcome.published = true
	}
}

func (s *CommodoreServer) processMediaAuthorityEventObligations(ctx context.Context) {
	s.processMediaAuthorityObligationLane(ctx, commodoredb.MediaAuthorityLaneEvent, mediaAuthorityLease, mediaAuthorityRefreshTimeout)
}

// processMediaAuthorityBulkObligations compiles fan-out and reconciliation work.
// It has its own lane and workers so that a tenant change touching thousands of
// objects never stands in front of a change to one of them.
func (s *CommodoreServer) processMediaAuthorityBulkObligations(ctx context.Context) {
	s.processMediaAuthorityObligationLane(ctx, commodoredb.MediaAuthorityLaneBulk, mediaAuthorityLease, mediaAuthorityRefreshTimeout)
}

func (s *CommodoreServer) processMediaAuthorityObjectRenewals(ctx context.Context) {
	s.processMediaAuthorityObligationLane(ctx, commodoredb.MediaAuthorityLaneObjectDeadline, mediaAuthorityDeadlineLease, mediaAuthorityDeadlineTimeout)
}

func (s *CommodoreServer) processMediaAuthorityTenantRenewals(ctx context.Context) {
	s.processMediaAuthorityObligationLane(ctx, commodoredb.MediaAuthorityLaneTenantDeadline, tenantMediaAuthorityDeadlineLease, tenantMediaAuthorityDeadlineTimeout)
}

// processMediaAuthorityObligationLane drains one lane. A single claimant feeds a
// fixed number of compile slots and claims again as soon as a slot frees, so the
// lane's rate is what the compiles take, not one batch per tick, and one slow
// compile holds one slot instead of the next claim. It returns when the lane is
// empty; the worker's ticker starts the next pass.
//
// Every claim carries its own memo of per-tenant compile inputs. The memo is
// created before the claim, so everything it holds was read after the rows it
// serves were enqueued, and it is dropped with them: a tenant's objects claimed
// together share one read of what they all derive from, and no read can outlive
// the change that enqueued the next rows.
func (s *CommodoreServer) processMediaAuthorityObligationLane(ctx context.Context, lane string, lease, timeout time.Duration) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	completed := make(chan struct{}, mediaAuthorityRefreshWorkers)
	running := 0
	wait := func() {
		for running > 0 {
			<-completed
			running--
		}
	}
	for {
		if ctx.Err() != nil {
			wait()
			return
		}
		available := mediaAuthorityRefreshWorkers - running
		if available == 0 {
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		memo := newMediaAuthorityCompileMemo()
		rows, err := commodoredb.New(s.db).ClaimMediaAuthorityObligations(ctx, commodoredb.ClaimMediaAuthorityObligationsParams{
			LeaseMs: lease.Milliseconds(), Lane: lane, BatchSize: int32(available),
		})
		if err != nil {
			if ctx.Err() == nil {
				s.logger.WithError(err).WithField("lane", lane).Warn("Failed to claim media authority refresh obligations")
			}
			wait()
			return
		}
		if len(rows) == 0 {
			if running == 0 {
				return
			}
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		for _, row := range rows {
			running++
			go func() {
				defer func() { completed <- struct{}{} }()
				rowCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				outcome := &mediaAuthorityCompileOutcome{}
				rowCtx = context.WithValue(rowCtx, mediaAuthorityObligationLaneContextKey{}, row.Lane)
				rowCtx = context.WithValue(rowCtx, mediaAuthorityCompileOutcomeContextKey{}, outcome)
				rowCtx = withMediaAuthorityCompileMemo(rowCtx, memo)
				s.settleMediaAuthorityObligation(row, outcome, s.compileMediaAuthorityObligation(rowCtx, row))
			}()
		}
	}
}

func (s *CommodoreServer) compileMediaAuthorityObligation(ctx context.Context, row commodoredb.ClaimMediaAuthorityObligationsRow) error {
	target := commodoredb.MediaAuthorityTarget{Key: row.TargetKey, Kind: row.TargetKind}
	switch row.TargetKind {
	case commodoredb.MediaAuthorityTargetTenantMediaObjects:
		// Fanout publishes nothing and must not fence out a concurrent tenant
		// compile while it enumerates dependent objects.
		return s.enqueueTenantMediaObjectRefreshes(ctx, row.TenantID)
	case commodoredb.MediaAuthorityTargetTenant:
		return s.withMediaAuthorityCompileFence(ctx, row.TargetKey, func(compileCtx context.Context) error {
			return s.compileTenantAuthority(compileCtx, row.TenantID)
		})
	case commodoredb.MediaAuthorityTargetLiveStream:
		return s.withMediaAuthorityCompileFence(ctx, row.TargetKey, func(compileCtx context.Context) error {
			return s.compileLiveStreamAuthority(compileCtx, target.ObjectID())
		})
	case commodoredb.MediaAuthorityTargetArtifact:
		return s.withMediaAuthorityCompileFence(ctx, row.TargetKey, func(compileCtx context.Context) error {
			return s.compileArtifactAuthority(compileCtx, target.ObjectID())
		})
	default:
		return parkAuthorityCompile("unsupported_target", fmt.Errorf("unsupported media-authority target kind %q", row.TargetKind))
	}
}

// enqueueTenantMediaObjectRefreshes refreshes the tenant's objects that cells
// still hold, in one statement, so a fanout is never half applied. The work goes
// to the bulk lane: a tenant with many objects must not stand in front of a
// change or a revocation waiting in the event lane. An object no cell holds a
// valid copy of has nothing to correct and is compiled when it is next used.
func (s *CommodoreServer) enqueueTenantMediaObjectRefreshes(ctx context.Context, tenantID string) error {
	if _, err := commodoredb.New(s.db).EnqueueTenantMediaObjectRefreshes(ctx, commodoredb.EnqueueTenantMediaObjectRefreshesParams{
		TenantID: tenantID, Reason: "tenant_media_objects:fanout", SourceEventID: "tenant-fanout:" + tenantID,
	}); err != nil {
		return fmt.Errorf("enqueue tenant media-object refreshes: %w", err)
	}
	return nil
}

// settleMediaAuthorityObligation runs on a detached context: the row's own
// context may already have expired, and an unsettled row waits out its lease.
func (s *CommodoreServer) settleMediaAuthorityObligation(row commodoredb.ClaimMediaAuthorityObligationsRow, outcome *mediaAuthorityCompileOutcome, compileErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), mediaAuthoritySettleTimeout)
	defer cancel()
	queries := commodoredb.New(s.db)
	fields := logging.Fields{"lane": row.Lane, "target": row.TargetKey, "tenant_id": row.TenantID, "attempts": row.Attempts}

	var (
		settled  int64
		err      error
		observed string
	)
	class, code := authorityCompileTransient, ""
	if compileErr != nil {
		class, code = classifyAuthorityCompileError(compileErr)
	}
	switch {
	case class == authorityCompileSuperseded:
		// Another compile of the same authority started after this one. It is
		// not known to have published: it can still fail (a fetch's source read,
		// its deadline), and nothing would compile the change this row carries
		// again. So the row stays pending and runs once more after the other
		// compile is out of the way; if that one published, this run is a no-op.
		// A renewal additionally has to keep the schedule alive.
		observed = "superseded"
		settled, err = queries.FailMediaAuthorityObligation(ctx, commodoredb.FailMediaAuthorityObligationParams{
			NextAttemptAt: time.Now().Add(mediaAuthoritySupersededRenewalDelay),
			LastError:     sql.NullString{String: compileErr.Error(), Valid: true},
			TargetKey:     row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
		})
	case compileErr == nil && outcome != nil && outcome.dormant && isMediaAuthorityDeadlineLane(row.Lane):
		// Nobody uses the authority, so it is not renewed and the copies cells
		// hold run out. The row stays as the authority's renewal: use, or the
		// next publication, revives it.
		observed = "dormant"
		settled, err = queries.SettleDormantMediaAuthorityObligation(ctx, commodoredb.SettleDormantMediaAuthorityObligationParams{
			TargetKey: row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
		})
	case compileErr == nil:
		observed = "noop"
		if outcome != nil && outcome.published {
			observed = "completed"
		}
		settled, err = queries.CompleteMediaAuthorityObligation(ctx, commodoredb.CompleteMediaAuthorityObligationParams{
			TargetKey: row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
		})
	case class == authorityCompileAwaitTenant:
		// The object becomes compilable once its tenant holds a valid authority.
		// Every tenant compile, whether it publishes or not, re-arms the objects
		// parked waiting for it, so the object is parked and the tenant compile
		// requested in one transaction, parked first: the compile can only start
		// once the park is visible to it.
		observed = "parked"
		// Only an object that is wanted gets here, and a tenant is in use when one
		// of its objects is: without this the tenant compile would find a tenant
		// nobody uses and publish nothing, and the object would wait forever.
		if useErr := s.recordMediaAuthorityUse(ctx, "tenant", row.TenantID, row.TenantID); useErr != nil {
			s.logger.WithError(useErr).WithFields(fields).Warn("Failed to record use of the tenant authority a media object is waiting for")
		}
		err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			txQueries := commodoredb.New(tx)
			parked, parkErr := s.parkMediaAuthorityObligation(ctx, txQueries, row, code, compileErr)
			if parkErr != nil || parked == 0 {
				settled = parked
				return parkErr
			}
			settled = parked
			return txQueries.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(row.TenantID), row.TenantID,
				"media_object_awaiting_tenant_authority", "commodore", "awaiting-tenant:"+row.TenantID)
		})
	case class == authorityCompilePark:
		observed = "parked"
		settled, err = s.parkMediaAuthorityObligation(ctx, queries, row, code, compileErr)
		s.logger.WithError(compileErr).WithFields(fields).WithField("park_reason", code).Warn("Parked media authority refresh: the target cannot compile until its source state changes")
	case row.Attempts >= mediaAuthorityMaxTransientAttempts:
		observed = "parked"
		settled, err = s.parkMediaAuthorityObligation(ctx, queries, row, "exhausted", compileErr)
		s.logger.WithError(compileErr).WithFields(fields).WithField("park_reason", "exhausted").Warn("Parked media authority refresh after repeated transient failures")
	default:
		observed = "transient"
		settled, err = queries.FailMediaAuthorityObligation(ctx, commodoredb.FailMediaAuthorityObligationParams{
			NextAttemptAt: time.Now().Add(authorityBackoff(row.Attempts, row.Lane, row.TargetKey)),
			LastError:     sql.NullString{String: compileErr.Error(), Valid: true},
			TargetKey:     row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
		})
		if row.Attempts == 1 {
			s.logger.WithError(compileErr).WithFields(fields).Warn("Media authority refresh failed; retrying with backoff")
		}
	}
	if err == nil && settled == 0 {
		// A newer event folded into the row while it was compiling and already
		// re-armed it; only the lease it preserved is left to clear.
		_, err = queries.ReleaseSupersededMediaAuthorityObligation(ctx, commodoredb.ReleaseSupersededMediaAuthorityObligationParams{
			TargetKey: row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
		})
	}
	// The compile is over whatever came of it, so another lane may take the
	// target now. A release that fails costs a wait for the claim's lease. A
	// worker whose lease lapsed and whose target was claimed again settles and
	// releases nothing: every write here requires its claim token.
	if releaseErr := queries.ReleaseMediaAuthorityTargetClaim(ctx, commodoredb.ReleaseMediaAuthorityTargetClaimParams{
		TargetKey: row.TargetKey, Lane: row.Lane, ClaimToken: row.ClaimToken,
	}); releaseErr != nil {
		s.logger.WithError(releaseErr).WithFields(fields).Warn("Failed to release the claim on a media authority target")
	}
	if err != nil {
		s.logger.WithError(err).WithFields(fields).Error("Failed to settle media authority refresh obligation")
		return
	}
	if s.metrics != nil && s.metrics.MediaAuthorityRefreshSettlements != nil {
		s.metrics.MediaAuthorityRefreshSettlements.WithLabelValues(row.Lane, observed).Inc()
	}
}

func (s *CommodoreServer) parkMediaAuthorityObligation(ctx context.Context, queries *commodoredb.Queries, row commodoredb.ClaimMediaAuthorityObligationsRow, code string, cause error) (int64, error) {
	return queries.ParkMediaAuthorityObligation(ctx, commodoredb.ParkMediaAuthorityObligationParams{
		ParkReason: sql.NullString{String: code, Valid: true},
		LastError:  sql.NullString{String: cause.Error(), Valid: true},
		TargetKey:  row.TargetKey, Lane: row.Lane, Revision: row.Revision, ClaimToken: row.ClaimToken,
	})
}

// mediaAuthorityLegacyAdopter folds unfinished rows of the refresh inbox into
// obligations. It idles between empty passes so a drained inbox costs one probe
// every mediaAuthorityLegacyAdoptIdleTicks worker ticks.
type mediaAuthorityLegacyAdopter struct {
	idleTicks int
}

func (s *CommodoreServer) adoptLegacyMediaAuthorityRefreshInbox(ctx context.Context) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	if s.mediaAuthorityLegacyAdopter.idleTicks > 0 {
		s.mediaAuthorityLegacyAdopter.idleTicks--
		return
	}
	adopted := 0
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		rows, err := queries.AdoptLegacyMediaAuthorityRefreshInbox(ctx, mediaAuthorityLegacyAdoptBatch)
		if err != nil {
			return err
		}
		adopted = len(rows)
		for _, row := range rows {
			// Renewal rows of the inbox bound a version that the obligation
			// model schedules itself; their targets need no separate refresh.
			if strings.HasPrefix(row.SourceEventID, "authority-deadline:") || strings.HasPrefix(row.SourceEventID, "tenant-deadline-fanout:") {
				continue
			}
			target := commodoredb.MediaAuthorityTargetForReason(row.TenantID, row.Reason)
			if enqueueErr := queries.EnqueueMediaAuthorityEvent(ctx, target, row.TenantID, row.Reason, row.SourceService, row.SourceEventID); enqueueErr != nil {
				return enqueueErr
			}
			// The inbox fanned a tenant event out to the tenant's objects when it
			// completed; an adopted row has not completed, so that fanout is owed.
			if target.Kind == commodoredb.MediaAuthorityTargetTenant && (row.SourceService != "purser" || purserReasonChangesMediaObjects(row.SourceService, row.Reason)) {
				if enqueueErr := queries.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaObjectsAuthorityTarget(row.TenantID), row.TenantID,
					"tenant_media_objects:"+row.Reason, row.SourceService, row.SourceEventID); enqueueErr != nil {
					return enqueueErr
				}
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() == nil {
			s.logger.WithError(err).Warn("Failed to adopt legacy media authority refresh inbox rows")
		}
		return
	}
	if adopted == 0 {
		s.mediaAuthorityLegacyAdopter.idleTicks = mediaAuthorityLegacyAdoptIdleTicks
		return
	}
	s.logger.WithField("rows", adopted).Info("Folded legacy media authority refresh inbox rows into obligations")
}

// mediaAuthorityRenewAt is when a published version is renewed: a third of the
// way through its validity, and never after refresh_after. Every authority adds
// a fixed offset of its own, tenants included: authorities published together
// (a startup, a re-issue, a restored database) would otherwise renew together
// for as long as they live. While objects are still capped by their tenant's
// validity, one that renews before its tenant cannot extend yet and retries.
func mediaAuthorityRenewAt(target commodoredb.MediaAuthorityTarget, issuedAt, refreshAfter, validUntil time.Time) time.Time {
	validity := validUntil.Sub(issuedAt)
	renewAt := issuedAt.Add(validity / 3).Add(mediaAuthoritySpread(target.Key, validity/36))
	if renewAt.After(refreshAfter) {
		return refreshAfter
	}
	return renewAt
}

// mediaAuthorityRenewalRetryAt spaces out renewals that are due but cannot yet
// extend validity, so they do not spin on an unchanged upper bound. The offset
// keeps a tenant's objects, which hit the same bound together, from retrying
// together.
func mediaAuthorityRenewalRetryAt(targetKey string, now, issuedAt, validUntil time.Time) time.Time {
	delay := validUntil.Sub(issuedAt) / 24
	if delay < 30*time.Second {
		delay = 30 * time.Second
	}
	return now.Add(delay).Add(mediaAuthoritySpread(targetKey, delay/2))
}

func (s *CommodoreServer) observeMediaAuthorityObligationStats(ctx context.Context) {
	if s.metrics == nil || s.metrics.MediaAuthorityRefreshPending == nil || s.metrics.MediaAuthorityRefreshOldestPendingSeconds == nil || s.metrics.MediaAuthorityRefreshParked == nil {
		return
	}
	queries := commodoredb.New(s.db)
	if s.metrics.MediaAuthorityObservationTimestamp != nil {
		s.metrics.MediaAuthorityObservationTimestamp.WithLabelValues().Add(0)
	}
	lanes, err := authorityMetricQuery(ctx, queries.ListMediaAuthorityObligationStats)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to observe media authority refresh obligations")
		return
	}
	due := map[string]commodoredb.ListMediaAuthorityObligationStatsRow{}
	for _, row := range lanes {
		due[row.Lane] = row
	}
	for _, lane := range []string{commodoredb.MediaAuthorityLaneEvent, commodoredb.MediaAuthorityLaneBulk, commodoredb.MediaAuthorityLaneObjectDeadline, commodoredb.MediaAuthorityLaneTenantDeadline} {
		s.metrics.MediaAuthorityRefreshPending.WithLabelValues(lane).Set(float64(due[lane].DueCount))
		s.metrics.MediaAuthorityRefreshOldestPendingSeconds.WithLabelValues(lane).Set(due[lane].OldestDueSeconds)
	}
	if s.metrics.MediaAuthorityExpiredWarm != nil {
		expiredRows, expiredErr := authorityMetricQuery(ctx, queries.ListExpiredWarmMediaAuthorityCounts)
		if expiredErr != nil {
			s.logger.WithError(expiredErr).Warn("Failed to observe media authorities that expired while in use")
			return
		} else {
			expired := map[string]int64{}
			for _, row := range expiredRows {
				expired[row.Lane] = row.ExpiredCount
			}
			s.metrics.MediaAuthorityExpiredWarm.WithLabelValues("tenant").Set(float64(expired[commodoredb.MediaAuthorityLaneTenantDeadline]))
			s.metrics.MediaAuthorityExpiredWarm.WithLabelValues("media_object").Set(float64(expired[commodoredb.MediaAuthorityLaneObjectDeadline]))
		}
	}
	parkedRows, err := authorityMetricQuery(ctx, queries.ListParkedMediaAuthorityObligationCounts)
	if err != nil {
		s.logger.WithError(err).Warn("Failed to observe parked media authority refresh obligations")
		return
	}
	parked := map[string]int64{}
	for _, row := range parkedRows {
		parked[row.TargetKind] = row.ParkedCount
	}
	for _, kind := range []string{commodoredb.MediaAuthorityTargetTenant, commodoredb.MediaAuthorityTargetLiveStream, commodoredb.MediaAuthorityTargetArtifact, commodoredb.MediaAuthorityTargetTenantMediaObjects} {
		s.metrics.MediaAuthorityRefreshParked.WithLabelValues(kind).Set(float64(parked[kind]))
	}
	if s.metrics.MediaAuthorityObservationTimestamp != nil {
		s.metrics.MediaAuthorityObservationTimestamp.WithLabelValues().SetToCurrentTime()
	}
}

func authorityMetricQuery[T any](ctx context.Context, query func(context.Context) (T, error)) (T, error) {
	bounded, cancel := context.WithTimeout(ctx, mediaAuthorityStatsTimeout)
	defer cancel()
	return query(bounded)
}
