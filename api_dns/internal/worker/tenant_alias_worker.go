package worker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_dns/internal/bunnyrecords"
	"frameworks/api_dns/internal/provider/bunny"
	"frameworks/api_dns/internal/store"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// EdgeAddressResolver maps a Foghorn-reported node_id to one or more
// public IPv4/IPv6 addresses. The worker uses this to populate Bunny
// smart records for the tenant's geo-DNS pool. Caller wires this from
// Quartermaster, where node-to-address lookup lives.
type EdgeAddressResolver interface {
	ResolveEdgeAddresses(ctx context.Context, nodeID string) (ipv4 []string, ipv6 []string, err error)
}

type ServiceAddress struct {
	NodeID string
	IP     string
}

type tenantAliasAddr struct {
	ip     string
	nodeID string
}

type TenantServiceAddressResolver interface {
	ResolveServiceAddressesForClusters(ctx context.Context, serviceType string, clusterIDs []string, staleThresholdSeconds int) ([]ServiceAddress, error)
}

type TenantClusterEligibility interface {
	TenantActiveInCluster(ctx context.Context, tenantID, clusterID string) (bool, error)
}

// ClusterControlCellHealth answers whether the cluster's owning Foghorn
// control cell is currently healthy enough to be in tenant alias DNS.
// Implemented by the Quartermaster-backed eligibility resolver.
type ClusterControlCellHealth interface {
	ClusterControlCellHealthy(ctx context.Context, clusterID string) (bool, error)
}

type tenantAliasDNSProvider interface {
	FindZone(ctx context.Context, domain string) (*bunny.Zone, bool, error)
	ReconcileRecordSet(ctx context.Context, zoneID int64, name string, recordType int, desired []bunny.Record) error
}

// AliasApplyStateWorker reconciles Bunny smart record sets in cdn.{root}
// from Navigator's durable per-edge ACK state.
type AliasApplyStateWorker struct {
	store              tenantAliasStore
	bunny              tenantAliasDNSProvider
	edges              EdgeAddressResolver
	logger             logging.Logger
	interval           time.Duration
	rootDomain         string
	tenantZoneLabel    string
	healthStaleSeconds int
}

// tenantAliasStore is the subset of store.Store this worker uses.
type tenantAliasStore interface {
	ListPendingTenantAliases(ctx context.Context) ([]store.TenantAlias, error)
	ListTenantAliasesByStatus(ctx context.Context, statuses []string) ([]store.TenantAlias, error)
	GetTenantAlias(ctx context.Context, tenantID string) (*store.TenantAlias, error)
	UpsertTenantEdgeApplyState(ctx context.Context, st *store.TenantEdgeApplyState) error
	ListTenantEdgeApplyState(ctx context.Context, tenantID, stateFilter string) ([]store.TenantEdgeApplyState, error)
	DeleteTenantEdgeApplyState(ctx context.Context, tenantID string) error
	DeleteTenantAlias(ctx context.Context, tenantID string) error
	ListTenantAliasRetirements(ctx context.Context) ([]store.TenantAliasRetirement, error)
	DeleteTenantAliasRetirement(ctx context.Context, tenantID, subdomain string) error
	RecordTenantAliasRetirementFailure(ctx context.Context, tenantID, subdomain, errMsg string) error
}

func NewAliasApplyStateWorker(s tenantAliasStore, bunnyClient *bunny.Client, edges EdgeAddressResolver, logger logging.Logger, interval time.Duration, rootDomain, tenantZoneLabel string, healthStaleSeconds int) *AliasApplyStateWorker {
	if healthStaleSeconds <= 0 {
		healthStaleSeconds = 300
	}
	return &AliasApplyStateWorker{
		store:              s,
		bunny:              bunnyClient,
		edges:              edges,
		logger:             logger,
		interval:           interval,
		rootDomain:         rootDomain,
		tenantZoneLabel:    tenantZoneLabel,
		healthStaleSeconds: healthStaleSeconds,
	}
}

