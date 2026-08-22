package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (q *Queries) InfrastructureClusterExists(ctx context.Context, clusterID string) (bool, error) {
	var exists bool
	err := q.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM quartermaster.infrastructure_clusters WHERE cluster_id = $1)",
		clusterID,
	).Scan(&exists)
	return exists, err
}

type UpsertInfrastructureNodeParams struct {
	NodeID, ClusterID, NodeName, NodeType string
	InternalIP, ExternalIP                *string
	WireguardIP, WireguardPublicKey       *string
	WireguardListenPort                   any
	Region, AvailabilityZone              *string
	Latitude, Longitude                   any
	CPUCores, MemoryGB, DiskGB            *int32
	Now                                   time.Time
}

func (q *Queries) UpsertInfrastructureNode(ctx context.Context, arg UpsertInfrastructureNodeParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type,
		                                                internal_ip, external_ip, wireguard_ip, wireguard_public_key,
		                                                wireguard_listen_port,
		                                                region, availability_zone,
		                                                latitude, longitude,
		                                                cpu_cores, memory_gb, disk_gb, status,
		                                                enrollment_origin,
		                                                created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, 'active', 'runtime_enrolled', $17, $17)
		ON CONFLICT (node_id) DO UPDATE SET
			cluster_id            = EXCLUDED.cluster_id,
			node_name             = EXCLUDED.node_name,
			node_type             = EXCLUDED.node_type,
			internal_ip           = COALESCE(EXCLUDED.internal_ip, quartermaster.infrastructure_nodes.internal_ip),
			external_ip           = COALESCE(EXCLUDED.external_ip, quartermaster.infrastructure_nodes.external_ip),
			wireguard_ip          = COALESCE(EXCLUDED.wireguard_ip, quartermaster.infrastructure_nodes.wireguard_ip),
			wireguard_public_key  = COALESCE(EXCLUDED.wireguard_public_key, quartermaster.infrastructure_nodes.wireguard_public_key),
			wireguard_listen_port = COALESCE(EXCLUDED.wireguard_listen_port, quartermaster.infrastructure_nodes.wireguard_listen_port),
			region                = COALESCE(EXCLUDED.region, quartermaster.infrastructure_nodes.region),
			availability_zone     = COALESCE(EXCLUDED.availability_zone, quartermaster.infrastructure_nodes.availability_zone),
			latitude              = COALESCE(EXCLUDED.latitude, quartermaster.infrastructure_nodes.latitude),
			longitude             = COALESCE(EXCLUDED.longitude, quartermaster.infrastructure_nodes.longitude),
			cpu_cores             = COALESCE(EXCLUDED.cpu_cores, quartermaster.infrastructure_nodes.cpu_cores),
			memory_gb             = COALESCE(EXCLUDED.memory_gb, quartermaster.infrastructure_nodes.memory_gb),
			disk_gb               = COALESCE(EXCLUDED.disk_gb, quartermaster.infrastructure_nodes.disk_gb),
			status                = 'active',
			updated_at            = EXCLUDED.updated_at
	`, arg.NodeID, arg.ClusterID, arg.NodeName, arg.NodeType, arg.InternalIP, arg.ExternalIP,
		arg.WireguardIP, arg.WireguardPublicKey, arg.WireguardListenPort, arg.Region, arg.AvailabilityZone,
		arg.Latitude, arg.Longitude, arg.CPUCores, arg.MemoryGB, arg.DiskGB, arg.Now)
	return err
}

type NodeFingerprintRow struct {
	TenantID, NodeID string
}

type NodeFingerprintLookup int

const (
	NodeFingerprintByMachineID NodeFingerprintLookup = iota
	NodeFingerprintByMACs
	NodeFingerprintBySeenIP
)

func (q *Queries) ResolveNodeFingerprint(ctx context.Context, kind NodeFingerprintLookup, value string) (NodeFingerprintRow, error) {
	predicate := "nf.fingerprint_machine_sha256 = $1"
	tail := ""
	switch kind {
	case NodeFingerprintByMachineID:
	case NodeFingerprintByMACs:
		predicate = "nf.fingerprint_macs_sha256 = $1"
	case NodeFingerprintBySeenIP:
		predicate = "$1::inet = ANY(nf.seen_ips)"
		tail = "\n\t\tORDER BY nf.last_seen DESC\n\t\tLIMIT 1"
	default:
		return NodeFingerprintRow{}, fmt.Errorf("unsupported fingerprint lookup %d", kind)
	}
	row := q.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT nf.tenant_id::text, nf.node_id
		FROM quartermaster.node_fingerprints nf
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
		WHERE %s
		  AND c.is_active = TRUE%s
	`, predicate, tail), value)
	var out NodeFingerprintRow
	err := row.Scan(&out.TenantID, &out.NodeID)
	return out, err
}

func (q *Queries) UpsertFingerprintSeenIP(ctx context.Context, nodeID, peerIP string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.node_fingerprints
		SET last_seen = NOW(), seen_ips = array_append(seen_ips, $1::inet)
		WHERE node_id = $2 AND NOT ($1::inet = ANY(seen_ips))
	`, peerIP, nodeID)
	return err
}

type ClusterMeshConfigRow struct {
	CIDR sql.NullString
	Port sql.NullInt32
}

func (q *Queries) GetClusterMeshConfig(ctx context.Context, clusterID string) (ClusterMeshConfigRow, error) {
	var out ClusterMeshConfigRow
	err := q.db.QueryRowContext(ctx, `
		SELECT wg_mesh_cidr, wg_listen_port
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1
	`, clusterID).Scan(&out.CIDR, &out.Port)
	return out, err
}
