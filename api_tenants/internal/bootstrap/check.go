package bootstrap

import (
	"errors"
	"fmt"
)

// Check is the read-only validation pass `quartermaster bootstrap --check`
// runs after parse. It exercises every reference that ought to be resolvable
// from the file alone, without touching the database:
//
//   - tenant aliases match the bootstrap charset and are unique;
//   - cluster owner_tenant refs resolve to a tenant defined in this file
//     (system tenant or one of the listed customer tenants);
//   - node.cluster_id, ingress.{tls_bundle_id,cluster_id,node_id} and
//     service_registry.{cluster_id,node_id} all reference clusters/nodes
//     defined in this file;
//   - at most one cluster carries is_default.
//
// Cross-service references (e.g. a Purser cluster_pricing.cluster_id) are
// validated on their respective service's check pass, not here.
func Check(qm QuartermasterSection) error {
	tenantAliases := make(map[string]struct{})
	if qm.SystemTenant != nil {
		alias := qm.SystemTenant.Alias
		if alias == "" {
			alias = SystemTenantAlias
		}
		if alias != SystemTenantAlias {
			return fmt.Errorf("system_tenant.alias must be %q (got %q)", SystemTenantAlias, alias)
		}
		tenantAliases[alias] = struct{}{}
	}
	for _, t := range qm.Tenants {
		if !ValidAlias(t.Alias) {
			return fmt.Errorf("tenant alias %q invalid", t.Alias)
		}
		if t.Alias == SystemTenantAlias {
			return fmt.Errorf("customer tenant alias %q is reserved for the system tenant", SystemTenantAlias)
		}
		if _, dup := tenantAliases[t.Alias]; dup {
			return fmt.Errorf("tenant alias %q duplicated", t.Alias)
		}
		tenantAliases[t.Alias] = struct{}{}
	}

	clusterIDs := make(map[string]struct{})
	defaults := 0
	for _, c := range qm.Clusters {
		if err := validateCluster(c); err != nil {
			return err
		}
		if _, dup := clusterIDs[c.ID]; dup {
			return fmt.Errorf("cluster id %q duplicated", c.ID)
		}
		clusterIDs[c.ID] = struct{}{}
		alias, err := AliasFromRef(c.OwnerTenant.Ref)
		if err != nil {
			return fmt.Errorf("cluster %q: %w", c.ID, err)
		}
		if _, ok := tenantAliases[alias]; !ok {
			return fmt.Errorf("cluster %q: owner_tenant alias %q not defined in this file", c.ID, alias)
		}
		if c.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return fmt.Errorf("%d clusters marked is_default; at most one allowed", defaults)
	}

	nodeIDs := make(map[string]struct{})
	for _, n := range qm.Nodes {
		if err := validateNode(n); err != nil {
			return err
		}
		if _, ok := clusterIDs[n.ClusterID]; !ok {
			return fmt.Errorf("node %q: cluster_id %q not defined in this file", n.ID, n.ClusterID)
		}
		if _, dup := nodeIDs[n.ID]; dup {
			return fmt.Errorf("node id %q duplicated", n.ID)
		}
		nodeIDs[n.ID] = struct{}{}
	}

	bundleIDs := make(map[string]struct{})
	for _, b := range qm.Ingress.TLSBundles {
		if err := validateTLSBundle(b); err != nil {
			return err
		}
		if _, ok := clusterIDs[b.ClusterID]; !ok {
			return fmt.Errorf("tls_bundle %q: cluster_id %q not defined in this file", b.ID, b.ClusterID)
		}
		if _, dup := bundleIDs[b.ID]; dup {
			return fmt.Errorf("tls_bundle id %q duplicated", b.ID)
		}
		bundleIDs[b.ID] = struct{}{}
	}
	for _, s := range qm.Ingress.Sites {
		if err := validateIngressSite(s); err != nil {
			return err
		}
		if _, ok := clusterIDs[s.ClusterID]; !ok {
			return fmt.Errorf("ingress_site %q: cluster_id %q not defined in this file", s.ID, s.ClusterID)
		}
		if _, ok := nodeIDs[s.NodeID]; !ok {
			return fmt.Errorf("ingress_site %q: node_id %q not defined in this file", s.ID, s.NodeID)
		}
		if _, ok := bundleIDs[s.TLSBundleID]; !ok {
			return fmt.Errorf("ingress_site %q: tls_bundle_id %q not defined in this file", s.ID, s.TLSBundleID)
		}
	}

	for _, e := range qm.ServiceRegistry {
		if err := validateServiceEntry(e); err != nil {
			return err
		}
		if _, ok := clusterIDs[e.ClusterID]; !ok {
			return fmt.Errorf("service %q: cluster_id %q not defined in this file", e.ServiceName, e.ClusterID)
		}
		if _, ok := nodeIDs[e.NodeID]; !ok {
			return fmt.Errorf("service %q: node_id %q not defined in this file", e.ServiceName, e.NodeID)
		}
	}
	return nil
}

// errSentinel keeps the import set honest when only some of the validators
// reach into errors.New (a few helpers do, others don't).
var _ = errors.New
