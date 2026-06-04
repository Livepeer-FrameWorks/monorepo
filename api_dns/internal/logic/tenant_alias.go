package logic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
)

// EnsureTenantAlias persists alias intent and queues the cert issuance.
// Idempotent: re-running with the same (tenantID, subdomain) is a
// no-op except for refreshing updated_at. Returns the row state for
// immediate display to callers.
//
// The actual ACME work happens asynchronously in the alias intent
// worker (see ProcessPendingTenantAliases). Callers should NOT block on
// cert issuance; paid tier activation must not depend on ACME latency.
func (m *CertManager) EnsureTenantAlias(ctx context.Context, tenantID, subdomain string) (*store.TenantAlias, error) {
	tenantID = strings.TrimSpace(tenantID)
	subdomain = strings.TrimSpace(strings.ToLower(subdomain))
	if tenantID == "" || subdomain == "" {
		return nil, fmt.Errorf("tenantID and subdomain are required")
	}
	return m.store.EnsureTenantAlias(ctx, tenantID, subdomain)
}

// GetTenantAlias returns the current alias state for a tenant, or
// ErrNotFound if the tenant has no alias intent.
func (m *CertManager) GetTenantAlias(ctx context.Context, tenantID string) (*store.TenantAlias, error) {
	return m.store.GetTenantAlias(ctx, tenantID)
}

// RemoveTenantAlias tears down a tenant alias. Sets status to
// tearing_down so the worker can clean up DNS + cert distribution.
// On a follow-up cycle the row is deleted.
//
// Idempotent: removing an alias that doesn't exist returns nil.
func (m *CertManager) RemoveTenantAlias(ctx context.Context, tenantID string) error {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return fmt.Errorf("tenantID is required")
	}
	// Mark for teardown first so the alias DNS worker stops
	// publishing DNS. Actual deletion happens after teardown reconciles.
	if err := m.store.SetTenantAliasStatus(ctx, tenantID, "tearing_down", ""); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// RemoveTenantAliasSubdomain retires one specific label without touching
// the tenant's active alias intent row. Quartermaster calls this for the
// old label on a subdomain rename; the alias worker clears that label's
// Bunny records and deletes the retirement. Idempotent on (tenantID,
// subdomain): a duplicate keeps the original requested_at.
func (m *CertManager) RemoveTenantAliasSubdomain(ctx context.Context, tenantID, subdomain string) error {
	tenantID = strings.TrimSpace(tenantID)
	subdomain = strings.TrimSpace(strings.ToLower(subdomain))
	if tenantID == "" || subdomain == "" {
		return fmt.Errorf("tenantID and subdomain are required")
	}
	return m.store.InsertTenantAliasRetirement(ctx, tenantID, subdomain)
}

// ListTenantAliasRetirementLabels returns the pending retirement labels for
// a tenant. The Quartermaster backstop reads this to avoid re-enqueuing a
// retire already in flight.
func (m *CertManager) ListTenantAliasRetirementLabels(ctx context.Context, tenantID string) ([]string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenantID is required")
	}
	return m.store.ListTenantAliasRetirementLabels(ctx, tenantID)
}

// RemoveTenantAliasCluster removes DNS eligibility for a tenant's edges
// in one cluster. Quartermaster calls this when cluster access is removed;
// DNS reconciliation publishes the remaining edge set before Foghorn drops
// the cert from future ConfigSeeds.
func (m *CertManager) RemoveTenantAliasCluster(ctx context.Context, tenantID, clusterID string) error {
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if tenantID == "" || clusterID == "" {
		return fmt.Errorf("tenantID and clusterID are required")
	}
	return m.store.DeleteTenantEdgeApplyStateForCluster(ctx, tenantID, clusterID)
}

// TenantAliasDNSReady reports whether a tenant alias has at least one
// edge currently published in DNS.
func (m *CertManager) TenantAliasDNSReady(ctx context.Context, tenantID string) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return false, fmt.Errorf("tenantID is required")
	}
	return m.store.TenantAliasHasDNS(ctx, tenantID)
}

