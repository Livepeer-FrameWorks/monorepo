package quartermasterdb

import (
	"strings"
	"testing"
)

func TestAuthorityQueriesRejectUnknownClusterAccessProvenance(t *testing.T) {
	queries := map[string]string{
		"entitlement ids":       listTenantEntitledClusterIDs,
		"effective access":      listTenantEffectiveAccess,
		"primary routing":       getTenantPrimaryClusterRouting,
		"resource limits":       getTenantClusterResourceLimits,
		"routing peers":         listTenantClusterRoutingPeers,
		"active access check":   tenantHasActiveClusterAccess,
		"peer cluster envelope": listPeerClusters,
		"tenant aliases":        listAliasedTenantsForCluster,
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "access_source <> 'unknown'") {
				t.Fatalf("authority query does not fail unknown provenance closed:\n%s", query)
			}
		})
	}
}

func TestCommercialMaterializationPreservesStrongerAuthority(t *testing.T) {
	for _, source := range []string{"operator_override", "private_invite", "owner"} {
		if !strings.Contains(materializeTenantClusterAccess, source) {
			t.Fatalf("materialization query does not preserve active %s authority", source)
		}
	}
	if !strings.Contains(revokeMaterializedTenantClusterAccess, "access_source =") {
		t.Fatal("commercial revocation is not source-bound")
	}
	if !strings.Contains(materializeTenantClusterAccess, "EXCLUDED.subscription_status = 'pending_approval'") ||
		!strings.Contains(materializeTenantClusterAccess, "tenant_cluster_access.is_active = true") {
		t.Fatal("pending marketplace request can downgrade an already-active grant")
	}
}
