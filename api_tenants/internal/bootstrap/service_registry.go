package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

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

// ensureServiceCatalogRow inserts the catalog row if missing and returns its
// service_id. Mirrors the gRPC handler's ensureServiceExists semantics but uses
// the bootstrap transaction directly (no advisory lock needed — bootstrap is
// already serialized by being a single-process invocation).
func ensureServiceCatalogRow(ctx context.Context, exec DBTX, e ServiceRegistryEntry) (string, error) {
	queries := quartermasterdb.New(exec)
	serviceID, err := queries.GetBootstrapServiceCatalog(ctx, e.ServiceName)
	protocol := e.Protocol
	if protocol == "" {
		protocol = "http"
	}
	if err == nil {
		if updateErr := queries.UpdateBootstrapServiceCatalog(ctx, quartermasterdb.UpdateBootstrapServiceCatalogParams{
			ServiceID: serviceID, Name: e.ServiceName, Plane: serviceEntryPlane(e), Type: e.Type, Protocol: protocol,
		}); updateErr != nil {
			return "", fmt.Errorf("update service catalog: %w", updateErr)
		}
		return serviceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("probe service catalog: %w", err)
	}
	if err := queries.InsertBootstrapServiceCatalog(ctx, quartermasterdb.InsertBootstrapServiceCatalogParams{
		ServiceID: e.ServiceName, Name: e.ServiceName, Plane: serviceEntryPlane(e), Type: e.Type, Protocol: protocol,
	}); err != nil {
		return "", fmt.Errorf("insert service catalog: %w", err)
	}
	return e.ServiceName, nil
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
		}); insertErr != nil {
			return "", fmt.Errorf("insert service_instance: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe service_instance: %w", err)
	}

	if current.AdvertiseHost == advHost && current.HealthEndpoint == e.HealthEndpoint && jsonObjectEq(current.Metadata, metadataJSON) {
		return "noop", nil
	}
	if err := queries.UpdateBootstrapServiceInstance(ctx, quartermasterdb.UpdateBootstrapServiceInstanceParams{
		ID: current.ID, AdvertiseHost: advHost, HealthEndpoint: e.HealthEndpoint, Metadata: metadataJSON,
	}); err != nil {
		return "", fmt.Errorf("update service_instance: %w", err)
	}
	return "updated", nil
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
