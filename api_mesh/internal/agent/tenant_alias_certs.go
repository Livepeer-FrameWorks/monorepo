package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	pkgingress "github.com/Livepeer-FrameWorks/monorepo/pkg/ingress"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
)

const foghornServiceType = "foghorn"

// ErrTenantAliasIngressPending means the inventory is durable but the hosted
// ingress has not yet acknowledged that exact generation map.
var ErrTenantAliasIngressPending = errors.New("tenant alias ingress inventory is pending application")

// syncTenantAliasCertificates publishes a complete SNI inventory only after
// validated immutable certificate generations exist. Nginx workers pin the
// generation through their loaded map, so rotation cannot mix a cert and key.
func (a *Agent) syncTenantAliasCertificates() error {
	if a.navigatorClient == nil || a.aliasClient == nil || a.registryClient == nil || strings.TrimSpace(a.nodeID) == "" {
		return nil
	}
	now := time.Now()
	last := a.lastAliasSync.Load()
	refreshDue := last == 0 || now.Sub(time.Unix(last, 0)) >= a.certSyncInterval
	ctx, cancel := context.WithTimeout(context.Background(), a.syncTimeout)
	defer cancel()
	clusters, scopeErr := a.localAliasClusters(ctx)
	if scopeErr != nil {
		return scopeErr
	}
	a.aliasIngressRequired.Store(len(clusters) > 0)
	if len(clusters) == 0 {
		if _, err := os.Stat(a.tenantAliasDir("")); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	desired := map[string]string{}
	for _, cluster := range clusters {
		response, inventoryErr := a.aliasClient.ListAliasedTenantsForCluster(ctx, cluster)
		if inventoryErr != nil {
			return fmt.Errorf("list aliased tenants for %s: %w", cluster, inventoryErr)
		}
		if response == nil {
			return fmt.Errorf("alias inventory for %s returned no authoritative response", cluster)
		}
		for _, ref := range response.GetTenants() {
			tenant, sub := strings.TrimSpace(ref.GetTenantId()), strings.TrimSpace(ref.GetSubdomain())
			if tenant == "" || !pkgingress.IsValidBundleID(sub) {
				continue
			}
			if prior, ok := desired[tenant]; ok && prior != sub {
				return fmt.Errorf("tenant %s has conflicting alias labels", tenant)
			}
			desired[tenant] = sub
		}
	}
	previous, loadErr := a.loadAliasInventory()
	if loadErr != nil {
		return loadErr
	}
	inventory := map[string]aliasCertificate{}
	tenants := make([]string, 0, len(desired))
	for tenant := range desired {
		tenants = append(tenants, tenant)
	}
	sort.Strings(tenants)
	var failures []error
	for _, tenant := range tenants {
		if material, ok := previous[tenant]; ok && !refreshDue && material.Subdomain == desired[tenant] && a.aliasCertificateUsable(material, now) {
			inventory[tenant] = material
			continue
		}
		response, fetchErr := a.navigatorClient.GetTLSBundle(ctx, &dnspb.GetTLSBundleRequest{BundleId: "tenant:" + tenant})
		if fetchErr == nil && responseError(response) != "" {
			fetchErr = errors.New(responseError(response))
		}
		if fetchErr == nil && response.GetFound() {
			var material aliasCertificate
			material, fetchErr = a.installAliasCertificate(tenant, response, now)
			if fetchErr == nil {
				inventory[tenant] = material
				continue
			}
		}
		if fetchErr != nil {
			failures = append(failures, fmt.Errorf("tenant %s TLS bundle: %w", tenant, fetchErr))
			if material, ok := previous[tenant]; ok && a.aliasCertificateUsable(material, now) {
				inventory[tenant] = material
			}
		}
	}
	if err := a.publishAliasInventory(inventory); err != nil {
		// A normal asynchronous apply wait must not reclassify a simultaneous
		// certificate or authority failure as a successful pending operation.
		if len(failures) > 0 && errors.Is(err, ErrTenantAliasIngressPending) {
			return errors.Join(failures...)
		}
		return errors.Join(append(failures, err)...)
	}
	active := map[string]string{}
	for tenant, material := range inventory {
		active[material.Subdomain] = tenant
	}
	if _, err := a.pruneTenantAliasDirs(active); err != nil {
		return errors.Join(append(failures, err)...)
	}
	if err := a.pruneExpiredAliasGenerations(inventory, now); err != nil {
		return errors.Join(append(failures, err)...)
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if refreshDue {
		a.lastAliasSync.Store(now.Unix())
	}
	return nil
}

func responseError(resp *dnspb.GetTLSBundleResponse) string {
	if resp == nil {
		return "empty response"
	}
	return strings.TrimSpace(resp.GetError())
}
func (a *Agent) tenantAliasDir(sub string) string {
	return filepath.Join(a.ingressTLSRoot, pkgingress.TenantAliasDirName, sub)
}
func (a *Agent) aliasMapPath() string { return a.tenantAliasDir(pkgingress.TenantAliasMapName) }
func (a *Agent) tenantAliasVersionUnchanged(sub, marker string) bool {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	previous, ok := a.aliasVersions[sub]
	return ok && previous == marker
}
func (a *Agent) recordTenantAliasVersion(sub, marker string) {
	a.ingressMu.Lock()
	defer a.ingressMu.Unlock()
	if a.aliasVersions == nil {
		a.aliasVersions = map[string]string{}
	}
	if marker == "" {
		delete(a.aliasVersions, sub)
	} else {
		a.aliasVersions[sub] = marker
	}
}

func (a *Agent) loadAliasInventory() (map[string]aliasCertificate, error) {
	body, err := os.ReadFile(a.tenantAliasDir("manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]aliasCertificate{}, nil
	}
	if err != nil {
		return nil, err
	}
	var result map[string]aliasCertificate
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("read alias certificate inventory: %w", err)
	}
	return result, nil
}

func (a *Agent) publishAliasInventory(inventory map[string]aliasCertificate) error {
	hosts := make([]string, 0, len(inventory))
	byHost := map[string]aliasCertificate{}
	for _, material := range inventory {
		if previous, exists := byHost[material.Host]; exists && previous.Path != material.Path {
			return fmt.Errorf("alias hostname %s has conflicting certificates", material.Host)
		}
		if _, exists := byHost[material.Host]; !exists {
			hosts = append(hosts, material.Host)
		}
		byHost[material.Host] = material
	}
	sort.Strings(hosts)
	var mapping strings.Builder
	mapping.WriteString("# Managed by Privateer; certificate generations are immutable.\n")
	for _, host := range hosts {
		fmt.Fprintf(&mapping, "%s %s;\n", host, byHost[host].Path)
	}
	body, marshalErr := json.Marshal(inventory)
	if marshalErr != nil {
		return marshalErr
	}
	if err := a.ensureAliasDirectory(a.tenantAliasDir("")); err != nil {
		return err
	}
	lock, openErr := os.Open(a.tenantAliasDir(""))
	if openErr != nil {
		return openErr
	}
	defer func() { _ = lock.Close() }()
	// The root reloader holds the same directory shared while parsing and
	// applying the map. Never block the mesh loop behind a stalled reload.
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrTenantAliasIngressPending
		}
		return err
	}
	if err := a.writeAliasFile(a.tenantAliasDir("manifest.json"), body, 0o640); err != nil {
		return err
	}
	if err := a.writeAliasFile(a.aliasMapPath(), []byte(mapping.String()), 0o640); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(mapping.String()))
	marker := hex.EncodeToString(sum[:])
	if a.aliasAppliedPath != "" && a.aliasIngressRequired.Load() {
		applied, err := os.ReadFile(a.aliasAppliedPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if strings.TrimSpace(string(applied)) != marker {
			a.aliasIngressApplied.Store(false)
			if err := a.touchIngressReloadTrigger(); err != nil {
				return fmt.Errorf("request hosted ingress application: %w", err)
			}
			return ErrTenantAliasIngressPending
		}
		a.recordTenantAliasVersion("<inventory>", marker)
	}
	if !a.tenantAliasVersionUnchanged("<inventory>", marker) {
		if err := a.touchIngressReloadTrigger(); err != nil {
			return fmt.Errorf("activate alias certificate inventory: %w", err)
		}
		a.recordTenantAliasVersion("<inventory>", marker)
	}
	a.aliasIngressApplied.Store(true)
	return nil
}

// Legacy per-label directories are no longer read by the generation-pinned
// ingress. Retain an active label during a rolling configuration upgrade.
func (a *Agent) pruneTenantAliasDirs(desired map[string]string) (bool, error) {
	entries, err := os.ReadDir(a.tenantAliasDir(""))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	pruned := false
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == "revisions" {
			continue
		}
		if _, active := desired[name]; active {
			continue
		}
		if err := os.RemoveAll(a.tenantAliasDir(name)); err != nil {
			return pruned, err
		}
		pruned = true
	}
	return pruned, nil
}

func (a *Agent) writeAliasFile(path string, body []byte, mode os.FileMode) error {
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !bytes.Equal(current, body) {
		if err := writeAtomicFile(path, body, mode); err != nil {
			return err
		}
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return a.setAliasReaderGroup(path)
}
