package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// artifactAssetTable maps the proto enum to the registry table, its hash column, and the tombstone
// marker kind. clip_hash, dvr_hash, vod_hash are the asset keys used at the Foghorn thumbnail-upload
// path; kind keys commodore.artifact_catalog_tombstones (DVR chapters are vod-kind, keyed by vod_hash).
func artifactAssetTable(t commodorepb.ArtifactAssetType) (table, keyCol, kind string, err error) {
	switch t {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		return "commodore.clips", "clip_hash", "clip", nil
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		return "commodore.dvr_recordings", "dvr_hash", "dvr", nil
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		return "commodore.vod_assets", "vod_hash", "vod", nil
	default:
		return "", "", "", status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", t.String())
	}
}

// UpdateArtifactCatalogSnapshot applies the authoritative catalog snapshot in one
// revision-guarded write; it is the reconciler's single-writer projection path.
//
// Source authority does NOT come from the revision number: each Foghorn owns an independent
// catalog_revision sequence, so revisions are not comparable across clusters. Authority comes
// from the RECONCILER only projecting rows its own cluster is authoritative for (origin cluster
// scoping), so exactly one cluster's reconciler ever writes a given artifact. The revision guard
// then only prevents THAT single writer's stale read from regressing a newer one — a snapshot
// applies only if source_revision exceeds the row's stored catalog_revision.
//
// Most fields are whole-state: an absent optional (size/duration/location/cluster) is written as
// NULL so a corrected snapshot repairs stale values. The exceptions preserve-on-absent via
// COALESCE — has_thumbnails and lifecycle_status — and tracks replace only when tracks_present is
// true (the source may not have captured them yet).
func (s *CommodoreServer) UpdateArtifactCatalogSnapshot(ctx context.Context, req *commodorepb.UpdateArtifactCatalogSnapshotRequest) (*commodorepb.UpdateArtifactCatalogSnapshotResponse, error) {
	tenantID := req.GetTenantId()
	assetKey := req.GetAssetKey()
	if tenantID == "" || assetKey == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and asset_key are required")
	}
	if req.GetSourceRevision() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "source_revision is required")
	}
	// Source authority is REQUIRED, not optional: the projecting cluster must identify itself so
	// Commodore can assign and enforce origin ownership. An anonymous snapshot cannot be trusted
	// to own catalog state.
	sourceCluster := strings.TrimSpace(req.GetSourceClusterId())
	if sourceCluster == "" {
		return nil, status.Error(codes.InvalidArgument, "source_cluster_id is required")
	}
	_, _, kind, err := artifactAssetTable(req.GetAssetType())
	if err != nil {
		return nil, err
	}
	lockKey := tenantID + ":" + kind + ":" + assetKey

	// Deletion projection: write the durable tombstone MARKER and REMOVE the business row in ONE
	// transaction. The marker carries Foghorn's authoritative deletion_revision and advances only to a
	// STRICTLY-GREATER revision (monotonic upsert), so a delayed/stale writer at revision <= the marker
	// cannot resurrect the asset; origin_cluster_id (NOT NULL) is claimed at insert and the DO UPDATE only
	// advances a marker of the SAME origin (a mismatched origin is rejected by the WHERE). The marker is
	// upserted EVEN WHEN no business row exists, so a delete that lands before registration is
	// representable. For a VOD (chapters are VOD-kind) the dvr_chapter_playback mapping is removed in
	// the same transaction — a partial delete must never strand a playback mapping. The per-artifact
	// advisory lock serializes this against MintChapterPlaybackID so an absent-row race cannot recreate
	// the asset between its marker check and its insert.
	if req.GetDeleted() {
		delTx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog delete begin: %v", txErr)
		}
		delCommitted := false
		defer func() {
			if !delCommitted {
				delTx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
			}
		}()
		queries := commodoredb.New(delTx)
		if lErr := queries.LockArtifactCreationIdentity(ctx, lockKey); lErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog delete lock: %v", lErr)
		}
		// Guard the LIVE business row BEFORE tombstoning or deleting it, under this transaction's advisory
		// lock. The marker upsert below is monotonic only against an EXISTING marker; it does not protect a
		// live row that a re-creation wrote after an earlier delete cleared the marker. Read the live row's
		// origin + revision and reject two unsafe cases: a foreign origin deleting a row it does not own,
		// and a delete whose revision is not strictly newer than the live row (a delayed delete that
		// predates a re-creation). Revisions are cluster-local, so the revision compare is meaningful only
		// for the same origin — the origin check runs first.
		live, liveErr := getLiveArtifactCatalogStateForUpdate(ctx, queries, req.GetAssetType(), tenantID, assetKey)
		switch {
		case liveErr == nil:
			if live.originCluster.Valid && live.originCluster.String != "" && live.originCluster.String != sourceCluster {
				return nil, status.Errorf(codes.PermissionDenied,
					"source cluster %q is not the origin cluster %q for live asset %s", sourceCluster, live.originCluster.String, assetKey)
			}
			if live.catalogRevision.Valid && req.GetSourceRevision() <= live.catalogRevision.Int64 {
				// The live row is at an equal-or-newer revision: this delete is stale (it predates a
				// re-creation). Leave the row intact and do NOT tombstone.
				if commitErr := delTx.Commit(); commitErr != nil {
					return nil, status.Errorf(codes.Internal, "catalog delete commit: %v", commitErr)
				}
				delCommitted = true
				return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: live.catalogRevision.Int64}, nil
			}
		case errors.Is(liveErr, sql.ErrNoRows):
			// No live row — a delete that lands before registration (or after an earlier removal) still
			// records the tombstone below so the removal is representable.
		default:
			return nil, status.Errorf(codes.Internal, "catalog delete live-row read: %v", liveErr)
		}
		markerRevision, delErr := queries.UpsertArtifactCatalogTombstone(ctx, commodoredb.UpsertArtifactCatalogTombstoneParams{
			TenantID: tenantID, Kind: kind, AssetKey: assetKey, OriginClusterID: sourceCluster,
			DeletionRevision: req.GetSourceRevision(),
		})
		if delErr == nil {
			// Marker durably present at markerRevision (>= source_revision). Remove the live business
			// row so ordinary readers (absence = not live) stop returning it.
			if rErr := deleteArtifactCatalogBusinessRow(ctx, queries, req.GetAssetType(), tenantID, assetKey); rErr != nil {
				return nil, status.Errorf(codes.Internal, "catalog delete row: %v", rErr)
			}
			if kind == "vod" {
				// Tenant-scoped delete: dvr_chapter_playback carries tenant_id as its ownership boundary. The
				// artifact_hash is a globally-unique, opaque id (not a content hash), so scoping by tenant is an
				// ownership proof for the delete, not a cross-tenant collision guard.
				if pErr := queries.DeleteDVRChapterPlaybackByArtifact(ctx, commodoredb.DeleteDVRChapterPlaybackByArtifactParams{TenantID: tenantID, AssetKey: assetKey}); pErr != nil {
					return nil, status.Errorf(codes.Internal, "delete chapter playback row: %v", pErr)
				}
			}
			if commitErr := delTx.Commit(); commitErr != nil {
				return nil, status.Errorf(codes.Internal, "catalog delete commit: %v", commitErr)
			}
			delCommitted = true
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: markerRevision}, nil
		}
		if !errors.Is(delErr, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "catalog delete marker failed: %v", delErr)
		}
		// 0 rows from the upsert means the ON CONFLICT guard rejected it: a marker already exists under a
		// different origin cluster. Surface it as an authority denial so a non-origin caller never
		// mistakes it for coverage. The readback runs through delTx so the decision stays in ONE tx.
		markerOrigin, oErr := queries.GetArtifactCatalogTombstoneOrigin(ctx, commodoredb.GetArtifactCatalogTombstoneOriginParams{TenantID: tenantID, Kind: kind, AssetKey: assetKey})
		if oErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog delete marker readback failed: %v", oErr)
		}
		return nil, status.Errorf(codes.PermissionDenied,
			"source cluster %q is not the origin cluster %q for tombstone %s", sourceCluster, markerOrigin, assetKey)
	}

	// Non-delete projection: the marker guards resurrection. A marker at deletion_revision >= this
	// snapshot's source_revision supersedes it (the asset was deleted at an equal-or-newer authoritative
	// revision) — do not revive; report the marker revision as covered so the reconciler advances past
	// the obsolete snapshot. Only a STRICTLY-NEWER source_revision (a genuine re-creation) supersedes
	// the marker: clear it, then apply the revive. The check + clear + revive run in ONE transaction
	// under the per-artifact advisory lock, so a concurrent delete cannot interleave.
	reviveTx, txErr := s.db.BeginTx(ctx, nil)
	if txErr != nil {
		return nil, status.Errorf(codes.Internal, "catalog snapshot begin: %v", txErr)
	}
	reviveCommitted := false
	defer func() {
		if !reviveCommitted {
			reviveTx.Rollback() //nolint:errcheck // best-effort rollback of an uncommitted tx
		}
	}()
	queries := commodoredb.New(reviveTx)
	if lErr := queries.LockArtifactCreationIdentity(ctx, lockKey); lErr != nil {
		return nil, status.Errorf(codes.Internal, "catalog snapshot lock: %v", lErr)
	}
	marker, mErr := queries.GetArtifactCatalogTombstoneForUpdate(ctx, commodoredb.GetArtifactCatalogTombstoneForUpdateParams{
		TenantID: tenantID, Kind: kind, AssetKey: assetKey,
	})
	switch {
	case mErr == nil:
		// Revisions are cluster-LOCAL and incomparable across origins. A tombstone written by a
		// DIFFERENT origin cluster can be neither superseded nor cleared by this snapshot — a foreign
		// cluster's coincidentally-larger revision must NOT resurrect another origin's deleted asset.
		// Reject BEFORE the revision comparison or any marker clear (fail closed). Enforced HERE, not
		// only at the business-row UPDATE below, because an absent business row would otherwise let the
		// marker clear commit as a benign not-found.
		if marker.OriginClusterID != sourceCluster {
			return nil, status.Errorf(codes.PermissionDenied,
				"source cluster %q is not the tombstone origin %q for %s", sourceCluster, marker.OriginClusterID, assetKey)
		}
		if marker.DeletionRevision >= req.GetSourceRevision() {
			if commitErr := reviveTx.Commit(); commitErr != nil {
				return nil, status.Errorf(codes.Internal, "catalog snapshot commit: %v", commitErr)
			}
			reviveCommitted = true
			return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: marker.DeletionRevision}, nil
		}
		if dErr := queries.ClearArtifactCatalogTombstone(ctx, commodoredb.ClearArtifactCatalogTombstoneParams{TenantID: tenantID, Kind: kind, AssetKey: assetKey}); dErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog snapshot clear marker: %v", dErr)
		}
	case errors.Is(mErr, sql.ErrNoRows):
		// No marker → not deleted; proceed with the revive.
	default:
		return nil, status.Errorf(codes.Internal, "catalog snapshot marker check: %v", mErr)
	}

	tracksArg := sql.NullString{}
	if req.GetTracksPresent() {
		body, mErr := commodoreclient.MarshalMediaTracks(req.GetTracks())
		if mErr != nil {
			return nil, status.Errorf(codes.Internal, "marshal tracks: %v", mErr)
		}
		tracksArg = sql.NullString{String: string(body), Valid: true}
	}
	// Most fields are whole-state: an absent optional is written as NULL so the snapshot repairs
	// stale values, not merely adds. Exceptions COALESCE to the stored value when absent —
	// has_thumbnails and lifecycle_status (matching the proto's documented "absent preserves"
	// contract) — and tracks are replaced only when tracks_present.
	// storage_cluster_id keeps its nullable semantics: NULL means "same as origin cluster"
	// (ListStorageArtifacts falls back via COALESCE), so an absent value writes SQL NULL, not
	// '', which would defeat the fallback and erase attribution.
	//
	// Source authority is assigned AND enforced in this one guarded write:
	//   - origin_cluster_id IS NULL  → the artifact is unattributed; the writer CLAIMS ownership
	//     (`origin_cluster_id = COALESCE(origin_cluster_id, $16)`), so a wrong first writer can no
	//     longer leave the row perpetually clobberable.
	//   - origin_cluster_id = $16    → the origin cluster; allowed.
	//   - origin_cluster_id <> $16   → a non-origin cluster; the WHERE matches 0 rows and the
	//     read-back below distinguishes this (PermissionDenied) from a mere revision-behind.
	params := commodoredb.ApplyClipCatalogSnapshotParams{
		SizeBytes: nullableInt64(req.SizeBytes), DurationMs: nullableInt64(req.DurationMs),
		TracksPresent: req.GetTracksPresent(), Tracks: tracksArg,
		SyncStatus: nullableString(req.SyncStatus), IsSynced: nullableBool(req.IsSynced), IsFinalized: nullableBool(req.IsFinalized),
		StorageLocation: nullableString(req.StorageLocation), StorageClusterID: nullableString(req.StorageClusterId),
		HasThumbnails: nullableBool(req.HasThumbnails), LifecycleStatus: nullableString(req.LifecycleStatus),
		OriginClusterID: sql.NullString{String: sourceCluster, Valid: true}, RetentionUntilUnix: nullableInt64(req.RetentionUntilUnix),
		ErrorMessage: nullableString(req.ErrorMessage), ThumbnailServingClusterID: nullableString(req.ThumbnailServingClusterId),
		SourceRevision: req.GetSourceRevision(), TenantID: tenantID, AssetKey: assetKey,
	}
	newRevision, appliedServingCluster, execErr := applyArtifactCatalogSnapshot(ctx, queries, req.GetAssetType(), params)
	if execErr == nil {
		// Applied: the row's revision is now source_revision.
		if commitErr := reviveTx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog snapshot commit: %v", commitErr)
		}
		reviveCommitted = true
		// Echo the STORED serving cluster so the caller can confirm a NEW Commodore applied field 21 (mixed-version ack).
		return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: newRevision.Int64, ThumbnailServingClusterId: nullStringToPtr(appliedServingCluster)}, nil
	}
	if !errors.Is(execErr, sql.ErrNoRows) {
		s.logger.WithError(execErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"asset_type": req.GetAssetType().String(),
			"asset_key":  assetKey,
		}).Error("UpdateArtifactCatalogSnapshot failed")
		return nil, status.Errorf(codes.Internal, "update failed: %v", execErr)
	}
	// The guarded UPDATE matched no row: the artifact isn't registered yet, OR the guard rejected
	// it (revision already >= source, OR a source-cluster/origin mismatch). Read origin_cluster_id
	// + catalog_revision to DISTINGUISH an authority denial from a benign revision-behind: a
	// non-origin caller must get an explicit PermissionDenied, never a false "covered". Any marker
	// clear above is committed so a genuine re-creation is unblocked.
	stored, qErr := getArtifactCatalogState(ctx, queries, req.GetAssetType(), tenantID, assetKey)
	if errors.Is(qErr, sql.ErrNoRows) {
		if commitErr := reviveTx.Commit(); commitErr != nil {
			return nil, status.Errorf(codes.Internal, "catalog snapshot commit: %v", commitErr)
		}
		reviveCommitted = true
		return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: false}, nil
	}
	if qErr != nil {
		return nil, status.Errorf(codes.Internal, "revision read failed: %v", qErr)
	}
	// Authority denial: the row is owned by a different origin cluster. Surface it explicitly so
	// the caller backs off rather than mistaking the stored revision for its own coverage.
	if stored.originCluster.Valid && stored.originCluster.String != "" && stored.originCluster.String != sourceCluster {
		return nil, status.Errorf(codes.PermissionDenied,
			"source cluster %q is not the origin cluster %q for %s", sourceCluster, stored.originCluster.String, assetKey)
	}
	// WRITE-ONCE CONFLICT: the serving cluster is stable (the tenant's official cluster). A stored non-null value that
	// DIFFERS from a non-empty incoming one is a real invariant violation (a thumbnail re-projected to a different
	// official cluster) — fail LOUDLY rather than silently loop or overwrite. A NULL→value fill already applied above.
	if incoming := strings.TrimSpace(req.GetThumbnailServingClusterId()); incoming != "" &&
		stored.servingCluster.Valid && stored.servingCluster.String != "" && stored.servingCluster.String != incoming {
		return nil, status.Errorf(codes.FailedPrecondition,
			"thumbnail serving cluster conflict for %s: stored %q, incoming %q (write-once)", assetKey, stored.servingCluster.String, incoming)
	}
	if commitErr := reviveTx.Commit(); commitErr != nil {
		return nil, status.Errorf(codes.Internal, "catalog snapshot commit: %v", commitErr)
	}
	reviveCommitted = true
	// Echo the stored serving cluster here too, so a caller that is revision-behind (its projection already superseded)
	// can still confirm the field is stored and advance without re-projecting forever.
	return &commodorepb.UpdateArtifactCatalogSnapshotResponse{Found: true, CurrentRevision: stored.catalogRevision.Int64, ThumbnailServingClusterId: nullStringToPtr(stored.servingCluster)}, nil
}

