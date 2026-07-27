package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// billingAttributionBatch bounds the DISTINCT (tenant, authoritative-cluster) pairs a single
// ReconcileBillingAttribution pass reviews, and thus the external per-tenant resolver calls it makes. Progress
// is persisted in foghorn.billing_attribution_cursor so the next pass continues past the last reviewed pair
// (never restarts at the same prefix), and wraps to the start on reaching the end.
const billingAttributionBatch = 200

// ReconcileBillingAttribution marks synced artifacts that are UNAMBIGUOUSLY on THIS cell's local backend but
// were written before durable_backend_local existed (or before the claim set it). It is IDENTITY-AWARE — it
// uses this cell's runtime cluster identity, not a blind SQL rule — so it can claim legitimate historical
// local rows (authoritative cluster == the local cluster, or a served cluster whose per-tenant backing
// resolves LOCAL) that the conservative both-null postdeploy backfill deliberately skipped. Idempotent (only
// flips false→true). Returns the number of rows marked. Federation-adopted remote rows are never claimed here.
//
// BOUNDED, DURABLE KEYSET PROGRESS: earlier this scanned ALL unmarked pairs with the resolver in the row loop
// under a shared timeout — if the ctx expired mid-scan, every pass restarted at the same prefix and a large or
// slow prefix could permanently STARVE later locally-owned pairs. Now each pass reviews at most
// billingAttributionBatch pairs PAST a durable cursor and advances it, so all pairs are reached within a
// bounded number of passes; on reaching the end the cursor wraps to re-review pairs whose access may change.
func ReconcileBillingAttribution(ctx context.Context) (int, error) {
	if db == nil {
		return 0, nil
	}
	// Read the durable cursor (single-row table seeded by the migration; treat a missing row as the start).
	var lastTenant, lastCluster string
	if cErr := db.QueryRowContext(ctx,
		`SELECT last_tenant, last_cluster FROM foghorn.billing_attribution_cursor WHERE id = true`,
	).Scan(&lastTenant, &lastCluster); cErr != nil && !errors.Is(cErr, sql.ErrNoRows) {
		return 0, cErr
	}

	// A BOUNDED page of DISTINCT (tenant, authoritative-cluster) pairs among still-unmarked synced rows,
	// strictly AFTER the cursor in (tenant, cluster) order. Owned pairs are marked and leave the unmarked set;
	// the cursor advances past every reviewed pair so genuinely-remote pairs never block later owned ones.
	rows, err := db.QueryContext(ctx, `
		SELECT p.tenant, p.cluster FROM (
			SELECT DISTINCT tenant_id::text AS tenant,
			       COALESCE(NULLIF(storage_cluster_id,''), NULLIF(origin_cluster_id,''), '') AS cluster
			FROM foghorn.artifacts
			WHERE sync_status = 'synced' AND durable_backend_local = false AND tenant_id IS NOT NULL
		) p
		WHERE (p.tenant, p.cluster) > ($1, $2)
		ORDER BY p.tenant, p.cluster
		LIMIT $3`, lastTenant, lastCluster, billingAttributionBatch)
	if err != nil {
		return 0, err
	}
	type pair struct{ tenant, cluster string }
	var page, owned []pair
	local := strings.TrimSpace(localClusterID)
	for rows.Next() {
		var p pair
		if scanErr := rows.Scan(&p.tenant, &p.cluster); scanErr != nil {
			rows.Close() //nolint:errcheck,sqlclosecheck
			return 0, scanErr
		}
		page = append(page, p)
		// Owned when: pure-local (empty cluster), the authoritative cluster IS this cell's local cluster, or
		// the tenant's own resolver maps that cluster to a LOCAL mint on this cell's backend (the only external
		// call — and it is now bounded to at most billingAttributionBatch per pass).
		if p.cluster == "" || (local != "" && p.cluster == local) || canMintOfficialLocallyFn(ctx, p.tenant, p.cluster) {
			owned = append(owned, p)
		}
	}
	if rowErr := rows.Err(); rowErr != nil {
		rows.Close() //nolint:errcheck,sqlclosecheck
		return 0, rowErr
	}
	rows.Close() //nolint:errcheck,sqlclosecheck

	marked := 0
	for _, p := range owned {
		res, uErr := db.ExecContext(ctx, `
			UPDATE foghorn.artifacts
			SET durable_backend_local = true
			WHERE sync_status = 'synced' AND durable_backend_local = false
			  AND tenant_id::text = $1
			  AND COALESCE(NULLIF(storage_cluster_id,''), NULLIF(origin_cluster_id,''), '') = $2`,
			p.tenant, p.cluster)
		if uErr != nil {
			return marked, uErr
		}
		if n, raErr := res.RowsAffected(); raErr == nil && n > 0 {
			marked += int(n)
		}
	}

	// Advance the durable cursor: a FULL page means more pairs remain → continue past the last reviewed pair;
	// a short page means we reached the end → wrap to the start so the next cycle re-reviews from the top.
	nextTenant, nextCluster := "", ""
	if len(page) == billingAttributionBatch {
		last := page[len(page)-1]
		nextTenant, nextCluster = last.tenant, last.cluster
	}
	if _, uErr := db.ExecContext(ctx, `
		UPDATE foghorn.billing_attribution_cursor SET last_tenant = $1, last_cluster = $2 WHERE id = true`,
		nextTenant, nextCluster); uErr != nil {
		return marked, uErr
	}
	return marked, nil
}

