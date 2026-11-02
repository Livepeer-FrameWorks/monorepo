package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

// ManagedStreamSet is one cluster's desired managed streams. Rows may start;
// Held, keyed by stream ID, may keep running where they already run but may not
// start, because their tenant's prepaid balance is spent. A held stream is
// neither desired nor omitted, so it leaves Complete untouched.
type ManagedStreamSet struct {
	Rows     []*commodorepb.ManagedStreamRow
	Held     map[string]*commodorepb.ManagedStreamRow
	Contexts map[string]*commodorepb.ResolveStreamContextResponse
	Marked   bool
	Complete bool
}

// HoldsStream reports whether a stream name or playback ID belongs to a held row.
func (s ManagedStreamSet) HoldsStream(streamName string) bool {
	for _, row := range s.Held {
		if row.GetInternalName() == streamName || strings.EqualFold(row.GetPlaybackId(), streamName) {
			return true
		}
	}
	return false
}

// unavailableRowWarnInterval spaces the Warn for one managed-stream row that a
// successful refetch left unreadable.
const unavailableRowWarnInterval = 10 * time.Minute

// unavailableManagedRows remembers the managed-stream rows the last desired-set
// read could not use. A row is logged at Debug until a refetch of its authority
// succeeds; if the row is still unusable after that, the cell cannot repair it
// by asking again, and it is logged at Warn once per interval.
type unavailableManagedRows struct {
	mu   sync.Mutex
	rows map[string]*unavailableManagedRow
}

type unavailableManagedRow struct {
	refetched bool
	warnedAt  time.Time
}

// note records authorityID as unavailable and reports whether this read
// should be logged at Warn.
func (u *unavailableManagedRows) note(authorityID string, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rows == nil {
		u.rows = map[string]*unavailableManagedRow{}
	}
	row := u.rows[authorityID]
	if row == nil {
		u.rows[authorityID] = &unavailableManagedRow{}
		return false
	}
	if !row.refetched || (!row.warnedAt.IsZero() && now.Sub(row.warnedAt) < unavailableRowWarnInterval) {
		return false
	}
	row.warnedAt = now
	return true
}

// refetched records that the control plane answered a refetch of authorityID.
func (u *unavailableManagedRows) refetched(authorityID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if row := u.rows[authorityID]; row != nil {
		row.refetched = true
	}
}