type artifactCatalogState struct {
	originCluster   sql.NullString
	catalogRevision sql.NullInt64
	servingCluster  sql.NullString
}

func getLiveArtifactCatalogStateForUpdate(ctx context.Context, queries *commodoredb.Queries, assetType commodorepb.ArtifactAssetType, tenantID, assetKey string) (artifactCatalogState, error) {
	switch assetType {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		row, err := queries.GetLiveClipCatalogStateForUpdate(ctx, commodoredb.GetLiveClipCatalogStateForUpdateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision}, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		row, err := queries.GetLiveDVRCatalogStateForUpdate(ctx, commodoredb.GetLiveDVRCatalogStateForUpdateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision}, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		row, err := queries.GetLiveVODCatalogStateForUpdate(ctx, commodoredb.GetLiveVODCatalogStateForUpdateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision}, err
	default:
		return artifactCatalogState{}, status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", assetType.String())
	}
}

func deleteArtifactCatalogBusinessRow(ctx context.Context, queries *commodoredb.Queries, assetType commodorepb.ArtifactAssetType, tenantID, assetKey string) error {
	params := commodoredb.DeleteCatalogOnlyVODParams{TenantID: tenantID, ArtifactHash: assetKey}
	switch assetType {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		return queries.DeleteCatalogOnlyClip(ctx, commodoredb.DeleteCatalogOnlyClipParams(params))
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		return queries.DeleteCatalogOnlyDVR(ctx, commodoredb.DeleteCatalogOnlyDVRParams(params))
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		return queries.DeleteCatalogOnlyVOD(ctx, params)
	default:
		return status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", assetType.String())
	}
}