// Start runs the worker until ctx is cancelled. Runs one pass
// immediately and then on the configured interval.
func (w *AliasApplyStateWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *AliasApplyStateWorker) runOnce(ctx context.Context) {
	aliases, err := w.listAllAliasesForReconcile(ctx)
	if err != nil {
		w.logger.WithError(err).Warn("Failed to list tenant aliases for apply state worker")
		return
	}

	for _, alias := range aliases {
		w.reconcileTenantAlias(ctx, alias)
	}

	w.processRetirements(ctx)
}

// listAllAliasesForReconcile returns aliases the DNS worker needs to act
// on: cert_issued (DNS publishing path) and tearing_down
// (cleanup path). cert_issuing/cert_failed are owned by the
// ProcessPendingTenantAliases worker and not handled here.
func (w *AliasApplyStateWorker) listAllAliasesForReconcile(ctx context.Context) ([]store.TenantAlias, error) {
	return w.store.ListTenantAliasesByStatus(ctx, []string{"cert_issued", "tearing_down"})
}

func (w *AliasApplyStateWorker) reconcileTenantAlias(ctx context.Context, alias store.TenantAlias) {
	log := w.logger.WithField("tenant_id", alias.TenantID).WithField("status", alias.Status)

	if alias.Status == "tearing_down" {
		w.teardown(ctx, alias)
		return
	}
	if alias.Status != "cert_issued" {
		return
	}
	if err := w.PublishTenantAlias(ctx, alias.TenantID); err != nil {
		log.WithError(err).Warn("Failed to publish tenant smart record set")
	}
}

// PublishTenantAlias reconciles one tenant's Bunny smart record sets from
// currently applied/in_dns edge rows. It is called both by the periodic
// worker and immediately after Foghorn reports a new ACK.
func (w *AliasApplyStateWorker) PublishTenantAlias(ctx context.Context, tenantID string) error {
	alias, err := w.store.GetTenantAlias(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if alias.Status != "cert_issued" {
		return nil
	}
	rows, err := w.store.ListTenantEdgeApplyState(ctx, tenantID, "")
	if err != nil {
		return err
	}
	eligible := make([]store.TenantEdgeApplyState, 0, len(rows))
	stale := make([]store.TenantEdgeApplyState, 0)
	var eligibility TenantClusterEligibility
	if checker, ok := w.edges.(TenantClusterEligibility); ok {
		eligibility = checker
	}
	var cellHealth ClusterControlCellHealth
	if checker, ok := w.edges.(ClusterControlCellHealth); ok {
		cellHealth = checker
	}
	for _, row := range rows {
		if row.State != "applied" && row.State != "in_dns" {
			continue
		}
		if eligibility != nil {
			active, activeErr := eligibility.TenantActiveInCluster(ctx, tenantID, row.ClusterID)
			if activeErr != nil {
				w.logger.WithError(activeErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"cluster_id": row.ClusterID,
				}).Warn("Tenant cluster eligibility check failed; preserving current tenant DNS")
				return activeErr
			}
			if !active {
				stale = append(stale, row)
				continue
			}
		}
		if cellHealth != nil {
			healthy, healthErr := cellHealth.ClusterControlCellHealthy(ctx, row.ClusterID)
			if healthErr != nil {
				w.logger.WithError(healthErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"cluster_id": row.ClusterID,
				}).Warn("Control-cell health check failed; preserving current tenant DNS")
				return healthErr
			}
			if !healthy {
				stale = append(stale, row)
				continue
			}
		}
		eligible = append(eligible, row)
	}
	return w.publishTenantSmartRecords(ctx, *alias, eligible, stale)
}

func (w *AliasApplyStateWorker) teardown(ctx context.Context, alias store.TenantAlias) {
	log := w.logger.WithField("tenant_id", alias.TenantID)
	// Clear the label's records first; local state is deleted only after
	// Bunny accepts the DNS removal.
	if err := w.clearTenantAliasRecords(ctx, alias.Subdomain); err != nil {
		log.WithError(err).Warn("Failed to clear tenant alias records during teardown")
		return
	}
	if err := w.store.DeleteTenantEdgeApplyState(ctx, alias.TenantID); err != nil {
		log.WithError(err).Warn("Failed to delete tenant edge apply state during teardown")
	}
	if err := w.store.DeleteTenantAlias(ctx, alias.TenantID); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.WithError(err).Warn("Failed to delete tenant alias during teardown")
	}
}

