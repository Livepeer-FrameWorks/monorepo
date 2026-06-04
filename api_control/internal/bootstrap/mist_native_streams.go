package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/lib/pq"
)

// ReconcileMistNativeStreams provisions operator-owned mist_native streams
// declared in the rendered bootstrap file into commodore.streams +
// commodore.stream_mist_sources, and the per-stream MistServer process policy
// (when set) into commodore.stream_processing_config.
//
// Stable key: (tenant_id, playback_id). Idempotent semantics mirror
// ReconcilePullStreams:
//
//   - Stream absent ⇒ create commodore.streams + stream_mist_sources, and
//     stream_processing_config when ProcessPolicy is non-nil.
//   - Stream present, all fields match ⇒ noop.
//   - Stream present, mutable fields differ ⇒ update the affected rows.
//
// ProcessPolicy lives in commodore.stream_processing_config rather than on
// commodore.streams so the process-policy authority stays in one place
// alongside tenant_processing_config; resolveProcessesJSON consults the
// per-stream layer before the per-tenant layer.
func ReconcileMistNativeStreams(
	ctx context.Context,
	exec DBTX,
	streams []MistNativeStream,
	resolver TenantResolver,
) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("ReconcileMistNativeStreams: nil executor")
	}
	if resolver == nil {
		return Result{}, errors.New("ReconcileMistNativeStreams: nil tenant resolver")
	}

	res := Result{}

	// Build the desired-state index per tenant so the absent-from-desired
	// prune pass can scope deletes to bootstrap-owned tenants only. Tenants
	// that have no mist_native streams in bootstrap.yaml don't get scanned
	// (so out-of-band mist_native streams under other tenants stay intact).
	desiredByTenant := make(map[string]map[string]struct{})

	for _, ms := range streams {
		if err := validateMistNativeShape(ms); err != nil {
			return Result{}, err
		}
		ms.AllowedClusterIDs = normalizeAllowedClusterIDs(ms.AllowedClusterIDs)

		alias, err := AliasFromRef(ms.OwnerTenant.Ref)
		if err != nil {
			return Result{}, fmt.Errorf("mist_native_stream %q: %w", ms.PlaybackID, err)
		}
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return Result{}, fmt.Errorf("mist_native_stream %q: %w", ms.PlaybackID, err)
		}

		if desiredByTenant[tenantID] == nil {
			desiredByTenant[tenantID] = make(map[string]struct{})
		}
		desiredByTenant[tenantID][strings.ToLower(ms.PlaybackID)] = struct{}{}

		action, err := reconcileMistNativeStream(ctx, exec, tenantID, alias, ms)
		if err != nil {
			return Result{}, fmt.Errorf("mist_native_stream %q: %w", ms.PlaybackID, err)
		}
		switch action {
		case "created":
			res.Created = append(res.Created, ms.PlaybackID)
		case "updated":
			res.Updated = append(res.Updated, ms.PlaybackID)
		case "noop":
			res.Noop = append(res.Noop, ms.PlaybackID)
		}
	}

	// Declarative delete: for every tenant that owns at least one
	// bootstrap-declared mist_native stream, any other mist_native stream
	// under the same tenant that is NOT in the desired set gets deleted.
	// Cascade-deletes commodore.stream_mist_sources +
	// commodore.stream_processing_config via FK.
	for tenantID, desired := range desiredByTenant {
		removed, err := pruneAbsentMistNativeStreams(ctx, exec, tenantID, desired)
		if err != nil {
			return Result{}, fmt.Errorf("prune absent mist_native streams (tenant %s): %w", tenantID, err)
		}
		res.Deleted = append(res.Deleted, removed...)
	}

	return res, nil
}

// PruneAllMistNativeStreams is called when the bootstrap manifest declares
// no mist_native streams at all. Without this, removing the last entry from
// bootstrap.yaml would leave existing rows running. The caller must scope
// `tenants` to the set under bootstrap control (typically the operator/
// system tenant alone); any tenant in the list gets every mist_native
// stream deleted.
func PruneAllMistNativeStreams(ctx context.Context, exec DBTX, resolver TenantResolver, tenantAliases []string) (Result, error) {
	if exec == nil {
		return Result{}, errors.New("PruneAllMistNativeStreams: nil executor")
	}
	if resolver == nil {
		return Result{}, errors.New("PruneAllMistNativeStreams: nil tenant resolver")
	}
	res := Result{}
	for _, alias := range tenantAliases {
		tenantID, err := resolver.Resolve(ctx, alias)
		if err != nil {
			return Result{}, fmt.Errorf("resolve tenant %s: %w", alias, err)
		}
		removed, err := pruneAbsentMistNativeStreams(ctx, exec, tenantID, nil)
		if err != nil {
			return Result{}, fmt.Errorf("prune mist_native streams (tenant %s): %w", tenantID, err)
		}
		res.Deleted = append(res.Deleted, removed...)
	}
	return res, nil
}

