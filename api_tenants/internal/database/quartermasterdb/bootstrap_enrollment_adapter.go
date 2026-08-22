package quartermasterdb

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

type LockedEnrollmentTokenRow struct {
	ID                              string
	TenantID, ClusterID, ExpectedIP sql.NullString
	UsageLimit                      sql.NullInt32
	UsageCount                      int32
	ExpiresAt                       time.Time
	Metadata                        string
}

func (q *Queries) LockEdgeEnrollmentToken(ctx context.Context, tokenHash string) (LockedEnrollmentTokenRow, error) {
	var out LockedEnrollmentTokenRow
	err := q.db.QueryRowContext(ctx, `
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'edge_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, tokenHash).Scan(&out.ID, &out.TenantID, &out.ClusterID, &out.UsageLimit, &out.UsageCount, &out.ExpiresAt, &out.ExpectedIP)
	return out, err
}

func (q *Queries) LockInfrastructureEnrollmentToken(ctx context.Context, tokenHash string) (LockedEnrollmentTokenRow, error) {
	var out LockedEnrollmentTokenRow
	err := q.db.QueryRowContext(ctx, `
		SELECT id, tenant_id::text, COALESCE(cluster_id, ''), usage_limit, usage_count, expires_at, expected_ip::text, COALESCE(metadata::text, '{}')
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1 AND kind = 'infrastructure_node'
		  AND (
		    (usage_limit IS NULL AND used_at IS NULL) OR
		    (usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
		FOR UPDATE
	`, tokenHash).Scan(&out.ID, &out.TenantID, &out.ClusterID, &out.UsageLimit, &out.UsageCount, &out.ExpiresAt, &out.ExpectedIP, &out.Metadata)
	return out, err
}

func (q *Queries) FirstActiveCluster(ctx context.Context) (string, error) {
	var clusterID string
	err := q.db.QueryRowContext(ctx, `
		SELECT cluster_id FROM quartermaster.infrastructure_clusters
		WHERE is_active = true
		ORDER BY cluster_name LIMIT 1
	`).Scan(&clusterID)
	return clusterID, err
}

func (q *Queries) GetNodeCluster(ctx context.Context, nodeID string) (string, error) {
	var clusterID string
	err := q.db.QueryRowContext(ctx, `SELECT cluster_id FROM quartermaster.infrastructure_nodes WHERE node_id = $1`, nodeID).Scan(&clusterID)
	return clusterID, err
}

type CreateEdgeNodeParams struct {
	ID, NodeID, ClusterID, Hostname string
	ExternalIP, Latitude, Longitude any
}

func (q *Queries) CreateEdgeNode(ctx context.Context, arg CreateEdgeNodeParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, latitude, longitude, tags, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'edge', $5::inet, $6, $7, '{}', '{}', NOW(), NOW())
	`, arg.ID, arg.NodeID, arg.ClusterID, arg.Hostname, arg.ExternalIP, arg.Latitude, arg.Longitude)
	return err
}

func (q *Queries) IncrementBootstrapTokenUsage(ctx context.Context, tokenID string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.bootstrap_tokens SET usage_count = usage_count + 1, used_at = NOW() WHERE id = $1
	`, tokenID)
	return err
}

type UpsertEdgeNodeFingerprintParams struct {
	TenantID, NodeID, MachineIDSHA, MACsSHA, AttrsJSON string
	IPs                                                []string
}

func (q *Queries) UpsertEdgeNodeFingerprint(ctx context.Context, arg UpsertEdgeNodeFingerprintParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.node_fingerprints (tenant_id, node_id, fingerprint_machine_sha256, fingerprint_macs_sha256, seen_ips, attrs)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), $5::inet[], $6)
		ON CONFLICT (node_id) DO UPDATE SET
			tenant_id = EXCLUDED.tenant_id,
			fingerprint_machine_sha256 = COALESCE(EXCLUDED.fingerprint_machine_sha256, quartermaster.node_fingerprints.fingerprint_machine_sha256),
			fingerprint_macs_sha256 = COALESCE(EXCLUDED.fingerprint_macs_sha256, quartermaster.node_fingerprints.fingerprint_macs_sha256),
			attrs = CASE WHEN EXCLUDED.attrs IS NULL OR EXCLUDED.attrs = '{}'::jsonb THEN quartermaster.node_fingerprints.attrs ELSE EXCLUDED.attrs END,
			last_seen = NOW(), seen_ips = quartermaster.node_fingerprints.seen_ips || EXCLUDED.seen_ips
	`, arg.TenantID, arg.NodeID, arg.MachineIDSHA, arg.MACsSHA, pq.Array(arg.IPs), arg.AttrsJSON)
	return err
}

type ExistingInfrastructureNodeRow struct {
	ClusterID     string
	WireguardIP   sql.NullString
	WireguardPort sql.NullInt32
}

func (q *Queries) GetExistingInfrastructureNode(ctx context.Context, nodeID string) (ExistingInfrastructureNodeRow, error) {
	var out ExistingInfrastructureNodeRow
	err := q.db.QueryRowContext(ctx, `
		SELECT cluster_id, host(wireguard_ip), wireguard_listen_port
		FROM quartermaster.infrastructure_nodes WHERE node_id = $1
	`, nodeID).Scan(&out.ClusterID, &out.WireguardIP, &out.WireguardPort)
	return out, err
}

func (q *Queries) MergeInfrastructureNodeMetadata(ctx context.Context, nodeID, metadata string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET metadata = COALESCE(metadata, '{}'::jsonb) || $2::jsonb, updated_at = NOW()
		WHERE node_id = $1
	`, nodeID, metadata)
	return err
}

type CreateInfrastructureNodeParams struct {
	ID, NodeID, ClusterID, Hostname, NodeType string
	ExternalIP, InternalIP                    any
	WireguardIP, WireguardPublicKey           string
	WireguardPort                             int32
	Latitude, Longitude                       any
	Metadata                                  string
}

func (q *Queries) CreateInfrastructureNode(ctx context.Context, arg CreateInfrastructureNodeParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.infrastructure_nodes (id, node_id, cluster_id, node_name, node_type, external_ip, internal_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port, enrollment_origin, latitude, longitude, tags, metadata, last_heartbeat, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::inet, $7::inet, $8::inet, $9, $10, 'runtime_enrolled', $11, $12, '{}', $13::jsonb, NOW(), NOW(), NOW())
	`, arg.ID, arg.NodeID, arg.ClusterID, arg.Hostname, arg.NodeType, arg.ExternalIP, arg.InternalIP,
		arg.WireguardIP, arg.WireguardPublicKey, arg.WireguardPort, arg.Latitude, arg.Longitude, arg.Metadata)
	return err
}