func applyArtifactCatalogSnapshot(ctx context.Context, queries *commodoredb.Queries, assetType commodorepb.ArtifactAssetType, params commodoredb.ApplyClipCatalogSnapshotParams) (sql.NullInt64, sql.NullString, error) {
	switch assetType {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		row, err := queries.ApplyClipCatalogSnapshot(ctx, params)
		return row.CatalogRevision, row.ThumbnailServingClusterID, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		row, err := queries.ApplyDVRCatalogSnapshot(ctx, commodoredb.ApplyDVRCatalogSnapshotParams(params))
		return row.CatalogRevision, row.ThumbnailServingClusterID, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		row, err := queries.ApplyVODCatalogSnapshot(ctx, commodoredb.ApplyVODCatalogSnapshotParams(params))
		return row.CatalogRevision, row.ThumbnailServingClusterID, err
	default:
		return sql.NullInt64{}, sql.NullString{}, status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", assetType.String())
	}
}

func getArtifactCatalogState(ctx context.Context, queries *commodoredb.Queries, assetType commodorepb.ArtifactAssetType, tenantID, assetKey string) (artifactCatalogState, error) {
	switch assetType {
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_CLIP:
		row, err := queries.GetClipCatalogState(ctx, commodoredb.GetClipCatalogStateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision, servingCluster: row.ThumbnailServingClusterID}, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_DVR:
		row, err := queries.GetDVRCatalogState(ctx, commodoredb.GetDVRCatalogStateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision, servingCluster: row.ThumbnailServingClusterID}, err
	case commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD:
		row, err := queries.GetVODCatalogState(ctx, commodoredb.GetVODCatalogStateParams{TenantID: tenantID, AssetKey: assetKey})
		return artifactCatalogState{originCluster: row.OriginClusterID, catalogRevision: row.CatalogRevision, servingCluster: row.ThumbnailServingClusterID}, err
	default:
		return artifactCatalogState{}, status.Errorf(codes.InvalidArgument, "unsupported asset_type: %s", assetType.String())
	}
}

func nullableInt64(v *int64) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *v, Valid: true}
}

func nullableString(v *string) sql.NullString {
	if v == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *v, Valid: true}
}

func nullableBool(v *bool) sql.NullBool {
	if v == nil {
		return sql.NullBool{}
	}
	return sql.NullBool{Bool: *v, Valid: true}
}

// nullStringToPtr converts a scanned sql.NullString to the *string an optional proto field expects (nil when NULL).
func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}