// pruneAbsentMistNativeStreams deletes every mist_native stream for the
// tenant whose lowercased playback_id is NOT in `desired`. A nil/empty
// `desired` deletes every mist_native stream for the tenant.
func pruneAbsentMistNativeStreams(ctx context.Context, exec DBTX, tenantID string, desired map[string]struct{}) ([]string, error) {
	const listSQL = `
		SELECT s.id::text, s.playback_id
		FROM commodore.streams s
		WHERE s.tenant_id = $1::uuid AND s.ingest_mode = 'mist_native'`
	rows, err := exec.QueryContext(ctx, listSQL, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list mist_native streams: %w", err)
	}
	defer rows.Close() //nolint:errcheck // best-effort cleanup; iteration errors handled below

	type victim struct {
		id         string
		playbackID string
	}
	var victims []victim
	for rows.Next() {
		var v victim
		if scanErr := rows.Scan(&v.id, &v.playbackID); scanErr != nil {
			return nil, fmt.Errorf("scan mist_native row: %w", scanErr)
		}
		if _, kept := desired[strings.ToLower(v.playbackID)]; kept {
			continue
		}
		victims = append(victims, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(victims) == 0 {
		return nil, nil
	}
	const deleteSQL = `DELETE FROM commodore.streams WHERE id = $1::uuid`
	out := make([]string, 0, len(victims))
	for _, v := range victims {
		if _, err := exec.ExecContext(ctx, deleteSQL, v.id); err != nil {
			return nil, fmt.Errorf("delete stream %s: %w", v.id, err)
		}
		out = append(out, v.playbackID)
	}
	return out, nil
}

func validateMistNativeShape(m MistNativeStream) error {
	if m.PlaybackID == "" {
		return errors.New("playback_id required")
	}
	if m.OwnerTenant.Ref == "" {
		return fmt.Errorf("mist_native_stream %q: owner_tenant.ref required", m.PlaybackID)
	}
	// mist_native streams are operator-tenant-only: customer-owned managed
	// streams would bypass the free-tier-load and per-tenant stream-cap
	// gates that PUSH_REWRITE enforces. Defense in depth — the CLI render
	// layer rejects the same shape, but non-CLI callers (hand-written or
	// stale rendered files) must hit the same gate here so the row never
	// lands in DB violating the invariant.
	if !isSystemTenantRef(m.OwnerTenant.Ref) {
		return fmt.Errorf("mist_native_stream %q: owner_tenant must be the operator/system tenant (got %q)", m.PlaybackID, m.OwnerTenant.Ref)
	}
	if m.Title == "" {
		return fmt.Errorf("mist_native_stream %q: title required", m.PlaybackID)
	}
	if m.Source == "" {
		return fmt.Errorf("mist_native_stream %q: source required", m.PlaybackID)
	}
	switch m.SourceKind {
	case "exec":
		if !strings.HasPrefix(m.Source, "ts-exec:") {
			return fmt.Errorf("mist_native_stream %q: source_kind=exec requires source to start with 'ts-exec:'", m.PlaybackID)
		}
	case "file":
		if !strings.HasPrefix(m.Source, "file://") && !strings.HasPrefix(m.Source, "/") {
			return fmt.Errorf("mist_native_stream %q: source_kind=file requires source to start with 'file://' or '/'", m.PlaybackID)
		}
	case "playlist":
		if !strings.HasPrefix(m.Source, "playlist:") &&
			!strings.HasSuffix(m.Source, ".pls") &&
			!strings.HasSuffix(m.Source, ".m3u") &&
			!strings.HasSuffix(m.Source, ".m3u8") {
			return fmt.Errorf("mist_native_stream %q: source_kind=playlist requires source prefix 'playlist:' or a .pls/.m3u/.m3u8 path", m.PlaybackID)
		}
	default:
		return fmt.Errorf("mist_native_stream %q: source_kind %q is not supported (file | playlist | exec)", m.PlaybackID, m.SourceKind)
	}
	if m.PlacementCount < 0 {
		return fmt.Errorf("mist_native_stream %q: placement_count must be >= 0 (0 ⇒ default 1), got %d", m.PlaybackID, m.PlacementCount)
	}
	// allowed_cluster_ids currently names exactly one source cluster
	// Foghorn elects within. Federation still handles cross-cluster viewer
	// routing from that active source, but there is no cross-cluster source
	// election authority.
	if len(m.AllowedClusterIDs) == 0 {
		return fmt.Errorf("mist_native_stream %q: allowed_cluster_ids must contain at least one cluster", m.PlaybackID)
	}
	for i, id := range m.AllowedClusterIDs {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("mist_native_stream %q: allowed_cluster_ids[%d] must be non-empty", m.PlaybackID, i)
		}
	}
	allowedClusters := normalizeAllowedClusterIDs(m.AllowedClusterIDs)
	if len(allowedClusters) != 1 {
		return fmt.Errorf("mist_native_stream %q: allowed_cluster_ids currently supports exactly one source cluster (got %d); cross-cluster source election is not implemented", m.PlaybackID, len(allowedClusters))
	}
	return nil
}

// isSystemTenantRef matches the same alias paths the CLI render layer
// accepts as the operator/system tenant. Kept in sync with
// cli/pkg/bootstrap.isSystemTenantRef — the alias literal is the
// cross-service contract, not the Go function.
func isSystemTenantRef(ref string) bool {
	r := strings.TrimSpace(ref)
	return r == "quartermaster.system_tenant" ||
		r == "quartermaster.tenants."+SystemTenantAlias
}

func reconcileMistNativeStream(ctx context.Context, exec DBTX, tenantID, alias string, m MistNativeStream) (string, error) {
	const probeSQL = `
		SELECT s.id::text,
		       s.title,
		       COALESCE(s.description, ''),
		       s.ingest_mode,
		       s.always_on,
		       s.is_recording_enabled,
		       mn.source_spec,
		       mn.source_kind,
		       mn.placement_count,
		       COALESCE(mn.allowed_cluster_ids, '{}'),
		       COALESCE(mn.local_asset_paths::text, '[]'),
		       COALESCE(spc.processes_live::text, '')
		FROM commodore.streams s
		LEFT JOIN commodore.stream_mist_sources mn ON mn.stream_id = s.id
		LEFT JOIN commodore.stream_processing_config spc ON spc.stream_id = s.id
		WHERE s.tenant_id = $1::uuid AND lower(s.playback_id::text) = lower($2)`

	var (
		streamID                                 string
		curTitle, curDesc, curMode               string
		curAlwaysOn, curRecording                bool
		curSourceSpec, curSourceKind             sql.NullString
		curPlacementCount                        sql.NullInt32
		curAllowedClusters                       pq.StringArray
		curLocalAssetsJSON, curProcessesLiveJSON string
	)
	err := exec.QueryRowContext(ctx, probeSQL, tenantID, m.PlaybackID).Scan(
		&streamID, &curTitle, &curDesc, &curMode, &curAlwaysOn, &curRecording,
		&curSourceSpec, &curSourceKind, &curPlacementCount,
		&curAllowedClusters, &curLocalAssetsJSON, &curProcessesLiveJSON,
	)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return createMistNativeStream(ctx, exec, tenantID, alias, m)
	case err != nil:
		return "", fmt.Errorf("probe stream: %w", err)
	}

	if curMode != "mist_native" {
		return "", fmt.Errorf("stream %q already exists with ingest_mode=%q; refusing to convert", m.PlaybackID, curMode)
	}

	placement := m.PlacementCount
	if placement == 0 {
		placement = 1
	}

	wantLocalAssetsJSON, err := encodeLocalAssetPaths(m.LocalAssets)
	if err != nil {
		return "", fmt.Errorf("encode local_assets: %w", err)
	}
	wantProcessesLiveJSON, err := encodeProcessPolicy(m.ProcessPolicy)
	if err != nil {
		return "", fmt.Errorf("encode process_policy: %w", err)
	}

	streamFieldsEq := curTitle == m.Title &&
		curDesc == m.Description &&
		curAlwaysOn == m.AlwaysOn &&
		curRecording == m.IsRecordingEnabled
	curAllowed := []string(curAllowedClusters)
	mistFieldsEq := curSourceSpec.Valid && curSourceSpec.String == m.Source &&
		curSourceKind.Valid && curSourceKind.String == m.SourceKind &&
		curPlacementCount.Valid && int(curPlacementCount.Int32) == placement &&
		slices.Equal(curAllowed, m.AllowedClusterIDs) &&
		jsonStringsEqual(curLocalAssetsJSON, wantLocalAssetsJSON)
	processFieldsEq := jsonStringsEqual(curProcessesLiveJSON, wantProcessesLiveJSON)

	if streamFieldsEq && mistFieldsEq && processFieldsEq {
		return "noop", nil
	}

	if !streamFieldsEq {
		const updateStreamSQL = `
			UPDATE commodore.streams
			SET title = $2,
			    description = $3,
			    always_on = $4,
			    is_recording_enabled = $5,
			    updated_at = NOW()
			WHERE id = $1::uuid`
		if _, err := exec.ExecContext(ctx, updateStreamSQL,
			streamID, m.Title, m.Description, m.AlwaysOn, m.IsRecordingEnabled,
		); err != nil {
			return "", fmt.Errorf("update stream: %w", err)
		}
	}
	if !mistFieldsEq {
		const upsertMistSQL = `
			INSERT INTO commodore.stream_mist_sources
				(stream_id, source_spec, source_kind, placement_count,
				 allowed_cluster_ids, local_asset_paths, created_at, updated_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb, NOW(), NOW())
			ON CONFLICT (stream_id) DO UPDATE
				SET source_spec         = EXCLUDED.source_spec,
				    source_kind         = EXCLUDED.source_kind,
				    placement_count     = EXCLUDED.placement_count,
				    allowed_cluster_ids = EXCLUDED.allowed_cluster_ids,
				    local_asset_paths   = EXCLUDED.local_asset_paths,
				    updated_at          = NOW()`
		if _, err := exec.ExecContext(ctx, upsertMistSQL,
			streamID, m.Source, m.SourceKind, placement,
			pq.Array(m.AllowedClusterIDs), wantLocalAssetsJSON,
		); err != nil {
			return "", fmt.Errorf("upsert stream_mist_sources: %w", err)
		}
	}
	if !processFieldsEq {
		if err := upsertStreamProcessingConfig(ctx, exec, streamID, wantProcessesLiveJSON); err != nil {
			return "", err
		}
	}
	return "updated", nil
}

