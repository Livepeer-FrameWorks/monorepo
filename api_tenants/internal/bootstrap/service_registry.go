package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// ReconcileServiceRegistry reconciles every ServiceRegistryEntry into
// quartermaster.services + quartermaster.service_instances. Stable instance
// key: (service_id, cluster_id, node_id, protocol, port). Drift on the stable
// tuple fails loud; advertise_host, health_endpoint, and metadata are mutable.
//
// Self-registering services (those that call BootstrapService at startup) are
// excluded by the renderer — see cli/pkg/clusterderive.SelfRegisters — so this
// reconciler only writes declarative rows for non-self-registering services.
// Metadata is whatever the manifest hands it; the reconciler does not enrich
// it from on-host state.
func ReconcileServiceRegistry(ctx context.Context, exec DBTX, entries []ServiceRegistryEntry) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileServiceRegistry: nil executor")
	}

	res := Result{}
	for _, e := range entries {
		if err := validateServiceEntry(e); err != nil {
			return Result{}, err
		}
		serviceID, err := ensureServiceCatalogRow(ctx, exec, e)
		if err != nil {
			return Result{}, fmt.Errorf("service %q: %w", e.ServiceName, err)
		}
		advHost, err := resolveNodeAdvertiseHost(ctx, exec, e.ClusterID, e.NodeID)
		if err != nil {
			return Result{}, fmt.Errorf("service %q: %w", e.ServiceName, err)
		}
		key := fmt.Sprintf("%s@%s/%s", e.ServiceName, e.NodeID, e.ClusterID)
		action, err := upsertServiceInstance(ctx, exec, serviceID, advHost, e)
		if err != nil {
			return Result{}, fmt.Errorf("service %q: %w", e.ServiceName, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, key)
		case "updated":
			res.Updated = append(res.Updated, key)
		case "noop":
			res.Noop = append(res.Noop, key)
		}
	}

	return res, nil
}

func validateServiceEntry(e ServiceRegistryEntry) error {
	if e.ServiceName == "" {
		return errors.New("service_name required")
	}
	if e.Type == "" {
		return fmt.Errorf("service %q: type required", e.ServiceName)
	}
	if e.ClusterID == "" {
		return fmt.Errorf("service %q: cluster_id required", e.ServiceName)
	}
	if e.NodeID == "" {
		return fmt.Errorf("service %q: node_id required", e.ServiceName)
	}
	if e.Port == 0 {
		return fmt.Errorf("service %q: port required", e.ServiceName)
	}
	return nil
}

// ensureServiceCatalogRow inserts or converges the catalog row and returns its
// service_id. A service that is both declared here and self-bootstrapping
// (purser) can create the same row through BootstrapService while this runs,
// so the insert keeps a concurrently committed row and the update then brings
// that row to the declared plane, type and protocol.
func ensureServiceCatalogRow(ctx context.Context, exec DBTX, e ServiceRegistryEntry) (string, error) {
	queries := quartermasterdb.New(exec)
	serviceID, err := queries.GetBootstrapServiceCatalog(ctx, e.ServiceName)
	protocol := e.Protocol
	if protocol == "" {
		protocol = "http"
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		serviceID = e.ServiceName
		if insertErr := queries.InsertBootstrapServiceCatalog(ctx, quartermasterdb.InsertBootstrapServiceCatalogParams{
			ServiceID: serviceID, Name: e.ServiceName, Plane: serviceEntryPlane(e), Type: e.Type, Protocol: protocol,
		}); insertErr != nil {
			return "", fmt.Errorf("insert service catalog: %w", insertErr)
		}
	case err != nil:
		return "", fmt.Errorf("probe service catalog: %w", err)
	}
	if updateErr := queries.UpdateBootstrapServiceCatalog(ctx, quartermasterdb.UpdateBootstrapServiceCatalogParams{
		ServiceID: serviceID, Name: e.ServiceName, Plane: serviceEntryPlane(e), Type: e.Type, Protocol: protocol,
	}); updateErr != nil {
		return "", fmt.Errorf("update service catalog: %w", updateErr)
	}
	return serviceID, nil
}

