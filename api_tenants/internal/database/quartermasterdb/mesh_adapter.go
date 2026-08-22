package quartermasterdb

import (
	"context"
	"database/sql"

	"github.com/lib/pq"
)

type MeshNodeIdentityRow struct {
	WireguardIP, PublicKey, ExternalIP, InternalIP sql.NullString
	ListenPort                                     sql.NullInt32
	ClusterID                                      string
}

func (q *Queries) GetMeshNodeIdentity(ctx context.Context, nodeID string) (MeshNodeIdentityRow, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT host(wireguard_ip), wireguard_public_key, host(external_ip), host(internal_ip), wireguard_listen_port, cluster_id
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1
	`, nodeID)
	var out MeshNodeIdentityRow
	err := row.Scan(&out.WireguardIP, &out.PublicKey, &out.ExternalIP, &out.InternalIP, &out.ListenPort, &out.ClusterID)
	return out, err
}

type UpdateMeshHeartbeatParams struct {
	NodeID                                                                                 string
	AppliedRevision                                                                        sql.NullString
	SnapshotCPU                                                                            sql.NullFloat64
	SnapshotRamUsed, SnapshotRamTotal, SnapshotDiskUsed, SnapshotDiskTotal, SnapshotUptime sql.NullInt64
	SnapshotAt                                                                             sql.NullTime
}

func (q *Queries) UpdateMeshHeartbeatWithSnapshot(ctx context.Context, arg UpdateMeshHeartbeatParams) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET last_heartbeat = NOW(),
		    applied_mesh_revision = $2,
		    status = 'active',
		    snapshot_cpu_percent = $3,
		    snapshot_ram_used_bytes = $4,
		    snapshot_ram_total_bytes = $5,
		    snapshot_disk_used_bytes = $6,
		    snapshot_disk_total_bytes = $7,
		    snapshot_uptime_seconds = $8,
		    snapshot_at = $9,
		    updated_at = NOW()
		WHERE node_id = $1
	`, arg.NodeID, arg.AppliedRevision, arg.SnapshotCPU, arg.SnapshotRamUsed, arg.SnapshotRamTotal,
		arg.SnapshotDiskUsed, arg.SnapshotDiskTotal, arg.SnapshotUptime, arg.SnapshotAt)
	return err
}

func (q *Queries) UpdateMeshHeartbeat(ctx context.Context, nodeID string, revision sql.NullString) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET last_heartbeat = NOW(),
		    applied_mesh_revision = $2,
		    status = 'active',
		    updated_at = NOW()
		WHERE node_id = $1
	`, nodeID, revision)
	return err
}

func (q *Queries) CurrentMeshTopologyRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE((SELECT revision FROM quartermaster.mesh_topology_state WHERE id = TRUE), 0)
	`).Scan(&revision)
	return revision, err
}

type MeshNodeConfigRow struct {
	ClusterID, MeshRevision, TopologySourceHash, WireguardIP string
	WireguardPort                                            int32
	Peers, ServiceEndpoints                                  []byte
	CurrentTopologyRevision                                  int64
}

func (q *Queries) GetMeshNodeConfig(ctx context.Context, nodeID string) (MeshNodeConfigRow, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT c.cluster_id, c.mesh_revision, c.topology_source_hash, host(c.wireguard_ip),
		       c.wireguard_port, c.peers, c.service_endpoints,
		       COALESCE((SELECT revision FROM quartermaster.mesh_topology_state WHERE id = TRUE), 0)
		FROM quartermaster.mesh_node_configs c
		WHERE c.node_id = $1
	`, nodeID)
	var out MeshNodeConfigRow
	err := row.Scan(&out.ClusterID, &out.MeshRevision, &out.TopologySourceHash, &out.WireguardIP,
		&out.WireguardPort, &out.Peers, &out.ServiceEndpoints, &out.CurrentTopologyRevision)
	return out, err
}

type StoreMeshNodeConfigParams struct {
	NodeID, ClusterID, MeshRevision, TopologySourceHash, WireguardIP string
	WireguardPort                                                    int32
	PeersJSON, ServiceEndpointsJSON                                  string
}

func (q *Queries) StoreMeshNodeConfig(ctx context.Context, arg StoreMeshNodeConfigParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.mesh_node_configs (
			node_id, cluster_id, mesh_revision, topology_source_hash,
			wireguard_ip, wireguard_port, peers, service_endpoints,
			computed_at, updated_at
		)
		VALUES ($1, $2, $3, $4, $5::inet, $6, $7::jsonb, $8::jsonb, NOW(), NOW())
		ON CONFLICT (node_id) DO UPDATE SET
			cluster_id = EXCLUDED.cluster_id, mesh_revision = EXCLUDED.mesh_revision,
			topology_source_hash = EXCLUDED.topology_source_hash, wireguard_ip = EXCLUDED.wireguard_ip,
			wireguard_port = EXCLUDED.wireguard_port, peers = EXCLUDED.peers,
			service_endpoints = EXCLUDED.service_endpoints, computed_at = EXCLUDED.computed_at,
			updated_at = NOW()
	`, arg.NodeID, arg.ClusterID, arg.MeshRevision, arg.TopologySourceHash, arg.WireguardIP,
		arg.WireguardPort, arg.PeersJSON, arg.ServiceEndpointsJSON)
	return err
}

