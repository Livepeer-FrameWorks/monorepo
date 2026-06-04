package resolvers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func functionSource(t *testing.T, path, name string) string {
	t.Helper()

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		return string(src[start:end])
	}
	t.Fatalf("function %s not found in %s", name, path)
	return ""
}

func assertContains(t *testing.T, src, want string) {
	t.Helper()
	if !strings.Contains(src, want) {
		t.Fatalf("expected source to contain %q\n%s", want, src)
	}
}

func assertNotContains(t *testing.T, src, forbidden string) {
	t.Helper()
	if strings.Contains(src, forbidden) {
		t.Fatalf("expected source not to contain %q\n%s", forbidden, src)
	}
}

func TestAnalyticsAccessScopeContracts(t *testing.T) {
	t.Run("tenant routing and federation stay tenant scoped", func(t *testing.T) {
		for _, fn := range []string{
			"DoGetClusterTrafficMatrix",
			"DoGetFederationEventsConnection",
			"DoGetFederationSummary",
		} {
			src := functionSource(t, "analytics_connections.go", fn)
			assertContains(t, src, "tenantIDFromContext(ctx)")
			assertNotContains(t, src, "requireClusterOperatorTenant")
			assertNotContains(t, src, "requireOwnedNode")
		}
	})

	t.Run("client qoe stays tenant scoped", func(t *testing.T) {
		src := functionSource(t, "analytics_connections.go", "DoGetClientMetrics5mConnection")
		assertContains(t, src, "GetClientMetrics5m(ctx, tenantID")
		assertNotContains(t, src, "requireClusterOperatorTenant")
		assertNotContains(t, src, "requireOwnedNode")
	})

	t.Run("node operations require owned cluster access", func(t *testing.T) {
		for _, fn := range []string{
			"DoGetNodeMetricsAggregated",
			"DoGetNodePerformance5mConnection",
		} {
			src := functionSource(t, "analytics_connections.go", fn)
			assertContains(t, src, "requireOwnedNode")
			assertContains(t, src, "requireClusterOperatorTenant")
		}

		for _, fn := range []string{"loadNodeMetrics", "loadNodeMetrics1h"} {
			src := functionSource(t, "analytics.go", fn)
			assertContains(t, src, "requireOwnedNode")
			assertContains(t, src, "requireClusterOperatorTenant")
		}
	})

	t.Run("service health inventory requires owned cluster access", func(t *testing.T) {
		src := functionSource(t, "../../graph/schema.resolvers.go", "ServiceInstancesHealth")
		assertContains(t, src, "RequireClusterOperatorTenant(ctx)")
	})

	t.Run("network status splits public and owner views", func(t *testing.T) {
		src := functionSource(t, "analytics_connections.go", "DoGetNetworkStatus")
		helper := functionSource(t, "analytics_connections.go", "listPublicNetworkClusters")
		assertContains(t, helper, "publicCtx := publicTopologyReadContext(ctx)")
		assertContains(t, helper, "ListPublicTopologyClusters(publicCtx)")
		assertContains(t, src, "ListMySubscriptions(ctx")
		assertContains(t, src, "ListClustersByOwner(ctx, tenantID")
		assertContains(t, src, "topologyClusterIDs")
		assertContains(t, src, "publicTopologyClusterIDs")
		assertContains(t, src, "exposedServiceInstanceClusterIDs")
		assertContains(t, src, "topologyServiceInstancesRaw")
		assertContains(t, src, "appendClusters(publicClustersResp.GetClusters(), true, true, false)")
		assertContains(t, src, "appendClusters(accessResp.GetClusters(), true, false, false)")
		assertContains(t, src, "appendClusters(ownedClustersResp.GetClusters(), true, false, true)")
		assertContains(t, src, "delete(publicTopologyClusterIDs, clusterID)")
		assertContains(t, src, "for clusterID := range topologyClusterIDs")
		assertContains(t, src, "readCtx = publicCtx")
		assertContains(t, src, "if _, expose := exposedServiceInstanceClusterIDs[clusterID]; expose")
		assertContains(t, src, "if _, visible := visibleClusterIDs[ls.ClusterId]; visible")
		assertContains(t, src, "operatorView")
		assertNotContains(t, src, "if operatorView {\n\t\t\t\tserviceInstances = append")
		assertNotContains(t, src, "if operatorView {\n\t\t\tnetworkNodes = append")
	})

	t.Run("orchestrator public scope ignores signed-in tenant visibility", func(t *testing.T) {
		src := functionSource(t, "orchestrators.go", "networkOrchestratorOwnerTenants")
		assertContains(t, src, "clustersResp, publicCtx, err := r.listPublicNetworkClusters(ctx)")
		assertContains(t, src, `cacheParts := []string{"public"}`)
		assertContains(t, src, `cacheParts = []string{"tenant", tenantID}`)
		assertContains(t, src, "publicClusterIDs")
		assertContains(t, src, "ListMySubscriptions(ctx")
		assertContains(t, src, "ListClustersByOwner(ctx, tenantID")
		assertContains(t, src, "delete(publicClusterIDs, clusterID)")
		assertContains(t, src, "readCtx = publicCtx")
		assertContains(t, src, "ListServiceInstances(readCtx, clusterID")
		assertContains(t, src, "orchestrator scope: failed to load tenant cluster access")
		assertContains(t, src, "orchestrator scope: failed to load owned clusters")
		assertContains(t, src, "orchestrator scope: failed to load cluster service instances")
		assertNotContains(t, src, "load tenant cluster access for orchestrator scope")
		assertNotContains(t, src, "load owned clusters for orchestrator scope")
		assertNotContains(t, src, "load orchestrator gateway instances for cluster")
	})
}

func TestInfrastructureAccessScopeContracts(t *testing.T) {
	t.Run("node and service connections use owner-gated loaders", func(t *testing.T) {
		nodes := functionSource(t, "infrastructure.go", "DoGetNodesConnection")
		assertContains(t, nodes, "DoGetNodes(ctx")
		services := functionSource(t, "infrastructure.go", "DoGetServiceInstancesConnection")
		assertContains(t, services, "DoGetServiceInstances(ctx")
	})

	t.Run("owned node checks resolve the node cluster before allowing access", func(t *testing.T) {
		src := functionSource(t, "infrastructure_access.go", "requireOwnedNode")
		assertContains(t, src, "GetNode(ctx, nodeID)")
		assertContains(t, src, "requireOwnedCluster(ctx, node.GetClusterId())")
	})
}
