package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_tenants/internal/database/quartermasterdb"
)

// ReconcileClusters reconciles every Cluster row into
// quartermaster.infrastructure_clusters and applies the cluster's mesh CIDR /
// listen port to the same row's wg_* columns. Stable keys: cluster_id,
// owner_tenant_id, wg_mesh_cidr (when an existing row already carries one).
//
// Default-cluster atomicity: at most one cluster may be is_default = true. The
// clear-then-set transition for that bit happens within the caller's outer
// transaction, eliminating the window in which a concurrent reconcile would
// leave zero clusters marked default (the gRPC handler at server.go:2690 /
// :2828 splits this across statements; here it is one tx).
//
// Drift policy: stable keys fail loud; mutable fields update.
func ReconcileClusters(ctx context.Context, exec DBTX, clusters []Cluster, aliases *AliasMap) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileClusters: nil executor")
	}
	if aliases == nil {
		return Result{}, errors.New("ReconcileClusters: nil alias map")
	}

	res := Result{}
	defaultsRequested := 0
	for _, c := range clusters {
		if c.IsDefault {
			defaultsRequested++
		}
	}
	if defaultsRequested > 1 {
		return Result{}, fmt.Errorf("ReconcileClusters: %d clusters marked is_default; at most one allowed", defaultsRequested)
	}

	if defaultsRequested == 1 {
		// Clear any other row's default flag inside the same tx so the post-commit
		// state has exactly one default. The matching SET happens in upsertCluster.
		if err := quartermasterdb.New(exec).ClearBootstrapDefaultCluster(ctx); err != nil {
			return Result{}, fmt.Errorf("clear default cluster: %w", err)
		}
	}

	for _, c := range clusters {
		if err := validateCluster(c); err != nil {
			return Result{}, err
		}
		ownerID, err := aliases.LookupRef(c.OwnerTenant.Ref)
		if err != nil {
			return Result{}, fmt.Errorf("cluster %q: %w", c.ID, err)
		}
		action, err := upsertCluster(ctx, exec, c, ownerID)
		if err != nil {
			return Result{}, fmt.Errorf("cluster %q: %w", c.ID, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, c.ID)
		case "updated":
			res.Updated = append(res.Updated, c.ID)
		case "noop":
			res.Noop = append(res.Noop, c.ID)
		}
	}

	return res, nil
}

func validateCluster(c Cluster) error {
	if c.ID == "" {
		return errors.New("cluster id required")
	}
	if c.Name == "" {
		return fmt.Errorf("cluster %q: name required", c.ID)
	}
	switch c.Type {
	case "central", "edge":
	default:
		return fmt.Errorf("cluster %q: type must be \"central\" or \"edge\" (got %q)", c.ID, c.Type)
	}
	if c.OwnerTenant.Ref == "" {
		return fmt.Errorf("cluster %q: owner_tenant.ref required", c.ID)
	}
	if c.Mesh.CIDR == "" {
		return fmt.Errorf("cluster %q: mesh.cidr required", c.ID)
	}
	return nil
}