func serviceEntryPlane(e ServiceRegistryEntry) string {
	if topology.IsInfraKind(e.Type) || e.Metadata["topology_provider"] == "infra" || e.Metadata["peer_only"] == "true" {
		return "infra"
	}
	return "control"
}

// resolveNodeAdvertiseHost resolves the node's WireGuard IP for use as
// advertise_host, the same convention BootstrapService uses when node_id is
// supplied. Fails loud if the node lacks a registered mesh address.
func resolveNodeAdvertiseHost(ctx context.Context, exec DBTX, clusterID, nodeID string) (string, error) {
	row, err := quartermasterdb.New(exec).GetBootstrapNodeAdvertiseHost(ctx, nodeID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("node %q not found (run nodes reconcile first)", nodeID)
	case err != nil:
		return "", fmt.Errorf("probe node: %w", err)
	}
	if row.ClusterID != clusterID {
		return "", fmt.Errorf("node %q belongs to cluster %q, not %q", nodeID, row.ClusterID, clusterID)
	}
	if row.WireguardIp == "" {
		return "", fmt.Errorf("node %q has no wireguard_ip", nodeID)
	}
	return row.WireguardIp, nil
}

func upsertServiceInstance(ctx context.Context, exec DBTX, serviceID, advHost string, e ServiceRegistryEntry) (string, error) {
	protocol := e.Protocol
	if protocol == "" {
		protocol = "http"
	}
	metadataJSON, err := encodeMetadata(e.Metadata)
	if err != nil {
		return "", fmt.Errorf("encode metadata: %w", err)
	}

	queries := quartermasterdb.New(exec)
	current, err := queries.GetBootstrapServiceInstance(ctx, quartermasterdb.GetBootstrapServiceInstanceParams{
		ServiceID: serviceID, ClusterID: e.ClusterID, NodeID: e.NodeID,
		Protocol: protocol, Port: int32(e.Port),
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// instance_id is UNIQUE in quartermaster.service_instances. Two rows
		// for the same (service, node) but different protocol or port are
		// legal under the row-level stable key, so the instance_id has to
		// distinguish them too — otherwise the second insert collides.
		instanceID := fmt.Sprintf("inst-%s-%s-%s-%d", e.ServiceName, e.NodeID, protocol, e.Port)
		if insertErr := queries.InsertBootstrapServiceInstance(ctx, quartermasterdb.InsertBootstrapServiceInstanceParams{
			InstanceID: instanceID, ClusterID: e.ClusterID,
			NodeID: e.NodeID, ServiceID: serviceID,
			Protocol: protocol, AdvertiseHost: advHost, HealthEndpoint: e.HealthEndpoint,
			Port: int32(e.Port), Metadata: metadataJSON,
			RegistryOwner: e.registryOwner,
		}); insertErr != nil {
			return "", fmt.Errorf("insert service_instance: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe service_instance: %w", err)
	}
	if e.registryOwner != "" && current.RegistryOwner != e.registryOwner {
		result, err := queries.ClaimManifestRegistryInstance(ctx, quartermasterdb.ClaimManifestRegistryInstanceParams{ID: current.ID, ClusterID: e.ClusterID, Owner: e.registryOwner})
		if err != nil {
			return "", err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return "", fmt.Errorf("service instance is owned by another manifest or a runtime registrar")
		}
	}

	if current.AdvertiseHost == advHost && current.HealthEndpoint == e.HealthEndpoint && jsonObjectEq(current.Metadata, metadataJSON) && current.Status != "stopped" {
		return "noop", nil
	}
	if err := queries.UpdateBootstrapServiceInstance(ctx, quartermasterdb.UpdateBootstrapServiceInstanceParams{
		ID: current.ID, AdvertiseHost: advHost, HealthEndpoint: e.HealthEndpoint, Metadata: metadataJSON,
	}); err != nil {
		return "", fmt.Errorf("update service_instance: %w", err)
	}
	return "updated", nil
}

func validateRegistryScope(scope *ServiceRegistryScope, clusters []Cluster) error {
	if scope == nil {
		return nil
	}
	if scope.Owner == "" || strings.TrimSpace(scope.Owner) != scope.Owner || len(scope.Owner) > 100 || len(scope.Clusters) == 0 {
		return errors.New("registry_scope requires a non-empty owner and declared clusters")
	}
	seen := make(map[string]bool)
	for _, cluster := range scope.Clusters {
		if seen[cluster] || !slices.ContainsFunc(clusters, func(c Cluster) bool { return c.ID == cluster }) {
			return fmt.Errorf("registry_scope cluster %q is duplicated or absent from this manifest", cluster)
		}
		seen[cluster] = true
	}
	adopted := make(map[string]bool)
	for _, item := range scope.Adopt {
		if item.InstanceID == "" || item.ServiceName == "" || item.NodeID == "" || item.Protocol == "" || item.Port <= 0 || item.Port > 65535 || !seen[item.ClusterID] || adopted[item.InstanceID] {
			return errors.New("registry_scope adoption requires a unique instance and its exact service, scoped cluster, node, protocol and port")
		}
		adopted[item.InstanceID] = true
	}
	return nil
}

// ReconcileManagedServiceRegistry retires only an explicitly complete owner's
// inventory. The caller's transaction makes ownership, upserts and stops atomic.
func ReconcileManagedServiceRegistry(ctx context.Context, exec DBTX, entries []ServiceRegistryEntry, scope *ServiceRegistryScope, clusters []Cluster) (Result, error) {
	if err := validateRegistryScope(scope, clusters); err != nil {
		return Result{}, err
	}
	if scope == nil {
		return ReconcileServiceRegistry(ctx, exec, entries)
	}
	q := quartermasterdb.New(exec)
	if err := q.LockManifestRegistryOwner(ctx, scope.Owner); err != nil {
		return Result{}, err
	}
	for _, item := range scope.Adopt {
		result, err := q.AdoptLegacyManifestRegistryInstance(ctx, quartermasterdb.AdoptLegacyManifestRegistryInstanceParams{
			Owner: scope.Owner, InstanceID: item.InstanceID, ServiceName: item.ServiceName, ClusterID: item.ClusterID, NodeID: item.NodeID, Protocol: item.Protocol, Port: int32(item.Port),
		})
		if err != nil {
			return Result{}, err
		}
		if count, err := result.RowsAffected(); err != nil || count != 1 {
			return Result{}, fmt.Errorf("legacy instance %q does not match adoption evidence or belongs to another registrar", item.InstanceID)
		}
	}
	owned := append([]ServiceRegistryEntry(nil), entries...)
	for i := range owned {
		if slices.Contains(scope.Clusters, owned[i].ClusterID) {
			owned[i].registryOwner = scope.Owner
		}
	}
	result, err := ReconcileServiceRegistry(ctx, exec, owned)
	if err != nil || !scope.Complete {
		return result, err
	}
	desiredIDs := make([]string, 0, len(owned))
	for _, entry := range owned {
		if entry.registryOwner == "" {
			continue
		}
		service, catalogErr := q.GetBootstrapServiceCatalog(ctx, entry.ServiceName)
		if catalogErr != nil {
			return Result{}, catalogErr
		}
		protocol := entry.Protocol
		if protocol == "" {
			protocol = "http"
		}
		row, instanceErr := q.GetBootstrapServiceInstance(ctx, quartermasterdb.GetBootstrapServiceInstanceParams{ServiceID: service, ClusterID: entry.ClusterID, NodeID: entry.NodeID, Protocol: protocol, Port: int32(entry.Port)})
		if instanceErr != nil {
			return Result{}, instanceErr
		}
		desiredIDs = append(desiredIDs, row.ID)
	}
	result.Stopped, err = q.StopRemovedManifestRegistryInstances(ctx, quartermasterdb.StopRemovedManifestRegistryInstancesParams{Owner: scope.Owner, ClusterIds: scope.Clusters, DesiredIds: desiredIDs})
	return result, err
}

func encodeMetadata(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsonObjectEq(a, b string) bool {
	var aa, bb map[string]any
	if err := json.Unmarshal([]byte(a), &aa); err != nil {
		return a == b
	}
	if err := json.Unmarshal([]byte(b), &bb); err != nil {
		return a == b
	}
	if len(aa) != len(bb) {
		return false
	}
	for k, va := range aa {
		vb, ok := bb[k]
		if !ok || fmt.Sprint(va) != fmt.Sprint(vb) {
			return false
		}
	}
	return true
}
