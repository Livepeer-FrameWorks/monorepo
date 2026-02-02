package control

import (
	"context"
)

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

// GetColdStorageUsage returns aggregated cold storage usage from foghorn.artifacts.
// Uses sync_status='synced' as the authoritative cold-storage marker (S3 has an authoritative copy),
// regardless of whether a warm/local cached copy also exists.
func GetColdStorageUsage(ctx context.Context) (map[string]*ColdStorageUsage, error) {
	results := make(map[string]*ColdStorageUsage)
	if db == nil {
		return results, nil
	}

	rows, err := db.QueryContext(ctx, `
			SELECT tenant_id, artifact_type, COALESCE(SUM(size_bytes), 0) AS total_bytes, COUNT(*) AS file_count
			FROM foghorn.artifacts
			WHERE tenant_id IS NOT NULL
			  AND status != 'deleted'
			  AND sync_status = 'synced'
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
			// Unknown artifact types are ignored for cold storage summaries.
		}
	}

	return results, nil
}