func createMistNativeStream(ctx context.Context, exec DBTX, tenantID, alias string, m MistNativeStream) (string, error) {
	const ownerSQL = `
		SELECT id::text FROM commodore.users
		WHERE tenant_id = $1::uuid AND role = 'owner'
		ORDER BY created_at LIMIT 1`
	var ownerID string
	switch err := exec.QueryRowContext(ctx, ownerSQL, tenantID).Scan(&ownerID); {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("tenant %s has no owner user — provision owners before mist_native streams", alias)
	case err != nil:
		return "", fmt.Errorf("lookup owner user: %w", err)
	}

	// stream_key + internal_name follow the same convention as pull streams:
	// stream_key is unused (Mist-native streams have no push ingest), but the
	// column is NOT NULL so we derive a stable placeholder from the playback_id.
	const insertStreamSQL = `
		INSERT INTO commodore.streams
			(id, tenant_id, user_id, stream_key, playback_id, internal_name,
			 title, description, ingest_mode, always_on, is_recording_enabled,
			 created_at, updated_at)
		VALUES (gen_random_uuid(), $1::uuid, $5::uuid,
		        'mistnative-' || $2, $2,
		        replace(gen_random_uuid()::text, '-', ''),
		        $3, $4, 'mist_native', $6, $7, NOW(), NOW())
		RETURNING id::text`
	var streamID string
	if err := exec.QueryRowContext(ctx, insertStreamSQL,
		tenantID, m.PlaybackID, m.Title, m.Description, ownerID, m.AlwaysOn, m.IsRecordingEnabled,
	).Scan(&streamID); err != nil {
		return "", fmt.Errorf("insert stream: %w", err)
	}

	placement := m.PlacementCount
	if placement == 0 {
		placement = 1
	}
	localAssetsJSON, err := encodeLocalAssetPaths(m.LocalAssets)
	if err != nil {
		return "", fmt.Errorf("encode local_assets: %w", err)
	}
	const insertMistSQL = `
		INSERT INTO commodore.stream_mist_sources
			(stream_id, source_spec, source_kind, placement_count,
			 allowed_cluster_ids, local_asset_paths, created_at, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6::jsonb, NOW(), NOW())`
	if _, err := exec.ExecContext(ctx, insertMistSQL,
		streamID, m.Source, m.SourceKind, placement,
		pq.Array(m.AllowedClusterIDs), localAssetsJSON,
	); err != nil {
		return "", fmt.Errorf("insert stream_mist_sources: %w", err)
	}

	if m.ProcessPolicy != nil {
		processPolicyJSON, err := encodeProcessPolicy(m.ProcessPolicy)
		if err != nil {
			return "", fmt.Errorf("encode process_policy: %w", err)
		}
		if err := upsertStreamProcessingConfig(ctx, exec, streamID, processPolicyJSON); err != nil {
			return "", err
		}
	}
	return "created", nil
}