func (q *Queries) ClaimMeshTopologyWarm(ctx context.Context, plannerVersion string) (int64, error) {
	var revision int64
	err := q.db.QueryRowContext(ctx, `
		UPDATE quartermaster.mesh_topology_state
		SET warming_started_at = NOW()
		WHERE id = TRUE
		  AND (revision > warmed_revision OR warmed_planner_version IS DISTINCT FROM $1)
		  AND (warming_started_at IS NULL OR warming_started_at < NOW() - INTERVAL '2 minutes')
		RETURNING revision
	`, plannerVersion).Scan(&revision)
	return revision, err
}

func (q *Queries) CompleteMeshTopologyWarm(ctx context.Context, revision int64, plannerVersion string) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.mesh_topology_state
		SET warmed_revision = GREATEST(warmed_revision, $1),
		    warmed_planner_version = $2, warming_started_at = NULL, updated_at = NOW()
		WHERE id = TRUE
	`, revision, plannerVersion)
	return err
}

func (q *Queries) ReleaseMeshTopologyWarm(ctx context.Context) error {
	_, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.mesh_topology_state
		SET warming_started_at = NULL, updated_at = NOW()
		WHERE id = TRUE
	`)
	return err
}

type ActiveMeshNodeRow struct {
	NodeID, ClusterID, WireguardIP string
	WireguardPort                  int32
}

