package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"

	"frameworks/api_tenants/internal/database/quartermasterdb"
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
	queries := quartermasterdb.New(exec)
	lat, lon := geoForNode(opts.GeoIPReader, n.ExternalIP)
	current, err := queries.GetBootstrapNode(ctx, n.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if insertErr := queries.InsertBootstrapNode(ctx, quartermasterdb.InsertBootstrapNodeParams{
			NodeID: n.ID, ClusterID: n.ClusterID, NodeName: n.ID, NodeType: n.Type,
			ExternalIp: n.ExternalIP, WireguardIp: n.WireGuard.IP,
			WireguardPublicKey: n.WireGuard.PublicKey, WireguardListenPort: int32(n.WireGuard.Port),
			Latitude: lat, Longitude: lon,
		}); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	if !sameHostIP(current.ExternalIp, n.ExternalIP) {
		return "", fmt.Errorf("external_ip drift: db=%q desired=%q (stable; refusing rewrite)", current.ExternalIp, n.ExternalIP)
	}
	if !sameHostIP(current.WireguardIp, n.WireGuard.IP) {
		return "", fmt.Errorf("wireguard.ip drift: db=%q desired=%q (stable; refusing rewrite)", current.WireguardIp, n.WireGuard.IP)
	}
	if current.WireguardPublicKey != n.WireGuard.PublicKey {
		return "", fmt.Errorf("wireguard.public_key drift: db=<set> desired=<different> (stable; refusing rewrite)")
	}

	desiredName := n.ID
	clusterMoved := false
	if current.ClusterID != n.ClusterID {
		if !bootstrapOwnsNode(current.EnrollmentOrigin) {
			return "", fmt.Errorf("cluster_id drift: db=%q desired=%q enrollment_origin=%q (only gitops_seed/adopted_local nodes can be moved by bootstrap)", current.ClusterID, n.ClusterID, current.EnrollmentOrigin)
		}
		if err := moveBootstrapOwnedNodeCluster(ctx, queries, n.ID, current.ClusterID, n.ClusterID); err != nil {
			return "", err
		}
		clusterMoved = true
	}

	needsGeoBackfill := lat.Valid && lon.Valid && (!current.Latitude.Valid || !current.Longitude.Valid)
	needsMutableUpdate := current.NodeName != desiredName || current.NodeType != n.Type || current.WireguardListenPort != int32(n.WireGuard.Port) || needsGeoBackfill
	if !needsMutableUpdate && !clusterMoved {
		return "noop", nil
	}
	if !needsMutableUpdate {
		return "updated", nil
	}

	if err := queries.UpdateBootstrapNodeMutableFields(ctx, quartermasterdb.UpdateBootstrapNodeMutableFieldsParams{
		NodeID: n.ID, NodeName: desiredName, NodeType: n.Type,
		WireguardListenPort: int32(n.WireGuard.Port), Latitude: lat, Longitude: lon,
	}); err != nil {
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

func moveBootstrapOwnedNodeCluster(ctx context.Context, queries *quartermasterdb.Queries, nodeID, fromClusterID, toClusterID string) error {
	if err := queries.DeferBootstrapNodeClusterConstraints(ctx); err != nil {
		return fmt.Errorf("defer node cluster FKs: %w", err)
	}

	if err := queries.MoveBootstrapNodeServiceInstances(ctx, quartermasterdb.MoveBootstrapNodeServiceInstancesParams{
		NodeID: nodeID, ToClusterID: toClusterID, FromClusterID: fromClusterID,
	}); err != nil {
		return fmt.Errorf("move service_instances cluster_id: %w", err)
	}

	if err := queries.MoveBootstrapNodeIngressSites(ctx, quartermasterdb.MoveBootstrapNodeIngressSitesParams{
		NodeID: nodeID, ToClusterID: toClusterID, FromClusterID: fromClusterID,
	}); err != nil {
		return fmt.Errorf("move ingress_sites cluster_id: %w", err)
	}

	// Physical TLS bundles are node/FQDN-stable (bundle_id = physical-<fqdn>) but
	// their cluster_id owner moves with the node, mirroring ingress_sites. Without
	// this, the bundle keeps the old cluster while the re-rendered desired state
	// derives the new one under the same stable bundle_id, and ingress reconcile
	// hard-fails on bundle stable-key drift. Match by the node's physical ingress
	// sites (already moved above; the join is by tls_bundle_id, not cluster_id).
	if err := queries.MoveBootstrapNodeTLSBundles(ctx, quartermasterdb.MoveBootstrapNodeTLSBundlesParams{
		NodeID: nodeID, ToClusterID: toClusterID, FromClusterID: fromClusterID,
	}); err != nil {
		return fmt.Errorf("move physical tls_bundles cluster_id: %w", err)
	}

	if err := queries.MoveBootstrapNode(ctx, quartermasterdb.MoveBootstrapNodeParams{
		NodeID: nodeID, ToClusterID: toClusterID, FromClusterID: fromClusterID,
	}); err != nil {
		return fmt.Errorf("move infrastructure_nodes cluster_id: %w", err)
	}

	return nil
}

func geoForNode(reader GeoIPLookup, externalIP string) (sql.NullFloat64, sql.NullFloat64) {
	if reader == nil || externalIP == "" {
		return sql.NullFloat64{}, sql.NullFloat64{}
	}
	geo := reader.Lookup(externalIP)
	if geo == nil {
		return sql.NullFloat64{}, sql.NullFloat64{}
	}
	geobucket.BucketGeoData(geo)
	return sql.NullFloat64{Float64: geo.Latitude, Valid: true}, sql.NullFloat64{Float64: geo.Longitude, Valid: true}
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