func upsertStreamProcessingConfig(ctx context.Context, exec DBTX, streamID, processesLiveJSON string) error {
	if processesLiveJSON == "" {
		// Clearing the per-stream override: delete the row so resolveProcessesJSON
		// falls through to the tenant / tier layers.
		const deleteSQL = `DELETE FROM commodore.stream_processing_config WHERE stream_id = $1::uuid`
		if _, err := exec.ExecContext(ctx, deleteSQL, streamID); err != nil {
			return fmt.Errorf("delete stream_processing_config: %w", err)
		}
		return nil
	}
	const upsertSQL = `
		INSERT INTO commodore.stream_processing_config (stream_id, processes_live, updated_at)
		VALUES ($1::uuid, $2::jsonb, NOW())
		ON CONFLICT (stream_id) DO UPDATE
			SET processes_live = EXCLUDED.processes_live,
			    updated_at     = NOW()`
	if _, err := exec.ExecContext(ctx, upsertSQL, streamID, processesLiveJSON); err != nil {
		return fmt.Errorf("upsert stream_processing_config: %w", err)
	}
	return nil
}

func encodeLocalAssetPaths(assets []MistNativeStreamAsset) (string, error) {
	if len(assets) == 0 {
		return "[]", nil
	}
	buf, err := json.Marshal(assets)
	if err != nil {
		return "", err
	}
	return string(buf), nil
}

