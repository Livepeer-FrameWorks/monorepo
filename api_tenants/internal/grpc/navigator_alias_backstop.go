package grpc

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

const tenantAliasBackstopInterval = 5 * time.Minute

// runTenantAliasBackstop periodically reconciles each tenant's intended alias
// state against Navigator's applied state and enqueues any missing or drifted
// transition into the same per-tenant-ordered outbox. It is a repair loop, not
// the primary path: every mutation already enqueues durably, so this only
// converges tenants whose intent never reached Navigator (e.g. an enqueue that
// never ran) or whose Navigator-side state has drifted.
func (s *QuartermasterServer) runTenantAliasBackstop(ctx context.Context) {
	if s.navigatorClient == nil {
		s.logger.Info("tenant-alias backstop disabled: no navigator client")
		return
	}
	ticker := time.NewTicker(tenantAliasBackstopInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileTenantAliasesOnce(ctx)
		}
	}
}

type tenantAliasDesired struct {
	tenantID   string
	subdomain  string
	want       bool
	clusterIDs []string
}

type tenantAliasBackstopAction struct {
	subdomain string
	clusterID string
	action    string
	reason    string
}

func (s *QuartermasterServer) reconcileTenantAliasesOnce(ctx context.Context) {
	defer s.reconcileTenantCustomDomainsOnce(ctx)
	desired, err := s.listDesiredTenantAliases(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("tenant-alias backstop: list tenants failed")
		return
	}

	repaired := 0
	for _, d := range desired {
		if s.reconcileOneTenantAlias(ctx, d) {
			repaired++
		}
	}
	if repaired > 0 {
		s.logger.WithField("repaired", repaired).Info("tenant-alias backstop enqueued repairs")
	}
}

// listDesiredTenantAliases computes each tenant's intended alias state. A
// tenant wants an alias iff Purser has explicitly observed an enabled
// custom-subdomain entitlement, the tenant is active, and it holds at least
// one active cluster subscription. This is the same condition the primary
// ensure/remove paths converge to, so the backstop never fights them.
func (s *QuartermasterServer) listDesiredTenantAliases(ctx context.Context) ([]tenantAliasDesired, error) {
	rows, err := quartermasterdb.New(s.db).ListDesiredTenantAliases(ctx)
	if err != nil {
		return nil, err
	}
	desired := make([]tenantAliasDesired, 0, len(rows))
	for _, row := range rows {
		clusterIDs := row.ClusterIds
		if !row.Want {
			clusterIDs = nil
		}
		desired = append(desired, tenantAliasDesired{
			tenantID: row.TenantID, subdomain: row.Subdomain, want: row.Want, clusterIDs: clusterIDs,
		})
	}
	return desired, nil
}

// reconcileOneTenantAlias compares one tenant's desired alias against
// Navigator's applied state and enqueues any missing transition. Returns true
// when it enqueued at least one repair. Tenants that already have a pending
// outbox row are skipped — they are either converging or operator-blocked, and
// re-enqueuing would only pile up behind the in-flight row.
func (s *QuartermasterServer) reconcileOneTenantAlias(ctx context.Context, d tenantAliasDesired) bool {
	hasPending, err := s.tenantAliasOutboxHasPending(ctx, d.tenantID)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", d.tenantID).Debug("tenant-alias backstop: pending check failed")
		return false
	}
	if hasPending {
		return false
	}

	statusResp, err := s.navigatorClient.GetTenantAliasStatus(ctx, &dnspb.GetTenantAliasStatusRequest{TenantId: d.tenantID})
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", d.tenantID).Debug("tenant-alias backstop: status lookup failed")
		return false
	}

	acts := tenantAliasBackstopActions(d, statusResp)
	if len(acts) == 0 {
		return false
	}

	var enqueueErr error
	txErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		enqueueErr = nil
		for _, a := range acts {
			if _, enqErr := s.EnqueueNavigatorTenantAliasTx(ctx, tx, d.tenantID, a.subdomain, a.action, a.clusterID, a.reason); enqErr != nil {
				enqueueErr = enqErr
				return enqErr
			}
		}
		return nil
	})
	if txErr != nil {
		if enqueueErr != nil {
			s.logger.WithError(enqueueErr).WithField("tenant_id", d.tenantID).Warn("tenant-alias backstop: enqueue failed")
		} else {
			s.logger.WithError(txErr).WithField("tenant_id", d.tenantID).Warn("tenant-alias backstop: commit failed")
		}
		return false
	}
	if s.metrics != nil && s.metrics.DNSBackstopRepairs != nil {
		for _, action := range acts {
			s.metrics.DNSBackstopRepairs.WithLabelValues("tenant_alias", action.action).Inc()
		}
	}
	return true
}