// RecordConfigSeedApplyResult persists Helmsman ConfigSeed ACKs that
// Foghorn observed. Only tenant bundles drive tenant alias DNS state;
// cluster and platform bundles are ignored here.
func (m *CertManager) RecordConfigSeedApplyResult(ctx context.Context, nodeID, clusterID string, seedVersion uint64, appliedBundleIDs, failedBundleIDs []string, appliedAt time.Time) ([]string, error) {
	nodeID = strings.TrimSpace(nodeID)
	clusterID = strings.TrimSpace(clusterID)
	if nodeID == "" {
		return nil, fmt.Errorf("nodeID is required")
	}
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	}
	seen := map[string]struct{}{}
	record := func(bundleID, state string) error {
		tenantID, ok := tenantIDFromBundleID(bundleID)
		if !ok {
			return nil
		}
		seen[tenantID] = struct{}{}
		return m.store.UpsertTenantEdgeApplyState(ctx, &store.TenantEdgeApplyState{
			TenantID:        tenantID,
			ClusterID:       clusterID,
			NodeID:          nodeID,
			BundleID:        bundleID,
			State:           state,
			LastSeedVersion: sql.NullInt64{Valid: true, Int64: int64(seedVersion)},
			LastAckAt:       sql.NullTime{Valid: true, Time: appliedAt.UTC()},
		})
	}
	for _, bundleID := range appliedBundleIDs {
		if err := record(bundleID, "applied"); err != nil {
			return nil, err
		}
	}
	for _, bundleID := range failedBundleIDs {
		if err := record(bundleID, "pending_apply"); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for tenantID := range seen {
		out = append(out, tenantID)
	}
	return out, nil
}

func tenantIDFromBundleID(bundleID string) (string, bool) {
	tenantID, ok := strings.CutPrefix(strings.TrimSpace(bundleID), "tenant:")
	if !ok || tenantID == "" {
		return "", false
	}
	return tenantID, true
}

// FinalizeTenantAliasRemoval deletes the alias row and any per-edge
// state. Called by the alias DNS worker after DNS membership has
// been cleared.
func (m *CertManager) FinalizeTenantAliasRemoval(ctx context.Context, tenantID string) error {
	if err := m.store.DeleteTenantEdgeApplyState(ctx, tenantID); err != nil {
		return fmt.Errorf("delete edge state: %w", err)
	}
	return m.store.DeleteTenantAlias(ctx, tenantID)
}

// ProcessPendingTenantAliases is the worker pass: for each tenant in
// status cert_issuing or cert_failed, run EnsureTenantWildcardCertificate
// and transition the row's status accordingly. Tenants in tearing_down
// state get cleaned up.
//
// Returns the number of aliases processed (any state transition counts).
// Caller drives the cadence. Typical interval: 30s reconciler tick.
func (m *CertManager) ProcessPendingTenantAliases(ctx context.Context, rootDomain, email string) (int, error) {
	rootDomain = strings.TrimSpace(rootDomain)
	if rootDomain == "" {
		return 0, fmt.Errorf("rootDomain is required")
	}
	tenantZoneLabel := TenantAliasZoneLabel

	pending, err := m.store.ListPendingTenantAliases(ctx)
	if err != nil {
		return 0, fmt.Errorf("list pending aliases: %w", err)
	}

	processed := 0
	for _, alias := range pending {
		// Validate subdomain still passes reserved-slug checks.
		if pkgdns.IsReservedTenantSlug(alias.Subdomain, nil) {
			if statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, "cert_failed", "subdomain is reserved"); statusErr != nil {
				return processed, fmt.Errorf("set cert_failed status: %w", statusErr)
			}
			processed++
			continue
		}

		_, certErr := m.EnsureTenantWildcardCertificate(ctx, alias.TenantID, alias.Subdomain, tenantZoneLabel, rootDomain, email)
		if certErr != nil {
			if statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, "cert_failed", certErr.Error()); statusErr != nil {
				return processed, fmt.Errorf("set cert_failed status: %w", statusErr)
			}
			processed++
			continue
		}
		if statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, "cert_issued", ""); statusErr != nil {
			return processed, fmt.Errorf("set cert_issued status: %w", statusErr)
		}
		processed++
	}
	return processed, nil
}