// clearTenantAliasRecords removes the apex + per-service Bunny records for
// one alias label. Shared by teardown (the active label) and the retirement
// pass (a retired label after a rename). No-op when no Bunny client or the
// tenant zone is absent.
func (w *AliasApplyStateWorker) clearTenantAliasRecords(ctx context.Context, subdomain string) error {
	if w.bunny == nil {
		return nil
	}
	zoneFQDN := w.tenantZoneLabel + "." + w.rootDomain
	zone, found, err := w.bunny.FindZone(ctx, zoneFQDN)
	if err != nil {
		return fmt.Errorf("find tenant alias zone: %w", err)
	}
	if !found {
		return nil
	}
	names := make([]string, 0, 1+len(pkgdns.TenantAliasableServiceTypes()))
	for _, serviceType := range pkgdns.TenantAliasableServiceTypes() {
		label, ok := pkgdns.PublicSubdomain(serviceType)
		if !ok || label == "" {
			continue
		}
		names = append(names, label+"."+subdomain)
	}
	names = append(names, retiredTenantAliasRecordNames(subdomain)...)
	names = append(names, subdomain) // apex last
	for _, name := range names {
		if reconcileErr := w.bunny.ReconcileRecordSet(ctx, zone.ID, name, bunny.RecordTypeA, nil); reconcileErr != nil {
			return fmt.Errorf("clear record set %s: %w", name, reconcileErr)
		}
	}
	return nil
}

// processRetirements clears Bunny records for retired alias labels — the old
// label after a subdomain rename. tenant_aliases overwrites subdomain in
// place, so each retirement row is Navigator's only memory of an old label.
func (w *AliasApplyStateWorker) processRetirements(ctx context.Context) {
	retirements, err := w.store.ListTenantAliasRetirements(ctx)
	if err != nil {
		w.logger.WithError(err).Warn("Failed to list tenant alias retirements")
		return
	}
	for _, r := range retirements {
		w.processRetirement(ctx, r)
	}
}

func (w *AliasApplyStateWorker) processRetirement(ctx context.Context, r store.TenantAliasRetirement) {
	log := w.logger.WithFields(logging.Fields{"tenant_id": r.TenantID, "subdomain": r.Subdomain})

	active, err := w.store.GetTenantAlias(ctx, r.TenantID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.WithError(err).Warn("Failed to look up active alias for retirement")
		return
	}

	// When the retired label equals the tenant's current active alias, the
	// label is live again — never clear its records.
	if active != nil && active.Subdomain == r.Subdomain {
		if active.UpdatedAt.After(r.RequestedAt) {
			// Re-pointed back AFTER the retirement was requested (a -> b -> a):
			// the retirement is stale. Drop it without touching live records.
			if delErr := w.store.DeleteTenantAliasRetirement(ctx, r.TenantID, r.Subdomain); delErr != nil {
				log.WithError(delErr).Warn("Failed to delete stale tenant alias retirement")
			}
			return
		}
		// active == retired label but not superseded: a retire was requested
		// for an already-active label, which the QM transition helper never
		// does (it only retires old != new). Surface it and keep pending.
		log.WithFields(logging.Fields{
			"requested_at": r.RequestedAt,
			"updated_at":   active.UpdatedAt,
		}).Error("Tenant alias retirement targets the active label but was not superseded; leaving pending (upstream logic bug)")
		return
	}

	if clearErr := w.clearTenantAliasRecords(ctx, r.Subdomain); clearErr != nil {
		log.WithError(clearErr).Warn("Failed to clear retired tenant alias records")
		if recErr := w.store.RecordTenantAliasRetirementFailure(ctx, r.TenantID, r.Subdomain, clearErr.Error()); recErr != nil {
			log.WithError(recErr).Warn("Failed to record tenant alias retirement failure")
		}
		return
	}
	if delErr := w.store.DeleteTenantAliasRetirement(ctx, r.TenantID, r.Subdomain); delErr != nil {
		log.WithError(delErr).Warn("Failed to delete completed tenant alias retirement")
	}
}