// upsertCluster inserts or reconciles a single cluster row. Returns "created",
// "updated", or "noop".
func upsertCluster(ctx context.Context, exec DBTX, c Cluster, ownerID string) (string, error) {
	queries := quartermasterdb.New(exec)
	current, probeErr := queries.GetBootstrapCluster(ctx, c.ID)
	switch {
	case errors.Is(probeErr, sql.ErrNoRows):
		if insertErr := queries.InsertBootstrapCluster(ctx, quartermasterdb.InsertBootstrapClusterParams{
			ClusterID: c.ID, ClusterName: c.Name, ClusterType: c.Type,
			OwnerTenantID: ownerID, BaseUrl: c.BaseURL,
			WgMeshCidr: c.Mesh.CIDR, WgListenPort: int32(c.Mesh.ListenPort),
			IsDefaultCluster: c.IsDefault, IsPlatformOfficial: c.IsPlatformOfficial,
			PublicTopology: c.PublicTopology, AllowPrivatePullSources: c.AllowPrivatePullSources,
			RegionID: c.Region, CellID: c.Cell, ClusterClass: c.Class,
			ControlCellID: c.ControlCell, EligibleServingCellIds: c.EligibleServingCells,
			S3Bucket: c.S3Bucket, S3Endpoint: c.S3Endpoint, S3Region: c.S3Region, S3Prefix: c.S3Prefix,
		}); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case probeErr != nil:
		return "", fmt.Errorf("probe: %w", probeErr)
	}

	// Stable-key drift: cluster_type, owner_tenant_id, an existing
	// wg_mesh_cidr, region_id, and cell_id must not change once set. These
	// are facts the rest of the platform indexes against; reassigning them
	// silently corrupts mesh allocations, geo routing, and apply-state ACK.
	if current.ClusterType != c.Type {
		return "", fmt.Errorf("type drift: db=%q desired=%q (cluster_type is stable; refusing rewrite)", current.ClusterType, c.Type)
	}
	if current.OwnerTenantID != ownerID {
		return "", fmt.Errorf("owner drift: db=%q desired=%q (owner_tenant_id is stable; refusing rewrite)", current.OwnerTenantID, ownerID)
	}
	if current.WgMeshCidr != "" && current.WgMeshCidr != c.Mesh.CIDR {
		return "", fmt.Errorf("mesh.cidr drift: db=%q desired=%q (cidr is stable once set; refusing rewrite)", current.WgMeshCidr, c.Mesh.CIDR)
	}
	if current.RegionID != "" && current.RegionID != c.Region {
		return "", fmt.Errorf("region drift: db=%q desired=%q (region_id is stable once set; refusing rewrite)", current.RegionID, c.Region)
	}
	if current.CellID != "" && current.CellID != c.Cell {
		return "", fmt.Errorf("cell drift: db=%q desired=%q (cell_id is stable once set; refusing rewrite)", current.CellID, c.Cell)
	}
	// S3 backend descriptor is IMMUTABLE once established: repointing a cluster's bucket/endpoint/region would misroute
	// cleanup and serving of historical bytes. Chandler reads THIS row for its effective descriptor and Foghorn
	// enforces the same invariant locally (cell_storage_identity). Once the descriptor is established (bucket set),
	// bucket/endpoint/region are frozen. Region compares on its EFFECTIVE value (empty→us-east-1) — the same
	// normalization every reader applies — so an omitted region does not
	// false-drift against a us-east-1 one; bucket/endpoint compare exactly.
	effCurS3Region := current.S3Region
	if effCurS3Region == "" {
		effCurS3Region = "us-east-1"
	}
	effReqS3Region := c.S3Region
	if effReqS3Region == "" {
		effReqS3Region = "us-east-1"
	}
	if current.S3Bucket != "" && (c.S3Bucket != current.S3Bucket || c.S3Endpoint != current.S3Endpoint || effReqS3Region != effCurS3Region) {
		return "", fmt.Errorf("s3 descriptor drift: db=(bucket=%q,endpoint=%q,region=%q) desired=(bucket=%q,endpoint=%q,region=%q) — the cluster S3 backend is immutable once set (repoint/clear/partial-fill all refused); decommissioning is a separate explicit operation",
			current.S3Bucket, current.S3Endpoint, current.S3Region, c.S3Bucket, c.S3Endpoint, c.S3Region)
	}
	// Prefix has a ONE-TIME adoption state on top of the same immutability. A row whose descriptor was established
	// before the s3_prefix column existed carries a NULL prefix (curS3PrefixSet==false) — its true prefix lived only in
	// env. That row is allowed to adopt a prefix ONCE (any value, including the explicit empty string), which persists
	// as a non-NULL value and marks the descriptor complete. After adoption (curS3PrefixSet==true), the prefix is frozen
	// exactly like the rest of the tuple: a known-empty '' and a value are both immutable; changing or clearing either
	// is a refused repoint. This is what lets an existing prefixed cell migrate onto the new column without a repoint.
	if current.S3Bucket != "" && current.S3PrefixSet && c.S3Prefix != current.S3Prefix {
		return "", fmt.Errorf("s3 prefix drift: db=%q desired=%q — the cluster S3 prefix is immutable once adopted (repoint/clear refused); to migrate a pre-existing cell's prefix, adopt it once while it is still unset",
			current.S3Prefix, c.S3Prefix)
	}

	eligibleNoop := stringSlicesEqual(current.EligibleServingCellIds, c.EligibleServingCells)

	if current.ClusterName == c.Name &&
		current.BaseUrl == c.BaseURL &&
		current.WgMeshCidr == c.Mesh.CIDR &&
		current.WgListenPort == int32(c.Mesh.ListenPort) &&
		current.IsDefaultCluster == c.IsDefault &&
		current.IsPlatformOfficial == c.IsPlatformOfficial &&
		current.PublicTopology == c.PublicTopology &&
		current.AllowPrivatePullSources == c.AllowPrivatePullSources &&
		current.RegionID == c.Region &&
		current.CellID == c.Cell &&
		current.ClusterClass == c.Class &&
		current.ControlCellID == c.ControlCell &&
		eligibleNoop &&
		current.S3Bucket == c.S3Bucket &&
		current.S3Endpoint == c.S3Endpoint &&
		current.S3Region == c.S3Region &&
		// A NULL prefix is not a noop even when the desired value is empty: the write must persist an explicit known-empty
		// value, so require the prefix to already be set and equal.
		current.S3PrefixSet && current.S3Prefix == c.S3Prefix {
		return "noop", nil
	}

	if err := queries.UpdateBootstrapCluster(ctx, quartermasterdb.UpdateBootstrapClusterParams{
		ClusterID: c.ID, ClusterName: c.Name, BaseUrl: c.BaseURL,
		WgMeshCidr: c.Mesh.CIDR, WgListenPort: int32(c.Mesh.ListenPort),
		IsDefaultCluster: c.IsDefault, IsPlatformOfficial: c.IsPlatformOfficial,
		PublicTopology: c.PublicTopology, AllowPrivatePullSources: c.AllowPrivatePullSources,
		RegionID: c.Region, CellID: c.Cell, ClusterClass: c.Class,
		ControlCellID: c.ControlCell, EligibleServingCellIds: c.EligibleServingCells,
		S3Bucket: c.S3Bucket, S3Endpoint: c.S3Endpoint, S3Region: c.S3Region, S3Prefix: c.S3Prefix,
	}); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return "updated", nil
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