func encodeProcessPolicy(policy any) (string, error) {
	if policy == nil {
		return "", nil
	}
	if err := validateProcessPolicyShape(policy); err != nil {
		return "", err
	}
	buf, err := json.Marshal(policy)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(string(buf))
	// Treat JSON null / empty container as "no override" so authors can clear
	// the policy by emitting an empty value rather than dropping the field.
	if out == "" || out == "null" || out == "{}" || out == "[]" {
		return "", nil
	}
	// Enforce the Mist process-config shape invariants (AV option names, and a
	// Livepeer process must request at least one rendition) on override policies
	// too — not only the billing catalog. This is the single runtime gate before
	// the policy is stamped into stream_processing_config and served verbatim to
	// MistServer, so an operator override can't slip a no-rendition Livepeer
	// config past the validators downstream.
	if err := mist.ValidateProcessConfigShape(out); err != nil {
		return "", err
	}
	return out, nil
}

// validateProcessPolicyShape rejects process_policy that doesn't match
// the Mist process config contract (a JSON array of process objects, each
// with at least a "process" key). The reconciler stamps this verbatim
// into commodore.stream_processing_config, which STREAM_PROCESS returns
// directly to MistServer; a non-array or object-shaped policy would
// silently disable processing on the stream (Mist ignores unknown shapes).
func validateProcessPolicyShape(policy any) error {
	arr, ok := policy.([]any)
	if !ok {
		return fmt.Errorf("process_policy must be a list of Mist process objects (e.g. [{process: Thumbs, ...}]); got %T", policy)
	}
	for i, entry := range arr {
		obj, ok := entry.(map[string]any)
		if !ok {
			return fmt.Errorf("process_policy[%d]: each entry must be a Mist process object with a 'process' key; got %T", i, entry)
		}
		proc, ok := obj["process"]
		if !ok {
			return fmt.Errorf("process_policy[%d]: missing required 'process' key", i)
		}
		s, ok := proc.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return fmt.Errorf("process_policy[%d]: 'process' must be a non-empty string (Mist process name, e.g. Thumbs, AV)", i)
		}
	}
	return nil
}

func jsonStringsEqual(a, b string) bool {
	ta := strings.TrimSpace(a)
	tb := strings.TrimSpace(b)
	if ta == tb {
		return true
	}
	var va, vb any
	if err := json.Unmarshal([]byte(orEmptyJSONNull(ta)), &va); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(orEmptyJSONNull(tb)), &vb); err != nil {
		return false
	}
	bufA, errA := json.Marshal(va)
	bufB, errB := json.Marshal(vb)
	if errA != nil || errB != nil {
		return false
	}
	return string(bufA) == string(bufB)
}

func orEmptyJSONNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}