// retain keeps only the rows in seen, the ones a read through every listed row
// found unavailable.
func (u *unavailableManagedRows) retain(seen map[string]struct{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for authorityID := range u.rows {
		if _, ok := seen[authorityID]; !ok {
			delete(u.rows, authorityID)
		}
	}
}

type ManagedStreamPromotion string

const (
	ManagedStreamPromotionNone     ManagedStreamPromotion = ""
	ManagedStreamPromotionMatched  ManagedStreamPromotion = "matched"
	ManagedStreamPromotionMismatch ManagedStreamPromotion = "mismatch"
)

// ManagedStreamSetOutcomes are the bounded outcomes counted for each desired-set read.
var ManagedStreamSetOutcomes = []string{"complete", "incomplete", "unmarked", "error", "cancelled"}

// SetManagedStreamSetMetric records one outcome for each desired-set read.
func (s *Store) SetManagedStreamSetMetric(metric *prometheus.CounterVec) {
	if s != nil {
		s.managedStreamSets = metric
	}
}

// ManagedStreams returns a complete desired-state view only when every
// locally known mist_native object and its tenant projection carry the exact
// source-readiness marker. A partial set is never used for retract decisions.
func (s *Store) ManagedStreams(ctx context.Context, clusterID string) (result ManagedStreamSet, resultErr error) {
	result = ManagedStreamSet{
		Held:     make(map[string]*commodorepb.ManagedStreamRow),
		Contexts: make(map[string]*commodorepb.ResolveStreamContextResponse),
	}
	defer func() {
		if resultErr != nil {
			result.Complete = false
		}
		if s.managedStreamSets == nil {
			return
		}
		outcome := "complete"
		switch {
		case errors.Is(resultErr, context.Canceled), errors.Is(resultErr, context.DeadlineExceeded):
			outcome = "cancelled"
		case resultErr != nil:
			outcome = "error"
		case !result.Marked:
			outcome = "unmarked"
		case !result.Complete:
			outcome = "incomplete"
		}
		s.managedStreamSets.WithLabelValues(outcome).Inc()
	}()
	rows, err := foghorndb.New(s.db).ListLocalManagedStreamAuthorities(ctx)
	if err != nil {
		return result, fmt.Errorf("list local managed-stream authorities: %w", err)
	}
	if len(rows) == 0 {
		s.unavailableRows.retain(nil)
		return result, nil
	}
	result.Complete = true
	unavailable := make(map[string]struct{})
	rowUnavailable := func(authorityID string, cause error) {
		result.Complete = false
		unavailable[authorityID] = struct{}{}
		warn := s.unavailableRows.note(authorityID, s.now().UTC())
		if s.logger != nil {
			entry := s.logger.WithError(cause).WithField("authority_id", authorityID)
			if warn {
				entry.Warn("Managed source authority still unavailable after a successful refetch; preserving other tenants and disabling retraction")
			} else {
				entry.Debug("Managed source authority unavailable; preserving other tenants and disabling retraction")
			}
		}
		s.refreshAsyncThen(AuthorityLookup{AuthorityID: authorityID}, func(err error) {
			if err == nil {
				s.unavailableRows.refetched(authorityID)
			}
		})
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.Marked = result.Marked || row.LocalSourceReady
		object, err := decodeMediaObjectSnapshot(row.Payload, row.PayloadSha256, row.AuthorityID, row.AuthorityVersion, false, row.RefreshAfter, row.ValidUntil, s.now().UTC())
		if err != nil {
			rowUnavailable(row.AuthorityID, err)
			continue
		}
		object.ParentVersion = row.TenantAuthorityVersion
		// A tombstone remains a deny after its envelope expires. It needs no
		// parent, secret or renewal and cannot make a desired set incomplete.
		if object.Authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
			continue
		}
		object.SourceReady = row.LocalSourceReady
		object.Freshness = s.fencedFreshness(object.Freshness, "media_object", row.AuthorityID, row.WithheldByTenantRevival)
		if !object.SourceReady {
			result.Complete = false
			continue
		}
		// One authority past its validity makes the set incomplete; it does not
		// fail the whole cluster's set. An incomplete set is never used to retract
		// anything, and the caller asks the control plane while it is reachable.
		// The authority is asked for again so the set completes by itself.
		if object.Freshness == FreshnessHardExpired {
			result.Complete = false
			s.refreshAsync(AuthorityLookup{AuthorityID: row.AuthorityID})
			continue
		}
		authority := object.Authority
		if authority == nil || authority.GetLiveStream() == nil {
			rowUnavailable(row.AuthorityID, fmt.Errorf("managed-stream authority %q has no live-stream payload", row.AuthorityID))
			continue
		}
		tenant, err := s.TenantSourceForObject(ctx, object)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if errors.Is(err, sql.ErrNoRows) {
			result.Complete = false
			s.refreshAsync(AuthorityLookup{AuthorityID: row.AuthorityID})
			continue
		}
		if err != nil {
			var readErr *authorityStoreReadError
			if errors.As(err, &readErr) {
				return result, err
			}
			rowUnavailable(row.AuthorityID, fmt.Errorf("load managed-stream tenant %q: %w", authority.GetTenantId(), err))
			continue
		}
		if !tenant.SourceReady {
			result.Complete = false
			continue
		}
		if tenant.Freshness == FreshnessHardExpired {
			result.Complete = false
			s.refreshAsync(AuthorityLookup{AuthorityID: row.AuthorityID})
			continue
		}
		// A spent prepaid balance is signed without cluster grants, so where the
		// source may run comes from its sealed secret alone, and the row is held
		// rather than desired: omitting it would retract a running source.
		spent := authority.GetLifecycle() == mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE &&
			sharedauthority.PrepaidBalanceSpent(tenant.Authority)
		if !spent {
			if authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
				tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
				tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
				continue
			}
			if !tenantAllowsCluster(tenant.Authority, clusterID) {
				continue
			}
		}
		secret, err := s.OpenLiveStreamSecret(object)
		if err != nil {
			rowUnavailable(row.AuthorityID, fmt.Errorf("open managed-stream authority %q: %w", row.AuthorityID, err))
			continue
		}
		if strings.TrimSpace(secret.GetNativeSourceSpec()) == "" || !slices.Contains(secret.GetNativeAllowedClusterIds(), clusterID) {
			continue
		}
		live := authority.GetLiveStream()
		managed := &commodorepb.ManagedStreamRow{
			StreamId: live.GetStreamId(), PlaybackId: authority.GetPlaybackId(), InternalName: authority.GetInternalName(),
			TenantId: authority.GetTenantId(), IngestMode: live.GetIngestMode(), SourceSpec: secret.GetNativeSourceSpec(),
			SourceKind: secret.GetNativeSourceKind(), AlwaysOn: secret.GetNativeAlwaysOn(), PlacementCount: secret.GetNativePlacementCount(),
			AllowedClusterIds: append([]string(nil), secret.GetNativeAllowedClusterIds()...),
		}
		// An always-on stream is in use for as long as it is configured, whether
		// or not anyone is watching: it has to keep being renewed, or the set it
		// is elected from would lose it. An on-demand one is in use when its
		// source is resolved.
		if managed.GetAlwaysOn() {
			s.NoteMediaObjectUse(object)
		}
		if spent {
			result.Held[live.GetStreamId()] = managed
			continue
		}
		result.Rows = append(result.Rows, managed)
		origin, official := authority.GetOriginClusterId(), tenant.Authority.GetOfficialClusterId()
		result.Contexts[live.GetStreamId()] = &commodorepb.ResolveStreamContextResponse{
			Admitted: true, StreamId: live.GetStreamId(), PlaybackId: authority.GetPlaybackId(), InternalName: authority.GetInternalName(),
			IngestMode: live.GetIngestMode(), TenantId: authority.GetTenantId(), UserId: authority.GetUserId(),
			IsRecordingEnabled: live.GetRecordingEnabled(), RequiresAuth: authority.GetPlaybackPolicy().GetKind() != mediaauthoritypb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC,
			BillingModel: localBillingModel(tenant.Authority.GetBillingModel()), OriginClusterId: &origin, OfficialClusterId: &official,
			ClusterPeers: s.RoutingClusterPeers(tenant.Authority, clusterID), AuthorityClusterPeers: TenantClusterPeers(tenant.Authority), ProcessesJson: live.GetProcessesJson(), DvrProcessesJson: live.GetDvrProcessesJson(),
			DvrPolicy: tenant.Authority.GetDvrPolicy(), Allowances: tenant.Authority.GetAllowances(), TenantResourceLimits: clusterResourceLimits(tenant.Authority, clusterID),
		}
	}
	s.unavailableRows.retain(unavailable)
	return result, nil
}

