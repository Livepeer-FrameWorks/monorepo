package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		if _, err := exec.ExecContext(ctx, `
			UPDATE quartermaster.infrastructure_clusters
			SET is_default_cluster = false, updated_at = NOW()
			WHERE is_default_cluster = true`); err != nil {
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
	const probeSQL = `
		SELECT
			cluster_name, cluster_type,
			COALESCE(owner_tenant_id::text, ''),
			COALESCE(base_url, ''),
			COALESCE(wg_mesh_cidr, ''),
			COALESCE(wg_listen_port, 0),
			is_default_cluster, is_platform_official
		FROM quartermaster.infrastructure_clusters
		WHERE cluster_id = $1`
	var (
		curName, curType, curOwner, curBaseURL, curCIDR string
		curListenPort                                   int
		curIsDefault, curIsPlatform                     bool
	)
	err := exec.QueryRowContext(ctx, probeSQL, c.ID).Scan(
		&curName, &curType, &curOwner, &curBaseURL, &curCIDR, &curListenPort, &curIsDefault, &curIsPlatform,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		const insertSQL = `
			INSERT INTO quartermaster.infrastructure_clusters (
				cluster_id, cluster_name, cluster_type,
				owner_tenant_id, base_url,
				wg_mesh_cidr, wg_listen_port,
				is_default_cluster, is_platform_official,
				created_at, updated_at
			) VALUES (
				$1, $2, $3,
				NULLIF($4, '')::uuid, NULLIF($5, ''),
				NULLIF($6, ''), NULLIF($7, 0),
				$8, $9,
				NOW(), NOW()
			)`
		if _, insertErr := exec.ExecContext(ctx, insertSQL,
			c.ID, c.Name, c.Type,
			ownerID, c.BaseURL,
			c.Mesh.CIDR, c.Mesh.ListenPort,
			c.IsDefault, c.IsPlatformOfficial,
		); insertErr != nil {
			return "", fmt.Errorf("insert: %w", insertErr)
		}
		return "created", nil
	case err != nil:
		return "", fmt.Errorf("probe: %w", err)
	}

	// Stable-key drift: cluster_type, owner_tenant_id, and an existing
	// wg_mesh_cidr must not change once set. These are facts the rest of the
	// platform indexes against; reassigning them silently corrupts mesh
	// allocations and tenant-scoped RBAC.
	if curType != c.Type {
		return "", fmt.Errorf("type drift: db=%q desired=%q (cluster_type is stable; refusing rewrite)", curType, c.Type)
	}
	if curOwner != ownerID {
		return "", fmt.Errorf("owner drift: db=%q desired=%q (owner_tenant_id is stable; refusing rewrite)", curOwner, ownerID)
	}
	if curCIDR != "" && curCIDR != c.Mesh.CIDR {
		return "", fmt.Errorf("mesh.cidr drift: db=%q desired=%q (cidr is stable once set; refusing rewrite)", curCIDR, c.Mesh.CIDR)
	}

	if curName == c.Name &&
		curBaseURL == c.BaseURL &&
		curCIDR == c.Mesh.CIDR &&
		curListenPort == c.Mesh.ListenPort &&
		curIsDefault == c.IsDefault &&
		curIsPlatform == c.IsPlatformOfficial {
		return "noop", nil
	}

	const updateSQL = `
		UPDATE quartermaster.infrastructure_clusters
		SET cluster_name = $2,
		    base_url = NULLIF($3, ''),
		    wg_mesh_cidr = NULLIF($4, ''),
		    wg_listen_port = NULLIF($5, 0),
		    is_default_cluster = $6,
		    is_platform_official = $7,
		    updated_at = NOW()
		WHERE cluster_id = $1`
	if _, err := exec.ExecContext(ctx, updateSQL,
		c.ID, c.Name, c.BaseURL,
		c.Mesh.CIDR, c.Mesh.ListenPort,
		c.IsDefault, c.IsPlatformOfficial,
	); err != nil {
		return "", fmt.Errorf("update: %w", err)
	}
	return "updated", nil
}
