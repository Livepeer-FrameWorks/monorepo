package bootstrap

import (
	"context"
	"errors"
	"fmt"
)

// Reconcile applies the entire QuartermasterSection in dependency order. It
// runs against the caller-supplied executor — the cobra dispatcher opens an
// outer transaction and decides commit (apply) vs rollback (--dry-run). No
// reconciler in this package opens its own tx.
//
//  1. tenants                       — establishes alias→UUID mapping.
//  2. clusters                      — references owner_tenant via alias.
//  3. nodes                         — references cluster_id.
//  4. ingress (TLS bundles, sites)  — references cluster_id and node_id.
//  5. service_registry              — references cluster_id, node_id; depends
//     on infrastructure_nodes for advertise_host.
//  6. system_tenant_cluster_access  — depends on tenants + clusters being live.
//
// A phase failure short-circuits and returns the partial result for diagnostics
// plus the wrapping error. The caller is expected to roll back on any error
// regardless of partial progress.
type Sections struct {
	Tenants            Result
	Clusters           Result
	Nodes              Result
	Ingress            Result
	ServiceRegistry    Result
	SystemTenantAccess Result
}

// Reconcile is the single entrypoint the bootstrap binary calls. It is idempotent.
func Reconcile(ctx context.Context, exec DBTX, qm QuartermasterSection) (*Sections, error) {
	if exec == nil {
		return nil, errors.New("Reconcile: nil executor")
	}
	out := &Sections{}

	aliases, tenantsRes, err := ReconcileTenants(ctx, exec, qm.SystemTenant, qm.Tenants)
	out.Tenants = tenantsRes
	if err != nil {
		return out, fmt.Errorf("tenants: %w", err)
	}

	clustersRes, err := ReconcileClusters(ctx, exec, qm.Clusters, aliases)
	out.Clusters = clustersRes
	if err != nil {
		return out, fmt.Errorf("clusters: %w", err)
	}

	nodesRes, err := ReconcileNodes(ctx, exec, qm.Nodes)
	out.Nodes = nodesRes
	if err != nil {
		return out, fmt.Errorf("nodes: %w", err)
	}

	ingressRes, err := ReconcileIngress(ctx, exec, qm.Ingress)
	out.Ingress = ingressRes
	if err != nil {
		return out, fmt.Errorf("ingress: %w", err)
	}

	srRes, err := ReconcileServiceRegistry(ctx, exec, qm.ServiceRegistry)
	out.ServiceRegistry = srRes
	if err != nil {
		return out, fmt.Errorf("service_registry: %w", err)
	}

	accessRes, err := ReconcileSystemTenantClusterAccess(ctx, exec, qm.SystemTenantClusterAccess, aliases)
	out.SystemTenantAccess = accessRes
	if err != nil {
		return out, fmt.Errorf("system_tenant_cluster_access: %w", err)
	}

	return out, nil
}