func (s *Store) PromoteManagedStreamIfMatching(ctx context.Context, clusterID string, connected *commodorepb.ManagedStreamRow, streamCtx *commodorepb.ResolveStreamContextResponse) (ManagedStreamPromotion, error) {
	if s == nil || connected == nil || streamCtx == nil || !streamCtx.GetAdmitted() {
		return ManagedStreamPromotionNone, nil
	}
	object, err := s.MediaObjectSourceByInternalName(ctx, connected.GetInternalName())
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedStreamPromotionNone, nil
	}
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	tenant, err := s.TenantSourceForObject(ctx, object)
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	if object.SourceReady {
		return ManagedStreamPromotionNone, nil
	}
	if object.Freshness == FreshnessHardExpired || tenant.Freshness == FreshnessHardExpired {
		return ManagedStreamPromotionMismatch, nil
	}
	if !ShadowComparable(tenant.Authority, object.Authority) {
		return ManagedStreamPromotionMismatch, nil
	}
	secret, err := s.OpenLiveStreamSecret(object)
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	wantAllowed := append([]string(nil), secret.GetNativeAllowedClusterIds()...)
	gotAllowed := append([]string(nil), connected.GetAllowedClusterIds()...)
	slices.Sort(wantAllowed)
	slices.Sort(gotAllowed)
	authority, live := object.Authority, object.Authority.GetLiveStream()
	localLimits := clusterResourceLimits(tenant.Authority, clusterID)
	if authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE ||
		tenant.Authority.GetBillingDecision() != mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW ||
		live.GetStreamId() != connected.GetStreamId() || authority.GetPlaybackId() != connected.GetPlaybackId() ||
		authority.GetInternalName() != connected.GetInternalName() || authority.GetTenantId() != connected.GetTenantId() ||
		live.GetIngestMode() != connected.GetIngestMode() || secret.GetNativeSourceSpec() != connected.GetSourceSpec() ||
		secret.GetNativeSourceKind() != connected.GetSourceKind() || secret.GetNativeAlwaysOn() != connected.GetAlwaysOn() ||
		secret.GetNativePlacementCount() != connected.GetPlacementCount() || !slices.Equal(wantAllowed, gotAllowed) ||
		streamCtx.GetStreamId() != live.GetStreamId() || streamCtx.GetTenantId() != authority.GetTenantId() ||
		streamCtx.GetUserId() != authority.GetUserId() || streamCtx.GetInternalName() != authority.GetInternalName() ||
		streamCtx.GetPlaybackId() != authority.GetPlaybackId() || streamCtx.GetIngestMode() != live.GetIngestMode() ||
		streamCtx.GetIsRecordingEnabled() != live.GetRecordingEnabled() || streamCtx.GetProcessesJson() != live.GetProcessesJson() ||
		streamCtx.GetDvrProcessesJson() != live.GetDvrProcessesJson() || streamCtx.GetBillingModel() != localBillingModel(tenant.Authority.GetBillingModel()) ||
		streamCtx.GetOfficialClusterId() != tenant.Authority.GetOfficialClusterId() || !proto.Equal(streamCtx.GetDvrPolicy(), tenant.Authority.GetDvrPolicy()) ||
		!sharedauthority.SameResourceLimits(streamCtx.GetTenantResourceLimits(), localLimits) ||
		!sharedauthority.SameTenantClusterGrants(tenant.Authority, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SamePreferredCluster(tenant.Authority, streamCtx.GetAuthorityClusterPeers()) ||
		!sharedauthority.SameAllowances(streamCtx.GetAllowances(), tenant.Authority.GetAllowances()) {
		return ManagedStreamPromotionMismatch, nil
	}
	var marked bool
	if tenant.SourceReady {
		marked, err = s.MarkMediaObjectLocalSourceReady(ctx, authority.GetTenantId(), object.AuthorityID, object.Version)
	} else {
		marked, err = s.MarkSourcePairLocalReady(ctx, authority.GetTenantId(), tenant.Version, object.AuthorityID, object.Version)
	}
	if err != nil {
		return ManagedStreamPromotionNone, err
	}
	if marked {
		return ManagedStreamPromotionMatched, nil
	}
	return ManagedStreamPromotionNone, nil
}

func tenantAllowsCluster(tenant *mediaauthoritypb.TenantAuthority, clusterID string) bool {
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		if grant.GetClusterId() == strings.TrimSpace(clusterID) {
			return true
		}
	}
	return false
}

func clusterResourceLimits(tenant *mediaauthoritypb.TenantAuthority, clusterID string) *tenantlimitspb.TenantResourceLimits {
	return sharedauthority.EffectiveResourceLimits(tenant, clusterID)
}

func localBillingModel(value mediaauthoritypb.TenantBillingModel) string {
	if value == mediaauthoritypb.TenantBillingModel_TENANT_BILLING_MODEL_PREPAID {
		return "prepaid"
	}
	return "postpaid"
}