// publishTenantSmartRecords publishes one Bunny record set per customer-facing
// tenant alias. Each paying tenant gets:
//
//   - {subdomain}.{tenantZone}                      (apex)
//   - {service-label}.{subdomain}.{tenantZone}      one per service
//
// where {service-label} iterates pkgdns.TenantAliasableServiceTypes()
// (foghorn, edge, edge-ingest, edge-egress, edge-storage, edge-processing).
func (w *AliasApplyStateWorker) publishTenantSmartRecords(ctx context.Context, alias store.TenantAlias, applied, stale []store.TenantEdgeApplyState) error {
	if w.bunny == nil || w.edges == nil {
		return nil
	}
	if w.tenantZoneLabel == "" || w.rootDomain == "" {
		return fmt.Errorf("tenant zone label and root domain are required for DNS publishing")
	}
	zoneFQDN := w.tenantZoneLabel + "." + w.rootDomain
	zone, found, err := w.bunny.FindZone(ctx, zoneFQDN)
	if err != nil {
		return fmt.Errorf("find zone %s: %w", zoneFQDN, err)
	}
	if !found {
		return fmt.Errorf("tenant zone %s not delegated to Bunny yet; see DNSManager.EnsureBunnyZone", zoneFQDN)
	}

	appliedNodeIDs := make(map[string]struct{}, len(applied))
	clusterIDs := make([]string, 0, len(applied))
	seenClusters := map[string]struct{}{}
	for _, edge := range applied {
		appliedNodeIDs[edge.NodeID] = struct{}{}
		if edge.ClusterID == "" {
			continue
		}
		if _, seen := seenClusters[edge.ClusterID]; seen {
			continue
		}
		seenClusters[edge.ClusterID] = struct{}{}
		clusterIDs = append(clusterIDs, edge.ClusterID)
	}

	resolveByService := func(serviceType string) ([]tenantAliasAddr, error) {
		if resolver, ok := w.edges.(TenantServiceAddressResolver); ok {
			serviceAddrs, err := resolver.ResolveServiceAddressesForClusters(ctx, serviceType, clusterIDs, w.healthStaleSeconds)
			if err != nil {
				return nil, err
			}
			out := make([]tenantAliasAddr, 0, len(serviceAddrs))
			seen := map[string]struct{}{}
			for _, a := range serviceAddrs {
				if a.IP == "" {
					continue
				}
				if isTenantEdgeServiceType(serviceType) {
					if _, applied := appliedNodeIDs[a.NodeID]; !applied {
						continue
					}
				}
				key := a.NodeID + "\x00" + a.IP
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, tenantAliasAddr{ip: a.IP, nodeID: a.NodeID})
			}
			return out, nil
		}
		if serviceType != "edge" {
			return nil, nil
		}
		return w.resolveAppliedEdgeAddresses(ctx, applied)
	}

	recordsByName := map[string][]tenantAliasAddr{}
	edgeAddrs, edgeErr := resolveByService("edge")
	if edgeErr != nil {
		return fmt.Errorf("resolve tenant edge addresses: %w", edgeErr)
	}
	recordsByName[alias.Subdomain] = edgeAddrs
	if edgeLabel, ok := pkgdns.PublicSubdomain("edge"); ok && edgeLabel != "" {
		recordsByName[edgeLabel+"."+alias.Subdomain] = edgeAddrs
	}
	for _, serviceType := range pkgdns.TenantAliasableServiceTypes() {
		if serviceType == "edge" {
			continue
		}
		label, ok := pkgdns.PublicSubdomain(serviceType)
		if !ok || label == "" {
			continue
		}
		addrs, resolveErr := resolveByService(serviceType)
		if resolveErr != nil {
			return fmt.Errorf("resolve tenant %s addresses: %w", serviceType, resolveErr)
		}
		recordsByName[label+"."+alias.Subdomain] = addrs
	}

	publishedNodes := make(map[string]struct{}, len(applied))
	for _, a := range edgeAddrs {
		publishedNodes[a.nodeID] = struct{}{}
	}

	for name, addrs := range recordsByName {
		desired := make([]bunny.Record, 0, len(addrs))
		for _, a := range addrs {
			desired = append(desired, bunnyrecords.ARecord(bunnyrecords.ARecordInput{
				Name:  name,
				Value: a.ip,
				TTL:   60,
				FQDN:  bunnyRecordFQDN(name, zoneFQDN),
			}))
		}
		if err := w.bunny.ReconcileRecordSet(ctx, zone.ID, name, bunny.RecordTypeA, desired); err != nil {
			w.logger.WithError(err).WithField("record_name", name).Warn("Failed to reconcile tenant smart record set")
			return err
		}
	}

	for _, name := range retiredTenantAliasRecordNames(alias.Subdomain) {
		if err := w.bunny.ReconcileRecordSet(ctx, zone.ID, name, bunny.RecordTypeA, nil); err != nil {
			w.logger.WithError(err).WithField("record_name", name).Warn("Failed to clear non-customer tenant alias record set")
			return err
		}
	}

	// Keep the durable state aligned with edge aggregate DNS. Edges without
	// addresses, or no longer eligible, are downgraded out of in_dns so API
	// readiness reflects DNS reality.
	for _, edge := range applied {
		if _, ok := publishedNodes[edge.NodeID]; !ok {
			if edge.State == "in_dns" {
				w.markEdgeNotInDNS(ctx, edge)
			}
			continue
		}
		edge.State = "in_dns"
		edge.InDNSAt = nullNow()
		if upsertErr := w.store.UpsertTenantEdgeApplyState(ctx, &edge); upsertErr != nil {
			w.logger.WithError(upsertErr).WithField("node_id", edge.NodeID).Debug("Failed to mark edge in_dns")
		}
	}
	for _, edge := range stale {
		if edge.State == "in_dns" {
			w.markEdgeNotInDNS(ctx, edge)
		}
	}
	return nil
}

