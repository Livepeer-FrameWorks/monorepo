package quartermasterdb

import (
	"context"
	"database/sql"
	"errors"
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
	PublicKeyEd25519 []byte
}

type NodeFingerprintLookup int

const (
	NodeFingerprintByMachineID NodeFingerprintLookup = iota
	NodeFingerprintByMACs
	NodeFingerprintBySeenIP
)

var ErrAmbiguousNodeFingerprint = errors.New("ambiguous node fingerprint")

func (q *Queries) ResolveNodeFingerprint(ctx context.Context, kind NodeFingerprintLookup, value string) (NodeFingerprintRow, error) {
	predicate := "nf.fingerprint_machine_sha256 = $1"
	switch kind {
	case NodeFingerprintByMachineID:
	case NodeFingerprintByMACs:
		predicate = "nf.fingerprint_macs_sha256 = $1"
	case NodeFingerprintBySeenIP:
		row := q.db.QueryRowContext(ctx, `
			SELECT nf.tenant_id::text, nf.node_id, nf.node_identity_public_key_ed25519
			FROM quartermaster.node_fingerprints nf
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
			JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
			WHERE $1::inet = ANY(nf.seen_ips)
			  AND c.is_active = TRUE
			ORDER BY nf.last_seen DESC, nf.node_id
			LIMIT 1
		`, value)
		var out NodeFingerprintRow
		err := row.Scan(&out.TenantID, &out.NodeID, &out.PublicKeyEd25519)
		return out, err
	default:
		return NodeFingerprintRow{}, fmt.Errorf("unsupported fingerprint lookup %d", kind)
	}
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT nf.tenant_id::text, nf.node_id, nf.node_identity_public_key_ed25519
		FROM quartermaster.node_fingerprints nf
		JOIN quartermaster.infrastructure_nodes n ON n.node_id = nf.node_id
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
		WHERE %s
		  AND c.is_active = TRUE
		ORDER BY nf.node_id
		LIMIT 2
	`, predicate), value)
	if err != nil {
		return NodeFingerprintRow{}, err
	}
	defer rows.Close()

	var out NodeFingerprintRow
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return NodeFingerprintRow{}, err
		}
		return NodeFingerprintRow{}, sql.ErrNoRows
	}
	if err := rows.Scan(&out.TenantID, &out.NodeID, &out.PublicKeyEd25519); err != nil {
		return NodeFingerprintRow{}, err
	}
	if rows.Next() {
		return NodeFingerprintRow{}, fmt.Errorf("%w for %s", ErrAmbiguousNodeFingerprint, value)
	}
	if err := rows.Err(); err != nil {
		return NodeFingerprintRow{}, err
	}
	return out, nil
}

func (q *Queries) BindNodeFingerprintPublicKey(ctx context.Context, nodeID string, publicKey []byte) ([]byte, error) {
	row := q.db.QueryRowContext(ctx, `
		UPDATE quartermaster.node_fingerprints
		SET node_identity_public_key_ed25519 = COALESCE(node_identity_public_key_ed25519, $2),
		    last_seen = NOW()
		WHERE node_id = $1
		RETURNING node_identity_public_key_ed25519
	`, nodeID, publicKey)
	var bound []byte
	err := row.Scan(&bound)
	return bound, err
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
