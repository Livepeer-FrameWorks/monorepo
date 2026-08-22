package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
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
	queries := commodoredb.New(exec)
	streams, err := queries.ListBootstrapMistNativeStreams(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list mist_native streams: %w", err)
	}
	var victims []commodoredb.ListBootstrapMistNativeStreamsRow
	for _, stream := range streams {
		if _, kept := desired[strings.ToLower(stream.PlaybackID)]; kept {
			continue
		}
		victims = append(victims, stream)
	}

	if len(victims) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(victims))
	for _, v := range victims {
		if err := queries.DeleteBootstrapStream(ctx, v.StreamID); err != nil {
			return nil, fmt.Errorf("delete stream %s: %w", v.StreamID, err)
		}
		out = append(out, v.PlaybackID)
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

// monitoringNullBool maps the rendered per-stream Monitoring value to the
// nullable commodore.streams.monitoring_enabled column.
func monitoringNullBool(monitoring string) (sql.NullBool, error) {
	switch strings.ToLower(strings.TrimSpace(monitoring)) {
	case "on":
		return sql.NullBool{Bool: true, Valid: true}, nil
	case "off":
		return sql.NullBool{Bool: false, Valid: true}, nil
	case "", "inherit":
		return sql.NullBool{}, nil
	default:
		return sql.NullBool{}, fmt.Errorf("monitoring must be one of inherit/on/off (got %q)", monitoring)
	}
}

func reconcileMistNativeStream(ctx context.Context, exec DBTX, tenantID, alias string, m MistNativeStream) (string, error) {
	queries := commodoredb.New(exec)
	current, err := queries.GetBootstrapMistNativeStream(ctx, commodoredb.GetBootstrapMistNativeStreamParams{
		TenantID: tenantID, PlaybackID: m.PlaybackID,
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return createMistNativeStream(ctx, queries, tenantID, alias, m)
	case err != nil:
		return "", fmt.Errorf("probe stream: %w", err)
	}

	if current.IngestMode != "mist_native" {
		return "", fmt.Errorf("stream %q already exists with ingest_mode=%q; refusing to convert", m.PlaybackID, current.IngestMode)
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

	wantMonitoring, err := monitoringNullBool(m.Monitoring)
	if err != nil {
		return "", err
	}
	streamFieldsEq := current.Title == m.Title &&
		current.Description == m.Description &&
		current.AlwaysOn == m.AlwaysOn &&
		current.IsRecordingEnabled.Valid && current.IsRecordingEnabled.Bool == m.IsRecordingEnabled &&
		current.MonitoringEnabled == wantMonitoring
	mistFieldsEq := current.SourceSpec.Valid && current.SourceSpec.String == m.Source &&
		current.SourceKind.Valid && current.SourceKind.String == m.SourceKind &&
		current.PlacementCount.Valid && int(current.PlacementCount.Int32) == placement &&
		slices.Equal(current.AllowedClusterIds, m.AllowedClusterIDs) &&
		jsonStringsEqual(current.LocalAssetPathsJson, wantLocalAssetsJSON)
	processFieldsEq := jsonStringsEqual(current.ProcessesLiveJson, wantProcessesLiveJSON)

	if streamFieldsEq && mistFieldsEq && processFieldsEq {
		return "noop", nil
	}

	if !streamFieldsEq {
		if err := queries.UpdateBootstrapMistNativeStream(ctx, commodoredb.UpdateBootstrapMistNativeStreamParams{
			Title: m.Title, Description: m.Description, AlwaysOn: m.AlwaysOn,
			IsRecordingEnabled: sql.NullBool{Bool: m.IsRecordingEnabled, Valid: true},
			MonitoringEnabled:  wantMonitoring, StreamID: current.StreamID,
		}); err != nil {
			return "", fmt.Errorf("update stream: %w", err)
		}
	}
	if !mistFieldsEq {
		if err := queries.UpsertBootstrapMistSource(ctx, commodoredb.UpsertBootstrapMistSourceParams{
			StreamID: current.StreamID, SourceSpec: m.Source, SourceKind: m.SourceKind,
			PlacementCount: int32(placement), AllowedClusterIds: m.AllowedClusterIDs,
			LocalAssetPaths: json.RawMessage(wantLocalAssetsJSON),
		}); err != nil {
			return "", fmt.Errorf("upsert stream_mist_sources: %w", err)
		}
	}
	if !processFieldsEq {
		if err := upsertStreamProcessingConfig(ctx, queries, current.StreamID, wantProcessesLiveJSON); err != nil {
			return "", err
		}
	}
	return "updated", nil
}

func createMistNativeStream(ctx context.Context, queries *commodoredb.Queries, tenantID, alias string, m MistNativeStream) (string, error) {
	monitoringEnabled, monErr := monitoringNullBool(m.Monitoring)
	if monErr != nil {
		return "", monErr
	}

	ownerID, err := queries.GetBootstrapOwnerUser(ctx, tenantID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("tenant %s has no owner user — provision owners before mist_native streams", alias)
	case err != nil:
		return "", fmt.Errorf("lookup owner user: %w", err)
	}

	// stream_key + internal_name follow the same convention as pull streams:
	// stream_key is unused (Mist-native streams have no push ingest), but the
	// column is NOT NULL so we derive a stable placeholder from the playback_id.
	streamID, err := queries.CreateBootstrapMistNativeStream(ctx, commodoredb.CreateBootstrapMistNativeStreamParams{
		TenantID: tenantID, OwnerID: ownerID, PlaybackID: m.PlaybackID,
		Title: m.Title, Description: m.Description, AlwaysOn: m.AlwaysOn,
		IsRecordingEnabled: sql.NullBool{Bool: m.IsRecordingEnabled, Valid: true},
		MonitoringEnabled:  monitoringEnabled,
	})
	if err != nil {
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
	if err := queries.CreateBootstrapMistSource(ctx, commodoredb.CreateBootstrapMistSourceParams{
		StreamID: streamID, SourceSpec: m.Source, SourceKind: m.SourceKind,
		PlacementCount: int32(placement), AllowedClusterIds: m.AllowedClusterIDs,
		LocalAssetPaths: json.RawMessage(localAssetsJSON),
	}); err != nil {
		return "", fmt.Errorf("insert stream_mist_sources: %w", err)
	}

	if m.ProcessPolicy != nil {
		processPolicyJSON, err := encodeProcessPolicy(m.ProcessPolicy)
		if err != nil {
			return "", fmt.Errorf("encode process_policy: %w", err)
		}
		if err := upsertStreamProcessingConfig(ctx, queries, streamID, processPolicyJSON); err != nil {
			return "", err
		}
	}
	return "created", nil
}

func upsertStreamProcessingConfig(ctx context.Context, queries *commodoredb.Queries, streamID, processesLiveJSON string) error {
	if processesLiveJSON == "" {
		// Clearing the per-stream override: delete the row so resolveProcessesJSON
		// falls through to the tenant / tier layers.
		if err := queries.DeleteBootstrapStreamProcessingConfig(ctx, streamID); err != nil {
			return fmt.Errorf("delete stream_processing_config: %w", err)
		}
		return nil
	}
	if err := queries.UpsertBootstrapStreamProcessingConfig(ctx, commodoredb.UpsertBootstrapStreamProcessingConfigParams{
		StreamID: streamID, ProcessesLive: json.RawMessage(processesLiveJSON),
	}); err != nil {
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