func (q *Queries) GetNodeEnrollmentOrigin(ctx context.Context, nodeID string) (string, error) {
	var origin string
	err := q.db.QueryRowContext(ctx, `SELECT enrollment_origin FROM quartermaster.infrastructure_nodes WHERE node_id = $1`, nodeID).Scan(&origin)
	return origin, err
}
func (q *Queries) UpdateNodeEnrollmentOrigin(ctx context.Context, nodeID, origin string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes SET enrollment_origin = $1, updated_at = NOW() WHERE node_id = $2
	`, origin, nodeID)
	return err
}

type BootstrapReplayTokenRow struct {
	ClusterID, ExpectedIP sql.NullString
	ExpiresAt             time.Time
}

func (q *Queries) GetBootstrapReplayToken(ctx context.Context, tokenHash string) (BootstrapReplayTokenRow, error) {
	var out BootstrapReplayTokenRow
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(cluster_id, ''), expires_at, expected_ip::text
		FROM quartermaster.bootstrap_tokens WHERE token_hash = $1 AND kind = 'infrastructure_node'
	`, tokenHash).Scan(&out.ClusterID, &out.ExpiresAt, &out.ExpectedIP)
	return out, err
}

type BootstrapReplayNodeRow struct {
	ClusterID, PublicKey, WireguardIP sql.NullString
	WireguardPort                     sql.NullInt32
	TenantID                          sql.NullString
}

func (q *Queries) GetBootstrapReplayNode(ctx context.Context, nodeID string) (BootstrapReplayNodeRow, error) {
	var out BootstrapReplayNodeRow
	err := q.db.QueryRowContext(ctx, `
		SELECT n.cluster_id, n.wireguard_public_key, host(n.wireguard_ip), n.wireguard_listen_port, c.owner_tenant_id::text
		FROM quartermaster.infrastructure_nodes n
		JOIN quartermaster.infrastructure_clusters c ON c.cluster_id = n.cluster_id
		WHERE n.node_id = $1
	`, nodeID).Scan(&out.ClusterID, &out.PublicKey, &out.WireguardIP, &out.WireguardPort, &out.TenantID)
	return out, err
}
