package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	pkgingress "github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

// foghornServiceType is the registry service id whose presence on this node
// makes it a tenant-alias playback host (nginx fronts foghorn's /play and
// needs the per-tenant wildcard certs on disk for its SNI-variable paths).
const foghornServiceType = "foghorn"

// syncTenantAliasCertificates materializes the cluster's tenant-alias
// wildcard certs (Navigator bundle "tenant:<id>") beneath
// <TLSRoot>/tenant-alias/<subdomain>/ on Foghorn hosts, and prunes
// subdirectories for tenants that are no longer alias-eligible (tier
// downgrades). Privateer exclusively owns that subtree. Keyed by subdomain —
// not bundle ID — because nginx can only derive the subdomain from
// $ssl_server_name at handshake time (and "tenant:<uuid>" is not a safe path
// component). Mirrors syncIngressCertificates' cadence, change detection,
// and reload-trigger contract.
func (a *Agent) syncTenantAliasCertificates() error {
	if a.navigatorClient == nil || a.aliasClient == nil || a.registryClient == nil {
		return nil
	}
	if strings.TrimSpace(a.clusterID) == "" || strings.TrimSpace(a.nodeID) == "" {
		return nil
	}

	now := time.Now().Unix()
	last := a.lastAliasSync.Load()
	if last > 0 && time.Duration(now-last)*time.Second < a.certSyncInterval {
		a.ingressMu.Lock()
		cached := append([]string(nil), a.cachedAliasSubs...)
		a.ingressMu.Unlock()
		if !a.missingTenantAliasMaterials(cached) {
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	defer cancel()

	services, err := a.listNodeServiceTypes(ctx)
	if err != nil {
		return fmt.Errorf("list node services for tenant-alias sync: %w", err)
	}
	desired := map[string]string{}
	if slices.Contains(services, foghornServiceType) {
		resp, listErr := a.aliasClient.ListAliasedTenantsForCluster(ctx, a.clusterID)
		if listErr != nil {
			return fmt.Errorf("list aliased tenants: %w", listErr)
		}
		for _, tenant := range resp.GetTenants() {
			sub := strings.TrimSpace(tenant.GetSubdomain())
			tenantID := strings.TrimSpace(tenant.GetTenantId())
			if sub == "" || tenantID == "" {
				continue
			}
			// Subdomains share the bundle-ID charset; reject anything unsafe
			// as a path component before touching disk.
			if !pkgingress.IsValidBundleID(sub) {
				a.logger.WithField("subdomain", sub).Warn("Tenant-alias sync: ignoring unsafe subdomain")
				continue
			}
			desired[sub] = tenantID
		}
	}
	// A non-foghorn node (or one foghorn was removed from) converges to an
	// empty desired set: nothing fetched, leftovers pruned.

	subs := make([]string, 0, len(desired))
	for sub := range desired {
		subs = append(subs, sub)
	}
	sort.Strings(subs)

	changed := false
	for _, sub := range subs {
		bundleID := "tenant:" + desired[sub]
		resp, getErr := a.navigatorClient.GetTLSBundle(ctx, &dnspb.GetTLSBundleRequest{BundleId: bundleID})
		if getErr != nil {
			a.logger.WithError(getErr).WithField("bundle_id", bundleID).Warn("Tenant-alias sync: GetTLSBundle failed")
			continue
		}
		// Not-found means issuance is still pending in Navigator; the next
		// cadence tick reconciles it.
		if !resp.GetFound() || strings.TrimSpace(resp.GetCertPem()) == "" || strings.TrimSpace(resp.GetKeyPem()) == "" {
			continue
		}
		marker := ingressBundleMarker(resp)
		if marker != "" && a.tenantAliasVersionUnchanged(sub, marker) {
			continue
		}
		if writeErr := a.writeTenantAliasBundle(sub, resp.GetCertPem(), resp.GetKeyPem()); writeErr != nil {
			return fmt.Errorf("write tenant-alias bundle %s: %w", sub, writeErr)
		}
		a.recordTenantAliasVersion(sub, marker)
		changed = true
	}

	pruned, pruneErr := a.pruneTenantAliasDirs(desired)
	if pruneErr != nil {
		return pruneErr
	}
	changed = changed || pruned

	if changed {
		if err := a.touchIngressReloadTrigger(); err != nil {
			return fmt.Errorf("touch ingress reload trigger: %w", err)
		}
	}

	a.ingressMu.Lock()
	a.cachedAliasSubs = subs
	a.ingressMu.Unlock()
	a.lastAliasSync.Store(now)
	return nil
}

func (a *Agent) tenantAliasDir(sub string) string {
	return filepath.Join(a.ingressTLSRoot, pkgingress.TenantAliasDirName, sub)
}

// missingTenantAliasMaterials reports whether any expected alias bundle is
// missing its on-disk cert or key, bypassing the cert sync cadence (same
// contract as missingIngressBundleMaterials).
func (a *Agent) missingTenantAliasMaterials(subs []string) bool {
	for _, sub := range subs {
		dir := a.tenantAliasDir(sub)
		if !fileExistsAndNonEmpty(filepath.Join(dir, "tls.crt")) {
			return true
		}
		if !fileExistsAndNonEmpty(filepath.Join(dir, "tls.key")) {
			return true
		}
	}
	return false
}

func (a *Agent) tenantAliasVersionUnchanged(sub, marker string) bool {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	prev, ok := a.aliasVersions[sub]
	return ok && prev == marker
}

func (a *Agent) recordTenantAliasVersion(sub, marker string) {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	if marker == "" {
		delete(a.aliasVersions, sub)
		return
	}
	a.aliasVersions[sub] = marker
}

func (a *Agent) writeTenantAliasBundle(sub, certPEM, keyPEM string) error {
	dir := a.tenantAliasDir(sub)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := writeAtomicFile(filepath.Join(dir, "tls.crt"), []byte(certPEM), 0o644); err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(dir, "tls.key"), []byte(keyPEM), 0o640)
}

// pruneTenantAliasDirs removes alias subdirectories that are no longer in the
// desired set — downgraded or removed tenants must stop being servable.
// Privateer exclusively owns <TLSRoot>/tenant-alias/, so anything unexpected
// there is ours to delete. Callers must only invoke this after a SUCCESSFUL
// ListAliasedTenantsForCluster — pruning on an API failure would tear down
// every tenant's cert.
func (a *Agent) pruneTenantAliasDirs(desired map[string]string) (bool, error) {
	root := filepath.Join(a.ingressTLSRoot, pkgingress.TenantAliasDirName)
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read tenant-alias dir: %w", err)
	}
	pruned := false
	for _, entry := range entries {
		name := entry.Name()
		if _, keep := desired[name]; keep {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return pruned, fmt.Errorf("prune tenant-alias %s: %w", name, err)
		}
		a.recordTenantAliasVersion(name, "")
		pruned = true
	}
	return pruned, nil
}
