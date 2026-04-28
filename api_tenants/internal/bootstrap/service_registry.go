package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	const probeSQL = `
		SELECT service_id FROM quartermaster.services WHERE service_id = $1 OR name = $1`
	var serviceID string
	err := exec.QueryRowContext(ctx, probeSQL, e.ServiceName).Scan(&serviceID)
	if err == nil {
		return serviceID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("probe service catalog: %w", err)
	}
	protocol := e.Protocol
	if protocol == "" {
		protocol = "http"
	}
	const insertSQL = `
		INSERT INTO quartermaster.services
			(service_id, name, plane, type, protocol, is_active, created_at, updated_at)
		VALUES ($1, $2, 'control', $3, $4, true, NOW(), NOW())`
	if _, err := exec.ExecContext(ctx, insertSQL, e.ServiceName, e.ServiceName, e.Type, protocol); err != nil {
		return "", fmt.Errorf("insert service catalog: %w", err)
	}
	return e.ServiceName, nil
}

// resolveNodeAdvertiseHost resolves the node's WireGuard IP for use as
// advertise_host, the same convention BootstrapService uses when node_id is
// supplied. Fails loud if the node lacks a registered mesh address.
func resolveNodeAdvertiseHost(ctx context.Context, exec DBTX, clusterID, nodeID string) (string, error) {
	const q = `
		SELECT cluster_id, COALESCE(wireguard_ip::text, '')
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1`
	var nodeCluster, wgIP string
	err := exec.QueryRowContext(ctx, q, nodeID).Scan(&nodeCluster, &wgIP)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("node %q not found (run nodes reconcile first)", nodeID)
	case err != nil:
		return "", fmt.Errorf("probe node: %w", err)
	}
	if nodeCluster != clusterID {
		return "", fmt.Errorf("node %q belongs to cluster %q, not %q", nodeID, nodeCluster, clusterID)
	}
	if wgIP == "" {
		return "", fmt.Errorf("node %q has no wireguard_ip", nodeID)
	}
	return wgIP, nil
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

	const probeSQL = `
		SELECT id::text,
		       COALESCE(advertise_host, ''),
		       COALESCE(health_endpoint_override, ''),
		       COALESCE(metadata::text, '{}')
		FROM quartermaster.service_instances
		WHERE service_id = $1 AND cluster_id = $2 AND node_id = $3
		  AND protocol = $4 AND port = $5
		ORDER BY updated_at DESC NULLS LAST
		LIMIT 1`
	var (
		id, curAdvHost, curHealth, curMetadata string
	)
	err = exec.QueryRowContext(ctx, probeSQL, serviceID, e.ClusterID, e.NodeID, protocol, e.Port).Scan(&id, &curAdvHost, &curHealth, &curMetadata)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// instance_id is UNIQUE in quartermaster.service_instances. Two rows
		// for the same (service, node) but different protocol or port are
		// legal under the row-level stable key, so the instance_id has to
		// distinguish them too — otherwise the second insert collides.
		instanceID := fmt.Sprintf("inst-%s-%s-%s-%d", e.ServiceName, e.NodeID, protocol, e.Port)
		const insertSQL = `
			INSERT INTO quartermaster.service_instances
				(instance_id, cluster_id, node_id, service_id, protocol, advertise_host,
				 health_endpoint_override, port, metadata, status, health_status,
				 created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb,
			        'running', 'unknown', NOW(), NOW())`
		if _, insertErr := exec.ExecContext(ctx, insertSQL,
			instanceID, e.ClusterID, e.NodeID, serviceID, protocol, advHost,
			e.HealthEndpoint, e.Port, metadataJSON,
		); insertErr != nil {
			return "", fmt.Errorf("insert service_instance: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe service_instance: %w", err)
	}

	if curAdvHost == advHost && curHealth == e.HealthEndpoint && jsonObjectEq(curMetadata, metadataJSON) {
		return "noop", nil
	}
	const updateSQL = `
		UPDATE quartermaster.service_instances
		SET advertise_host = $2,
		    health_endpoint_override = $3,
		    metadata = $4::jsonb,
		    updated_at = NOW()
		WHERE id = $1::uuid`
	if _, err := exec.ExecContext(ctx, updateSQL, id, advHost, e.HealthEndpoint, metadataJSON); err != nil {
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
