package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"frameworks/api_balancing/internal/database/foghorndb"
)

// billingAttributionBatch bounds the DISTINCT (tenant, authoritative-cluster) pairs a single
// ReconcileBillingAttribution pass reviews, and thus the external per-tenant resolver calls it makes. Progress
// is persisted in foghorn.billing_attribution_cursor so the next pass continues past the last reviewed pair
// (never restarts at the same prefix), and wraps to the start on reaching the end.
const billingAttributionBatch = 200

// ReconcileBillingAttribution marks synced artifacts that are UNAMBIGUOUSLY on THIS cell's local backend by
// RECORDED EVIDENCE ONLY — the row's own persisted authoritative cluster is empty (schema convention: NULL =
// local) or equals this cell's runtime cluster id. It does NOT consult the storage resolver / current advertised
// backing (invariant I2): a cluster that re-points its backing to this cell AFTER bytes were written elsewhere
// must NOT retroactively become "local" — current topology is not evidence of where historical bytes live. Rows
// whose backend cannot be established from recorded evidence are left unattributed for a pre-billing-launch
// evidence-based backfill (persisted object URLs/backend ids, provider inventory), never today's resolver.
// Idempotent (only flips false→true). Returns the number of rows marked. Federation-adopted remote rows are never
// claimed here.
//
// BOUNDED, DURABLE KEYSET PROGRESS: each pass reviews at most billingAttributionBatch DISTINCT (tenant, cluster)
// pairs PAST a durable cursor and advances it, bounding per-pass scan/update volume on a large table; on reaching
// the end the cursor wraps to re-review from the top (cheap: the marked pairs have left the unmarked set).
func ReconcileBillingAttribution(ctx context.Context) (int, error) {
	if db == nil {
		return 0, nil
	}
	// Read the durable cursor (single-row table seeded by the migration; treat a missing row as the start).
	queries := foghorndb.New(db)
	cursor, cErr := queries.GetBillingAttributionCursor(ctx)
	if cErr != nil && !errors.Is(cErr, sql.ErrNoRows) {
		return 0, cErr
	}
	lastTenant, lastCluster := cursor.LastTenant, cursor.LastCluster

	// A BOUNDED page of DISTINCT (tenant, authoritative-cluster) pairs among still-unmarked synced rows,
	// strictly AFTER the cursor in (tenant, cluster) order. Owned pairs are marked and leave the unmarked set;
	// the cursor advances past every reviewed pair so genuinely-remote pairs never block later owned ones.
	rows, err := queries.ListUnattributedStoragePairs(ctx, foghorndb.ListUnattributedStoragePairsParams{
		LastTenant: lastTenant, LastCluster: lastCluster, PageLimit: int32(billingAttributionBatch),
	})
	if err != nil {
		return 0, err
	}
	type pair struct{ tenant, cluster string }
	var page, owned []pair
	local := strings.TrimSpace(localClusterID)
	for _, row := range rows {
		p := pair{tenant: row.Tenant, cluster: row.Cluster}
		page = append(page, p)
		// Owned by RECORDED EVIDENCE ONLY (I2): the row's persisted authoritative cluster is empty (NULL = local
		// by schema convention) or equals this cell's cluster id. The current advertised backing is NOT consulted —
		// a resolver verdict is topology-now, not evidence of where the historical bytes were written.
		if p.cluster == "" || (local != "" && p.cluster == local) {
			owned = append(owned, p)
		}
	}
	marked := 0
	for _, p := range owned {
		n, uErr := queries.MarkStoragePairLocallyAttributed(ctx, foghorndb.MarkStoragePairLocallyAttributedParams{
			TenantID: p.tenant, ClusterID: sql.NullString{String: p.cluster, Valid: true},
		})
		if uErr != nil {
			return marked, uErr
		}
		if n > 0 {
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
	if uErr := queries.SetBillingAttributionCursor(ctx, foghorndb.SetBillingAttributionCursorParams{
		LastTenant: nextTenant, LastCluster: nextCluster,
	}); uErr != nil {
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

	rows, err := foghorndb.New(db).ListColdStorageUsage(ctx)
	if err != nil {
		return results, err
	}
	for _, row := range rows {
		tenantID, artifactType := row.TenantID, row.ArtifactType
		totalBytes, fileCount := uint64(row.TotalBytes), uint32(row.FileCount)

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
	return results, nil
}
