package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"

	geobucket "frameworks/api_tenants/internal/geo"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
)

// GeoIPLookup is the small subset of the shared GeoIP reader used by bootstrap.
type GeoIPLookup interface {
	Lookup(ipStr string) *geoip.GeoData
}

type NodeOptions struct {
	GeoIPReader GeoIPLookup
}

// ReconcileNodes reconciles every Node row into
// quartermaster.infrastructure_nodes. Stable keys (fail loud on drift):
//
//   - node_id (cluster row pinned by node_id, since node_id is the table's
//     UNIQUE constraint),
//   - external_ip,
//   - wireguard.ip,
//   - wireguard.public_key.
//
// Mutable for GitOps-owned rows: cluster_id, node_name, node_type,
// wireguard.listen_port. Heartbeats / runtime status / mesh-revision columns
// are owned by the running node, not bootstrap; this reconciler must not touch
// them.
//
// enrollment_origin is set to "gitops_seed" on insert (cluster.yaml is the
// declarative source); existing rows keep whatever origin runtime enrollment
// stamped them with.
func ReconcileNodes(ctx context.Context, exec DBTX, nodes []Node) (Result, error) {
	return ReconcileNodesWithOptions(ctx, exec, nodes, NodeOptions{})
}

func ReconcileNodesWithOptions(ctx context.Context, exec DBTX, nodes []Node, opts NodeOptions) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileNodes: nil executor")
	}

	res := Result{}
	for _, n := range nodes {
		if err := validateNode(n); err != nil {
			return Result{}, err
		}
		action, err := upsertNode(ctx, exec, n, opts)
		if err != nil {
			return Result{}, fmt.Errorf("node %q: %w", n.ID, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, n.ID)
		case "updated":
			res.Updated = append(res.Updated, n.ID)
		case "noop":
			res.Noop = append(res.Noop, n.ID)
		}
	}

	return res, nil
}

func validateNode(n Node) error {
	if n.ID == "" {
		return errors.New("node id required")
	}
	if n.ClusterID == "" {
		return fmt.Errorf("node %q: cluster_id required", n.ID)
	}
	switch n.Type {
	case "core", "edge":
	default:
		return fmt.Errorf("node %q: type must be \"core\" or \"edge\" (got %q)", n.ID, n.Type)
	}
	if n.ExternalIP == "" {
		return fmt.Errorf("node %q: external_ip required", n.ID)
	}
	if n.WireGuard.IP == "" {
		return fmt.Errorf("node %q: wireguard.ip required", n.ID)
	}
	if n.WireGuard.PublicKey == "" {
		return fmt.Errorf("node %q: wireguard.public_key required", n.ID)
	}
	return nil
}