func (q *Queries) ListActiveMeshNodes(ctx context.Context) ([]ActiveMeshNodeRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT node_id, cluster_id, host(wireguard_ip), wireguard_listen_port
		FROM quartermaster.infrastructure_nodes
		WHERE status = 'active' AND wireguard_ip IS NOT NULL
		  AND wireguard_public_key IS NOT NULL AND wireguard_listen_port IS NOT NULL
		ORDER BY node_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ActiveMeshNodeRow{}
	for rows.Next() {
		var row ActiveMeshNodeRow
		if err := rows.Scan(&row.NodeID, &row.ClusterID, &row.WireguardIP, &row.WireguardPort); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type MeshPeerCandidateRow struct {
	NodeName, PublicKey                 string
	ExternalIP, InternalIP, WireguardIP sql.NullString
	ListenPort                          sql.NullInt32
	ScanErr                             error
}

type ListMeshPeerCandidatesParams struct {
	NodeID, ClusterID string
	RequiredNodeIDs   []string
}

// ListLocalMeshServiceTypes returns the effective service set used by the
// topology planner. Desired services are included so a node can discover its
// dependencies before those services have started.
func (q *Queries) ListLocalMeshServiceTypes(ctx context.Context, nodeID string) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT DISTINCT service_type
		FROM (
			SELECT s.type AS service_type
			FROM quartermaster.service_instances si
			JOIN quartermaster.services s ON s.service_id = si.service_id
			WHERE si.node_id = $1 AND si.status IN ('running', 'active') AND s.type IS NOT NULL AND s.type <> ''
			UNION ALL
			SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'desired_service_types') = 'array' THEN n.metadata->'desired_service_types' ELSE '[]'::jsonb END)
			FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $1
			UNION ALL
			SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'service_types') = 'array' THEN n.metadata->'service_types' ELSE '[]'::jsonb END)
			FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $1
		) local_services
		WHERE service_type IS NOT NULL AND service_type <> ''
	`, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var serviceType string
		if err := rows.Scan(&serviceType); err != nil {
			return nil, err
		}
		result = append(result, serviceType)
	}
	return result, rows.Err()
}

type MeshServiceEndpointParams struct {
	ClusterID, NodeID          string
	PeerTypes, GlobalPeerTypes []string
}

type MeshServiceEndpointRow struct {
	ServiceType, NodeID, WireguardIP string
}

func (q *Queries) ListMeshServiceEndpoints(ctx context.Context, arg MeshServiceEndpointParams) ([]MeshServiceEndpointRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		WITH request_contexts AS (
			SELECT $1::text AS cluster_id WHERE $1 <> ''
			UNION SELECT si.cluster_id FROM quartermaster.service_instances si WHERE si.node_id = $2 AND si.status IN ('running', 'active') AND si.cluster_id IS NOT NULL AND si.cluster_id <> ''
			UNION SELECT sca.cluster_id FROM quartermaster.service_instances si JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true WHERE si.node_id = $2 AND si.status IN ('running', 'active') AND sca.cluster_id IS NOT NULL AND sca.cluster_id <> ''
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
		), eligible AS (
			SELECT s.type, si.node_id, host(n.wireguard_ip) AS wireguard_ip,
			       COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id) AS provider_cluster
			FROM quartermaster.services s
			JOIN quartermaster.service_instances si ON si.service_id = s.service_id
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
			LEFT JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE si.status IN ('running', 'active') AND n.wireguard_ip IS NOT NULL AND n.status = 'active'
			  AND (s.type = ANY($3::text[]) OR s.type = ANY($4::text[]))
		), service_scope AS (
			SELECT type, COUNT(DISTINCT provider_cluster) AS provider_cluster_count FROM eligible
			WHERE provider_cluster IS NOT NULL AND provider_cluster <> '' GROUP BY type
		)
		SELECT DISTINCT e.type, e.node_id, e.wireguard_ip
		FROM eligible e JOIN service_scope ss ON ss.type = e.type
		WHERE e.type = ANY($4::text[]) OR e.provider_cluster IN (SELECT cluster_id FROM request_contexts) OR ss.provider_cluster_count = 1
	`, arg.ClusterID, arg.NodeID, pq.Array(arg.PeerTypes), pq.Array(arg.GlobalPeerTypes))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MeshServiceEndpointRow
	for rows.Next() {
		var row MeshServiceEndpointRow
		if err := rows.Scan(&row.ServiceType, &row.NodeID, &row.WireguardIP); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

type InfraMeshPeerParams struct {
	ClusterID, NodeID       string
	Kinds, Providers, Names []string
}

func (q *Queries) ListInfraMeshPeerNodeIDs(ctx context.Context, arg InfraMeshPeerParams) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		WITH dependency_input AS (
			SELECT kind, provider, name FROM unnest($3::text[], $4::text[], $5::text[]) AS t(kind, provider, name)
		), request_contexts AS (
			SELECT $1::text AS cluster_id WHERE $1 <> ''
			UNION SELECT si.cluster_id FROM quartermaster.service_instances si WHERE si.node_id = $2 AND si.status IN ('running', 'active') AND si.cluster_id IS NOT NULL AND si.cluster_id <> ''
			UNION SELECT sca.cluster_id FROM quartermaster.service_instances si JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true WHERE si.node_id = $2 AND si.status IN ('running', 'active') AND sca.cluster_id IS NOT NULL AND sca.cluster_id <> ''
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
			UNION SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n WHERE n.node_id = $2
		), eligible AS (
			SELECT di.kind, di.provider, di.name, si.node_id,
			       COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id) AS provider_cluster,
			       COALESCE(si.metadata->>'infra_role', '') AS infra_role,
			       COALESCE(si.metadata->>'infra_name', '') AS infra_name
			FROM dependency_input di
			JOIN quartermaster.services svc ON svc.type = di.kind AND svc.plane = 'infra'
			JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
			LEFT JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE si.status IN ('running', 'active') AND n.wireguard_ip IS NOT NULL AND n.status = 'active'
		), service_scope AS (
			SELECT kind, COUNT(DISTINCT provider_cluster) AS provider_cluster_count FROM eligible
			WHERE provider_cluster IS NOT NULL AND provider_cluster <> '' GROUP BY kind
		)
		SELECT DISTINCT e.node_id FROM eligible e JOIN service_scope ss ON ss.kind = e.kind
		WHERE (e.provider = 'primary' AND e.infra_role = 'primary')
		   OR (e.provider = 'aggregator' AND e.infra_role = 'aggregator')
		   OR (e.provider = 'named' AND e.infra_name = e.name AND (e.provider_cluster IN (SELECT cluster_id FROM request_contexts) OR ss.provider_cluster_count = 1))
		   OR (e.provider = 'regional' AND e.infra_role = 'regional' AND (e.provider_cluster IN (SELECT cluster_id FROM request_contexts) OR ss.provider_cluster_count = 1))
	`, arg.ClusterID, arg.NodeID, pq.Array(arg.Kinds), pq.Array(arg.Providers), pq.Array(arg.Names))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			result = append(result, id)
		}
	}
	return result, rows.Err()
}

type ReciprocalMeshPeerParams struct {
	NodeID, ClusterID             string
	ProvidedTypes, DependentTypes []string
	GlobalFlags                   []bool
}

func (q *Queries) ListReciprocalMeshPeerNodeIDs(ctx context.Context, arg ReciprocalMeshPeerParams) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `
		WITH dependency_input AS (
			SELECT provided_type, dependent_type, is_global
			FROM unnest($3::text[], $4::text[], $5::boolean[]) AS t(provided_type, dependent_type, is_global)
		), provided AS (
			SELECT DISTINCT di.provided_type, COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id, $2) AS provider_cluster
			FROM dependency_input di JOIN quartermaster.services svc ON svc.type = di.provided_type
			JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
			LEFT JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE si.node_id = $1 AND si.status IN ('running', 'active')
			UNION SELECT DISTINCT di.provided_type, $2::text AS provider_cluster FROM dependency_input di
		), service_scope AS (
			SELECT di.provided_type, COUNT(DISTINCT COALESCE(NULLIF(sca.cluster_id, ''), NULLIF(si.cluster_id, ''), n.cluster_id)) AS provider_cluster_count
			FROM dependency_input di JOIN quartermaster.services svc ON svc.type = di.provided_type
			JOIN quartermaster.service_instances si ON si.service_id = svc.service_id
			JOIN quartermaster.infrastructure_nodes n ON n.node_id = si.node_id
			LEFT JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true
			WHERE si.status IN ('running', 'active') AND n.wireguard_ip IS NOT NULL AND n.status = 'active' GROUP BY di.provided_type
		), node_services AS (
			SELECT si.node_id, svc.type AS service_type FROM quartermaster.service_instances si JOIN quartermaster.services svc ON svc.service_id = si.service_id
			WHERE si.status IN ('running', 'active') AND svc.type IS NOT NULL AND svc.type <> ''
			UNION SELECT n.node_id, jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'desired_service_types') = 'array' THEN n.metadata->'desired_service_types' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n
			UNION SELECT n.node_id, jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'service_types') = 'array' THEN n.metadata->'service_types' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n
		), consumer_contexts AS (
			SELECT node_id, cluster_id FROM quartermaster.infrastructure_nodes WHERE cluster_id IS NOT NULL AND cluster_id <> ''
			UNION SELECT si.node_id, si.cluster_id FROM quartermaster.service_instances si WHERE si.status IN ('running', 'active') AND si.cluster_id IS NOT NULL AND si.cluster_id <> ''
			UNION SELECT si.node_id, sca.cluster_id FROM quartermaster.service_instances si JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id AND sca.is_active = true WHERE si.status IN ('running', 'active') AND sca.cluster_id IS NOT NULL AND sca.cluster_id <> ''
			UNION SELECT n.node_id, jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'desired_cluster_ids') = 'array' THEN n.metadata->'desired_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n
			UNION SELECT n.node_id, jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'service_cluster_ids') = 'array' THEN n.metadata->'service_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n
			UNION SELECT n.node_id, jsonb_array_elements_text(CASE WHEN jsonb_typeof(n.metadata->'logical_cluster_ids') = 'array' THEN n.metadata->'logical_cluster_ids' ELSE '[]'::jsonb END) FROM quartermaster.infrastructure_nodes n
		)
		SELECT DISTINCT n.node_id FROM quartermaster.infrastructure_nodes n
		JOIN node_services ns ON ns.node_id = n.node_id JOIN dependency_input di ON di.dependent_type = ns.service_type
		LEFT JOIN service_scope ss ON ss.provided_type = di.provided_type
		WHERE n.node_id <> $1 AND n.wireguard_public_key IS NOT NULL AND n.wireguard_ip IS NOT NULL AND n.status = 'active'
		  AND (di.is_global OR COALESCE(ss.provider_cluster_count, 0) <= 1 OR EXISTS (
			SELECT 1 FROM provided p JOIN consumer_contexts cc ON cc.node_id = n.node_id AND cc.cluster_id = p.provider_cluster
			WHERE p.provided_type = di.provided_type
		  ))
	`, arg.NodeID, arg.ClusterID, pq.Array(arg.ProvidedTypes), pq.Array(arg.DependentTypes), pq.Array(arg.GlobalFlags))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if id != "" {
			result = append(result, id)
		}
	}
	return result, rows.Err()
}

func (q *Queries) ListMeshPeerCandidates(ctx context.Context, arg ListMeshPeerCandidatesParams) ([]MeshPeerCandidateRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT n.node_name, n.wireguard_public_key, host(n.external_ip), host(n.internal_ip), host(n.wireguard_ip), n.wireguard_listen_port
		FROM quartermaster.infrastructure_nodes n
		WHERE n.node_id != $1
		  AND (n.cluster_id = $2 OR n.node_id = ANY($3))
		  AND n.wireguard_public_key IS NOT NULL AND n.wireguard_ip IS NOT NULL AND n.status = 'active'
	`, arg.NodeID, arg.ClusterID, pq.Array(arg.RequiredNodeIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []MeshPeerCandidateRow{}
	for rows.Next() {
		var row MeshPeerCandidateRow
		if err := rows.Scan(&row.NodeName, &row.PublicKey, &row.ExternalIP, &row.InternalIP, &row.WireguardIP, &row.ListenPort); err != nil {
			row.ScanErr = err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
