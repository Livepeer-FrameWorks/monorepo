package logic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var configSeedApplyAckOutcomes = newConfigSeedApplyAckOutcomes()

func newConfigSeedApplyAckOutcomes() *prometheus.CounterVec {
	counter := promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "navigator_config_seed_apply_ack_outcomes_total",
		Help: "Config-seed bundle acknowledgement writes by reported state and database outcome",
	}, []string{"state", "outcome"})
	for _, state := range []string{"applied", "pending_apply"} {
		for _, outcome := range []string{"accepted", "stale", "revoked", "missing_parent", "filtered", "error"} {
			counter.WithLabelValues(state, outcome).Add(0)
		}
	}
	return counter
}

// ObserveConfigSeedApplyAckFiltered records tenant outcomes omitted by a
// successful authority lookup because the tenant is not assigned to the edge.
func ObserveConfigSeedApplyAckFiltered(applied, failed int) {
	configSeedApplyAckOutcomes.WithLabelValues("applied", "filtered").Add(float64(applied))
	configSeedApplyAckOutcomes.WithLabelValues("pending_apply", "filtered").Add(float64(failed))
}

// ObserveConfigSeedApplyAckAuthorityErrors records tenant-bundle writes that
// could not be authorized before reaching the store. They share the same
// bounded state/outcome labels as database failures.
func ObserveConfigSeedApplyAckAuthorityErrors(applied, failed int) {
	configSeedApplyAckOutcomes.WithLabelValues("applied", "error").Add(float64(applied))
	configSeedApplyAckOutcomes.WithLabelValues("pending_apply", "error").Add(float64(failed))
}

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
	if _, err := m.store.SetTenantAliasStatus(ctx, tenantID, "", 0, "tearing_down", ""); err != nil {
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
func (m *CertManager) RemoveTenantAliasCluster(ctx context.Context, tenantID, clusterID string, sequence int64) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if tenantID == "" || clusterID == "" {
		return false, fmt.Errorf("tenantID and clusterID are required")
	}
	if sequence < 0 {
		return false, fmt.Errorf("authority sequence must be non-negative")
	}
	return m.store.RevokeTenantAliasClusterAuthority(ctx, tenantID, clusterID, sequence)
}

func (m *CertManager) EnsureTenantAliasCluster(ctx context.Context, tenantID, clusterID string, sequence int64) (bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if tenantID == "" || clusterID == "" {
		return false, fmt.Errorf("tenantID and clusterID are required")
	}
	if sequence < 0 {
		return false, fmt.Errorf("authority sequence must be non-negative")
	}
	return m.store.GrantTenantAliasClusterAuthority(ctx, tenantID, clusterID, sequence)
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

// TenantAliasClusterAuthorityState exposes Navigator's ordered local projection
// for ACK admission. Empty means a rolling-upgrade first admission may consult
// Quartermaster; revoked is a durable negative decision and never does.
func (m *CertManager) TenantAliasClusterAuthorityState(ctx context.Context, tenantID, clusterID string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	clusterID = strings.TrimSpace(clusterID)
	if tenantID == "" || clusterID == "" {
		return "", nil
	}
	return m.store.TenantAliasClusterAuthorityState(ctx, tenantID, clusterID)
}

func (m *CertManager) ListTenantAliasAuthorizedClusters(ctx context.Context, tenantID string) ([]string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil, fmt.Errorf("tenantID is required")
	}
	return m.store.ListTenantAliasAuthorizedClusters(ctx, tenantID)
}

type ConfigSeedApplyDiscarded struct {
	Stale         int
	Revoked       int
	MissingParent int
}

func (d ConfigSeedApplyDiscarded) Total() int { return d.Stale + d.Revoked + d.MissingParent }