// ColdStorageUsage aggregates S3-backed storage by tenant and type.
// Uses sync_status='synced' as the cold-storage marker (S3 has an authoritative copy),
// regardless of whether a warm/local cached copy also exists.
type ColdStorageUsage struct {
	TenantID  string
	FileCount uint32
	DvrBytes  uint64
	ClipBytes uint64
	VodBytes  uint64
}

// GetColdStorageUsage returns aggregated cold storage usage from foghorn.artifacts, restricted to storage
// THIS provider actually owns.
//
// Ownership is read from the STABLE, write-time attribution column durable_backend_local — NOT recomputed
// from tenant routing at read time. Recomputing was unsound: a tenant's access can expire or their BYOC
// backing can change while the bytes remain in this bucket, and the read-time resolver would then drop those
// bytes from billing even though this provider still stores them. durable_backend_local is set TRUE where a
// local mint is claimed/completed or a VOD lands on local S3, and left FALSE for playback-federation
// adopted-remote rows (bytes on another provider's backend; cross-provider settlement is
// docs/rfcs/cross-cluster-durable-replication-v1.md).
func GetColdStorageUsage(ctx context.Context) (map[string]*ColdStorageUsage, error) {
	results := make(map[string]*ColdStorageUsage)
	if db == nil {
		return results, nil
	}

	rows, err := db.QueryContext(ctx, `
			SELECT tenant_id::text, artifact_type, COALESCE(SUM(size_bytes), 0) AS total_bytes, COUNT(*) AS file_count
			FROM foghorn.artifacts
			WHERE tenant_id IS NOT NULL
			  AND status != 'deleted'
			  AND sync_status = 'synced'
			  AND durable_backend_local = true
			GROUP BY tenant_id, artifact_type
		`)
	if err != nil {
		return results, err
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID, artifactType string
		var totalBytes uint64
		var fileCount uint32
		if err := rows.Scan(&tenantID, &artifactType, &totalBytes, &fileCount); err != nil {
			return results, err
		}

		usage := results[tenantID]
		if usage == nil {
			usage = &ColdStorageUsage{TenantID: tenantID}
			results[tenantID] = usage
		}

		switch artifactType {
		case "clip":
			usage.ClipBytes += totalBytes
			usage.FileCount += fileCount
		case "dvr":
			usage.DvrBytes += totalBytes
			usage.FileCount += fileCount
		case "vod":
			usage.VodBytes += totalBytes
			usage.FileCount += fileCount
		default:
			// An owned, synced artifact whose type has NO billing bucket would vanish from the snapshot if we
			// silently skipped it — a silent under-bill. Fail the whole snapshot (like the rows.Err() path
			// below) so a new artifact type surfaces loudly and a bucket is added, rather than under-counting.
			return results, fmt.Errorf("cold storage usage: owned synced artifact type %q has no billing bucket (tenant %s, %d bytes)", artifactType, tenantID, totalBytes)
		}
	}
	// A row iteration error (e.g. a truncated result set) must NOT be reported as a complete usage snapshot
	// — that would silently UNDER-bill. Fail the call so the caller does not emit a partial snapshot.
	if err := rows.Err(); err != nil {
		return results, err
	}

	return results, nil
}