func upsertNode(ctx context.Context, exec DBTX, n Node, opts NodeOptions) (string, error) {
	const probeSQL = `
		SELECT
			node_name, node_type, cluster_id,
			COALESCE(host(external_ip), ''),
			COALESCE(host(wireguard_ip), ''),
			COALESCE(wireguard_public_key, ''),
			COALESCE(wireguard_listen_port, 0),
			enrollment_origin,
			latitude, longitude
		FROM quartermaster.infrastructure_nodes
		WHERE node_id = $1`
	var (
		curName, curType, curCluster, curExternal, curWGIP, curPubKey string
		curEnrollmentOrigin                                           string
		curWGPort                                                     int
		curLat, curLon                                                sql.NullFloat64
	)
	lat, lon := geoForNode(opts.GeoIPReader, n.ExternalIP)
	err := exec.QueryRowContext(ctx, probeSQL, n.ID).Scan(
		&curName, &curType, &curCluster, &curExternal, &curWGIP, &curPubKey, &curWGPort,
		&curEnrollmentOrigin, &curLat, &curLon,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO quartermaster.infrastructure_nodes (
				node_id, cluster_id, node_name, node_type,
				external_ip, wireguard_ip, wireguard_public_key, wireguard_listen_port,
				latitude, longitude,
				enrollment_origin, status,
				created_at, updated_at
			) VALUES (
				$1, $2, $3, $4,
				NULLIF($5, '')::inet, NULLIF($6, '')::inet, $7, NULLIF($8, 0),
				$9, $10,
				'gitops_seed', 'offline',
				NOW(), NOW()
			)`
		nodeName := n.ID
		if _, insertErr := exec.ExecContext(ctx, insertSQL,
			n.ID, n.ClusterID, nodeName, n.Type,
			n.ExternalIP, n.WireGuard.IP, n.WireGuard.PublicKey, n.WireGuard.Port, lat, lon,
		); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if !sameHostIP(curExternal, n.ExternalIP) {
		return "", fmt.Errorf("external_ip drift: db=%q desired=%q (stable; refusing rewrite)", curExternal, n.ExternalIP)
	}
	if !sameHostIP(curWGIP, n.WireGuard.IP) {
		return "", fmt.Errorf("wireguard.ip drift: db=%q desired=%q (stable; refusing rewrite)", curWGIP, n.WireGuard.IP)
	}
	if curPubKey != n.WireGuard.PublicKey {
		return "", fmt.Errorf("wireguard.public_key drift: db=<set> desired=<different> (stable; refusing rewrite)")
	}

	desiredName := n.ID
	clusterMoved := false
	if curCluster != n.ClusterID {
		if !bootstrapOwnsNode(curEnrollmentOrigin) {
			return "", fmt.Errorf("cluster_id drift: db=%q desired=%q enrollment_origin=%q (only gitops_seed/adopted_local nodes can be moved by bootstrap)", curCluster, n.ClusterID, curEnrollmentOrigin)
		}
		if err := moveBootstrapOwnedNodeCluster(ctx, exec, n.ID, curCluster, n.ClusterID); err != nil {
			return "", err
		}
		clusterMoved = true
	}

	needsGeoBackfill := lat != nil && lon != nil && (!curLat.Valid || !curLon.Valid)
	needsMutableUpdate := curName != desiredName || curType != n.Type || curWGPort != n.WireGuard.Port || needsGeoBackfill
	if !needsMutableUpdate && !clusterMoved {
		return "noop", nil
	}
	if !needsMutableUpdate {
		return "updated", nil
	}

	const updateSQL = `
		UPDATE quartermaster.infrastructure_nodes
		SET node_name = $2,
		    node_type = $3,
		    wireguard_listen_port = NULLIF($4, 0),
		    latitude = COALESCE(latitude, $5),
		    longitude = COALESCE(longitude, $6),
		    updated_at = NOW()
		WHERE node_id = $1`
	if _, err := exec.ExecContext(ctx, updateSQL, n.ID, desiredName, n.Type, n.WireGuard.Port, lat, lon); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return "updated", nil
}

func bootstrapOwnsNode(enrollmentOrigin string) bool {
	switch enrollmentOrigin {
	case "gitops_seed", "adopted_local":
		return true
	default:
		return false
	}
}

func moveBootstrapOwnedNodeCluster(ctx context.Context, exec DBTX, nodeID, fromClusterID, toClusterID string) error {
	if _, err := exec.ExecContext(ctx, `SET CONSTRAINTS fk_qm_service_instances_node_cluster, fk_qm_ingress_sites_node_cluster DEFERRED`); err != nil {
		return fmt.Errorf("defer node cluster FKs: %w", err)
	}

	if _, err := exec.ExecContext(ctx, `
		UPDATE quartermaster.service_instances
		SET cluster_id = $2,
		    updated_at = NOW()
		WHERE node_id = $1
		  AND cluster_id = $3`, nodeID, toClusterID, fromClusterID); err != nil {
		return fmt.Errorf("move service_instances cluster_id: %w", err)
	}

	if _, err := exec.ExecContext(ctx, `
		UPDATE quartermaster.ingress_sites
		SET cluster_id = $2,
		    updated_at = NOW()
		WHERE node_id = $1
		  AND cluster_id = $3`, nodeID, toClusterID, fromClusterID); err != nil {
		return fmt.Errorf("move ingress_sites cluster_id: %w", err)
	}

	// Physical TLS bundles are node/FQDN-stable (bundle_id = physical-<fqdn>) but
	// their cluster_id owner moves with the node, mirroring ingress_sites. Without
	// this, the bundle keeps the old cluster while the re-rendered desired state
	// derives the new one under the same stable bundle_id, and ingress reconcile
	// hard-fails on bundle stable-key drift. Match by the node's physical ingress
	// sites (already moved above; the join is by tls_bundle_id, not cluster_id).
	if _, err := exec.ExecContext(ctx, `
		UPDATE quartermaster.tls_bundles
		SET cluster_id = $2,
		    updated_at = NOW()
		WHERE cluster_id = $3
		  AND bundle_id IN (
			SELECT tls_bundle_id FROM quartermaster.ingress_sites
			WHERE node_id = $1 AND kind = 'physical'
		  )`, nodeID, toClusterID, fromClusterID); err != nil {
		return fmt.Errorf("move physical tls_bundles cluster_id: %w", err)
	}

	if _, err := exec.ExecContext(ctx, `
		UPDATE quartermaster.infrastructure_nodes
		SET cluster_id = $2,
		    updated_at = NOW()
		WHERE node_id = $1
		  AND cluster_id = $3`, nodeID, toClusterID, fromClusterID); err != nil {
		return fmt.Errorf("move infrastructure_nodes cluster_id: %w", err)
	}

	return nil
}

func geoForNode(reader GeoIPLookup, externalIP string) (any, any) {
	if reader == nil || externalIP == "" {
		return nil, nil
	}
	geo := reader.Lookup(externalIP)
	if geo == nil {
		return nil, nil
	}
	geobucket.BucketGeoData(geo)
	return geo.Latitude, geo.Longitude
}

func sameHostIP(a, b string) bool {
	na, okA := normalizeHostIP(a)
	nb, okB := normalizeHostIP(b)
	if !okA || !okB {
		return a == b
	}
	return na == nb
}

func normalizeHostIP(s string) (string, bool) {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.String(), true
	}
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return "", false
	}
	if prefix.Bits() != prefix.Addr().BitLen() {
		return prefix.String(), true
	}
	return prefix.Addr().String(), true
}