// RecordConfigSeedApplyResult persists Helmsman ConfigSeed ACKs that
// Foghorn observed. Only tenant bundles drive tenant alias DNS state;
// cluster and platform bundles are ignored here.
func (m *CertManager) RecordConfigSeedApplyResult(ctx context.Context, nodeID, clusterID string, seedVersion, deliverySequence uint64, appliedBundleIDs, failedBundleIDs []string, bundleVersions map[string]string, appliedAt time.Time) ([]string, ConfigSeedApplyDiscarded, error) {
	nodeID = strings.TrimSpace(nodeID)
	clusterID = strings.TrimSpace(clusterID)
	if nodeID == "" {
		return nil, ConfigSeedApplyDiscarded{}, fmt.Errorf("nodeID is required")
	}
	if seedVersion == 0 || seedVersion > math.MaxInt64 {
		return nil, ConfigSeedApplyDiscarded{}, fmt.Errorf("seedVersion must fit a positive signed 64-bit integer")
	}
	if deliverySequence > math.MaxInt64 {
		return nil, ConfigSeedApplyDiscarded{}, fmt.Errorf("deliverySequence must fit a signed 64-bit integer")
	}
	if appliedAt.IsZero() {
		appliedAt = time.Now().UTC()
	}
	seen := map[string]struct{}{}
	discarded := ConfigSeedApplyDiscarded{}
	record := func(bundleID, state string) error {
		tenantID, ok := tenantIDFromBundleID(bundleID)
		if !ok {
			return nil
		}
		ackOutcome, err := m.store.UpsertTenantEdgeApplyAck(ctx, &store.TenantEdgeApplyState{
			TenantID:             tenantID,
			ClusterID:            clusterID,
			NodeID:               nodeID,
			BundleID:             bundleID,
			BundleVersion:        strings.TrimSpace(bundleVersions[bundleID]),
			State:                state,
			LastSeedVersion:      sql.NullInt64{Valid: true, Int64: int64(seedVersion)},
			LastDeliverySequence: int64(deliverySequence),
			LastAckAt:            sql.NullTime{Valid: true, Time: appliedAt.UTC()},
		})
		outcome := string(ackOutcome)
		if err != nil {
			outcome = "error"
		} else if ackOutcome == store.TenantEdgeApplyAckStale {
			discarded.Stale++
		} else if ackOutcome == store.TenantEdgeApplyAckRevoked {
			discarded.Revoked++
		} else if ackOutcome == store.TenantEdgeApplyAckMissingParent {
			discarded.MissingParent++
		} else {
			seen[tenantID] = struct{}{}
		}
		configSeedApplyAckOutcomes.WithLabelValues(state, outcome).Inc()
		return err
	}
	for _, bundleID := range appliedBundleIDs {
		if err := record(bundleID, "applied"); err != nil {
			return nil, discarded, err
		}
	}
	for _, bundleID := range failedBundleIDs {
		if err := record(bundleID, "pending_apply"); err != nil {
			return nil, discarded, err
		}
	}
	out := make([]string, 0, len(seen))
	for tenantID := range seen {
		out = append(out, tenantID)
	}
	return out, discarded, nil
}

func tenantIDFromBundleID(bundleID string) (string, bool) {
	tenantID, ok := strings.CutPrefix(strings.TrimSpace(bundleID), "tenant:")
	if !ok || tenantID == "" {
		return "", false
	}
	return tenantID, true
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
			transitioned, statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, alias.Subdomain, alias.AuthorityVersion, "cert_failed", "subdomain is reserved")
			if statusErr != nil {
				return processed, fmt.Errorf("set cert_failed status: %w", statusErr)
			}
			if transitioned {
				processed++
			}
			continue
		}

		_, certErr := m.EnsureTenantWildcardCertificate(ctx, alias.TenantID, alias.Subdomain, tenantZoneLabel, rootDomain, email)
		if certErr != nil {
			if errors.Is(certErr, store.ErrIssuanceInProgress) {
				continue
			}
			transitioned, statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, alias.Subdomain, alias.AuthorityVersion, "cert_failed", certErr.Error())
			if statusErr != nil {
				return processed, fmt.Errorf("set cert_failed status: %w", statusErr)
			}
			if transitioned {
				processed++
			}
			continue
		}
		transitioned, statusErr := m.store.SetTenantAliasStatus(ctx, alias.TenantID, alias.Subdomain, alias.AuthorityVersion, "cert_issued", "")
		if statusErr != nil {
			return processed, fmt.Errorf("set cert_issued status: %w", statusErr)
		}
		if transitioned {
			processed++
		}
	}
	return processed, nil
}
