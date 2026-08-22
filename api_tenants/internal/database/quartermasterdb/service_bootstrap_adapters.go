package quartermasterdb

import (
	"context"
	"database/sql"
)

type GetNodeBootstrapLocationRow struct {
	ClusterID string
	NodeIP    sql.NullString
}

// GetNodeBootstrapLocation retains nullable IP semantics for a COALESCE over
// three nullable inet columns, which sqlc cannot infer accurately.
func (q *Queries) GetNodeBootstrapLocation(ctx context.Context, nodeID string) (GetNodeBootstrapLocationRow, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT cluster_id,
		       COALESCE(host(wireguard_ip), host(internal_ip), host(external_ip))
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID)
	var result GetNodeBootstrapLocationRow
	err := row.Scan(&result.ClusterID, &result.NodeIP)
	return result, err
}

type UpdateBootstrappedServiceInstanceParams struct {
	AdvertiseHost  string
	HealthEndpoint *string
	Version        string
	NodeID         *string
	Metadata       *string
	Protocol       string
	Port           int32
	ID             string
}

func (q *Queries) UpdateBootstrappedServiceInstance(ctx context.Context, arg UpdateBootstrappedServiceInstanceParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET advertise_host = $1,
		    health_endpoint_override = $2,
		    version = $3,
		    node_id = COALESCE($4, node_id),
		    metadata = COALESCE($5::jsonb, metadata),
		    protocol = $6,
		    port = $7,
		    status = 'running',
		    health_status = 'unknown',
		    started_at = COALESCE(started_at, NOW()),
		    stopped_at = NULL,
		    last_health_check = NULL,
		    updated_at = NOW()
		WHERE id = $8::uuid
	`, arg.AdvertiseHost, arg.HealthEndpoint, arg.Version, arg.NodeID, arg.Metadata, arg.Protocol, arg.Port, arg.ID)
	return err
}

type CreateBootstrappedServiceInstanceParams struct {
	InstanceID     string
	ClusterID      string
	NodeID         *string
	ServiceID      string
	Protocol       string
	AdvertiseHost  string
	HealthEndpoint *string
	Version        string
	Port           int32
	Metadata       *string
}

func (q *Queries) CreateBootstrappedServiceInstance(ctx context.Context, arg CreateBootstrappedServiceInstanceParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.service_instances
			(instance_id, cluster_id, node_id, service_id, protocol, advertise_host, health_endpoint_override, version, port, metadata, status, health_status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, COALESCE($10::jsonb, '{}'::jsonb), 'running', 'unknown', NOW(), NOW())
	`, arg.InstanceID, arg.ClusterID, arg.NodeID, arg.ServiceID, arg.Protocol, arg.AdvertiseHost, arg.HealthEndpoint, arg.Version, arg.Port, arg.Metadata)
	return err
}

type StopDuplicateServiceInstancesParams struct {
	ServiceID     string
	ClusterID     string
	InstanceID    string
	AdvertiseHost string
	Protocol      string
	Port          int32
}

func (q *Queries) StopDuplicateServiceInstances(ctx context.Context, arg StopDuplicateServiceInstancesParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET status = 'stopped', stopped_at = NOW(), updated_at = NOW()
		WHERE service_id = $1 AND cluster_id = $2 AND instance_id != $3
		  AND protocol = $5
		  AND status != 'stopped'
		  AND COALESCE(advertise_host, '') = $4
		  AND COALESCE(port, 0) = $6
	`, arg.ServiceID, arg.ClusterID, arg.InstanceID, arg.AdvertiseHost, arg.Protocol, arg.Port)
	return err
}

type StopStaleFoghornControlListenersParams struct {
	ServiceID     string
	ClusterID     string
	InstanceID    string
	AdvertiseHost string
	Protocol      string
	Port          int32
}

func (q *Queries) StopStaleFoghornControlListeners(ctx context.Context, arg StopStaleFoghornControlListenersParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET status = 'stopped', stopped_at = NOW(), updated_at = NOW()
		WHERE service_id = $1 AND cluster_id = $2 AND instance_id != $3
		  AND protocol = $4
		  AND status != 'stopped'
		  AND COALESCE(advertise_host, '') = $5
		  AND COALESCE(port, 0) != $6
	`, arg.ServiceID, arg.ClusterID, arg.InstanceID, arg.Protocol, arg.AdvertiseHost, arg.Port)
	return err
}