func (w *AliasApplyStateWorker) resolveAppliedEdgeAddresses(ctx context.Context, applied []store.TenantEdgeApplyState) ([]tenantAliasAddr, error) {
	var addrs []tenantAliasAddr
	for _, edge := range applied {
		ipv4s, _, addrErr := w.edges.ResolveEdgeAddresses(ctx, edge.NodeID)
		if addrErr != nil {
			w.logger.WithError(addrErr).WithField("node_id", edge.NodeID).Debug("ResolveEdgeAddresses failed")
			continue
		}
		for _, ip := range ipv4s {
			addrs = append(addrs, tenantAliasAddr{ip: ip, nodeID: edge.NodeID})
		}
	}
	return addrs, nil
}

func isTenantEdgeServiceType(serviceType string) bool {
	return serviceType == "edge" || strings.HasPrefix(serviceType, "edge-")
}

func retiredTenantAliasRecordNames(subdomain string) []string {
	out := []string{}
	for _, serviceType := range []string{"chandler", "livepeer-gateway"} {
		label, ok := pkgdns.PublicSubdomain(serviceType)
		if !ok || label == "" {
			continue
		}
		out = append(out, label+"."+subdomain)
	}
	return out
}

func bunnyRecordFQDN(recordName, zoneDomain string) string {
	if recordName == "" {
		return zoneDomain
	}
	return recordName + "." + zoneDomain
}

func (w *AliasApplyStateWorker) markEdgeNotInDNS(ctx context.Context, edge store.TenantEdgeApplyState) {
	edge.State = "applied"
	edge.InDNSAt = sql.NullTime{}
	if upsertErr := w.store.UpsertTenantEdgeApplyState(ctx, &edge); upsertErr != nil {
		w.logger.WithError(upsertErr).WithField("node_id", edge.NodeID).Debug("Failed to mark edge out of DNS")
	}
}

func nullNow() sql.NullTime {
	return sql.NullTime{Valid: true, Time: time.Now().UTC()}
}
