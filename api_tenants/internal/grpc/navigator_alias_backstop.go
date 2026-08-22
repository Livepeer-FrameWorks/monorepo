package grpc

import (
	"context"
	"slices"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
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
	tenantID  string
	subdomain string
	want      bool
}

func (s *QuartermasterServer) reconcileTenantAliasesOnce(ctx context.Context) {
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
// tenant wants an alias iff it is active on an alias-eligible monthly tier
// AND holds at least one active cluster subscription — the same condition the
// primary ensure/remove paths converge to, so the backstop never fights them.
func (s *QuartermasterServer) listDesiredTenantAliases(ctx context.Context) ([]tenantAliasDesired, error) {
	rows, err := quartermasterdb.New(s.db).ListDesiredTenantAliases(ctx)
	if err != nil {
		return nil, err
	}
	desired := make([]tenantAliasDesired, 0, len(rows))
	for _, row := range rows {
		desired = append(desired, tenantAliasDesired{tenantID: row.TenantID, subdomain: row.Subdomain, want: row.Want})
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

	want := d.want && d.subdomain != ""
	found := statusResp.GetFound()
	activeLabel := statusResp.GetSubdomain()
	pending := statusResp.GetPendingRetirements()

	type aliasAct struct {
		subdomain, action, reason string
	}
	var acts []aliasAct
	switch {
	case want && !found:
		acts = append(acts, aliasAct{d.subdomain, "ensure", "backstop_missing"})
	case want && found && activeLabel != d.subdomain:
		// Drift: Navigator's active label differs from intent. Retire the old
		// label (unless already in flight) and ensure the current one.
		if activeLabel != "" && !slices.Contains(pending, activeLabel) {
			acts = append(acts, aliasAct{activeLabel, "retire", "backstop_mismatch"})
		}
		acts = append(acts, aliasAct{d.subdomain, "ensure", "backstop_mismatch"})
	case !want && found:
		acts = append(acts, aliasAct{"", "remove", "backstop_undesired"})
	}
	if len(acts) == 0 {
		return false
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.logger.WithError(err).WithField("tenant_id", d.tenantID).Warn("tenant-alias backstop: begin tx failed")
		return false
	}
	defer tx.Rollback() //nolint:errcheck
	for _, a := range acts {
		if _, enqErr := s.EnqueueNavigatorTenantAliasTx(ctx, tx, d.tenantID, a.subdomain, a.action, "", a.reason); enqErr != nil {
			s.logger.WithError(enqErr).WithField("tenant_id", d.tenantID).Warn("tenant-alias backstop: enqueue failed")
			return false
		}
	}
	if commitErr := tx.Commit(); commitErr != nil {
		s.logger.WithError(commitErr).WithField("tenant_id", d.tenantID).Warn("tenant-alias backstop: commit failed")
		return false
	}
	return true
}

func (s *QuartermasterServer) tenantAliasOutboxHasPending(ctx context.Context, tenantID string) (bool, error) {
	return quartermasterdb.New(s.db).TenantAliasOutboxHasPending(ctx, tenantID)
}