func tenantAliasBackstopActions(d tenantAliasDesired, statusResp *dnspb.GetTenantAliasStatusResponse) []tenantAliasBackstopAction {
	var acts []tenantAliasBackstopAction
	want := d.want && d.subdomain != ""
	found := statusResp.GetFound()
	activeLabel := statusResp.GetSubdomain()
	pending := statusResp.GetPendingRetirements()
	switch {
	case want && !found:
		acts = append(acts, tenantAliasBackstopAction{subdomain: d.subdomain, action: "ensure", reason: "backstop_missing"})
	case want && found && activeLabel != d.subdomain:
		// Drift: Navigator's active label differs from intent. Retire the old
		// label (unless already in flight) and ensure the current one.
		if activeLabel != "" && !slices.Contains(pending, activeLabel) {
			acts = append(acts, tenantAliasBackstopAction{subdomain: activeLabel, action: "retire", reason: "backstop_mismatch"})
		}
		acts = append(acts, tenantAliasBackstopAction{subdomain: d.subdomain, action: "ensure", reason: "backstop_mismatch"})
	case !want && found:
		acts = append(acts, tenantAliasBackstopAction{action: "remove", reason: "backstop_undesired"})
	}

	desiredClusters := make(map[string]struct{}, len(d.clusterIDs))
	for _, clusterID := range d.clusterIDs {
		desiredClusters[clusterID] = struct{}{}
	}
	appliedClusters := make(map[string]struct{}, len(statusResp.GetAuthorizedClusterIds()))
	for _, clusterID := range statusResp.GetAuthorizedClusterIds() {
		appliedClusters[clusterID] = struct{}{}
	}
	for clusterID := range desiredClusters {
		if _, applied := appliedClusters[clusterID]; !applied {
			acts = append(acts, tenantAliasBackstopAction{clusterID: clusterID, action: "ensure_cluster", reason: "backstop_cluster_missing"})
		}
	}
	for clusterID := range appliedClusters {
		if _, desired := desiredClusters[clusterID]; !desired {
			acts = append(acts, tenantAliasBackstopAction{clusterID: clusterID, action: "remove_cluster", reason: "backstop_cluster_undesired"})
		}
	}
	return acts
}

func (s *QuartermasterServer) tenantAliasOutboxHasPending(ctx context.Context, tenantID string) (bool, error) {
	return quartermasterdb.New(s.db).TenantAliasOutboxHasPending(ctx, tenantID)
}

type tenantCustomDomainDesired struct {
	tenantID string
	domain   string
	want     bool
}

func (s *QuartermasterServer) reconcileTenantCustomDomainsOnce(ctx context.Context) {
	rows, err := quartermasterdb.New(s.db).ListDesiredTenantCustomDomains(ctx)
	if err != nil {
		s.logger.WithError(err).Warn("custom-domain backstop: list tenants failed")
		return
	}
	repaired := 0
	for _, row := range rows {
		desired := tenantCustomDomainDesired{tenantID: row.TenantID, domain: row.CustomDomain, want: row.Want}
		pending, pendingErr := quartermasterdb.New(s.db).TenantCustomDomainOutboxHasPending(ctx, desired.tenantID)
		if pendingErr != nil || pending {
			continue
		}
		lookupDomain := tenantCustomDomainLookupDomain(desired)
		current, statusErr := s.navigatorClient.GetCustomDomainStatus(ctx, &dnspb.GetCustomDomainStatusRequest{
			TenantId: desired.tenantID,
			Domain:   lookupDomain,
		})
		if statusErr != nil {
			s.logger.WithError(statusErr).WithField("tenant_id", desired.tenantID).Warn("custom-domain backstop: status lookup failed")
			continue
		}
		action, domain := tenantCustomDomainBackstopTarget(desired, current)
		if action == "" {
			continue
		}
		if txErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			_, enqueueErr := s.EnqueueNavigatorCustomDomainTx(ctx, tx, desired.tenantID, domain, action)
			return enqueueErr
		}); txErr != nil {
			continue
		}
		if s.metrics != nil && s.metrics.DNSBackstopRepairs != nil {
			s.metrics.DNSBackstopRepairs.WithLabelValues("custom_domain", action).Inc()
		}
		repaired++
	}
	if repaired > 0 {
		s.logger.WithField("repaired", repaired).Info("custom-domain backstop enqueued repairs")
	}
}

func tenantCustomDomainLookupDomain(desired tenantCustomDomainDesired) string {
	if !desired.want {
		// Empty means "return one deterministic row". This is required to
		// drain every stale legacy row when the desired domain itself is empty
		// or no longer names the extra Navigator rows.
		return ""
	}
	return desired.domain
}

func tenantCustomDomainBackstopAction(desired tenantCustomDomainDesired, current *dnspb.GetCustomDomainStatusResponse) string {
	action, _ := tenantCustomDomainBackstopTarget(desired, current)
	return action
}

func tenantCustomDomainBackstopTarget(desired tenantCustomDomainDesired, current *dnspb.GetCustomDomainStatusResponse) (string, string) {
	found := current != nil && current.GetFound()
	status := ""
	if current != nil {
		status = current.GetStatus()
	}
	switch {
	case desired.want && (!found || status == "tearing_down"):
		return "ensure", desired.domain
	case !desired.want && found && status != "tearing_down":
		return "remove", current.GetDomain()
	default:
		return "", ""
	}
}
