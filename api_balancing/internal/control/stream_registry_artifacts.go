package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ArtifactKind classifies an artifact's lifecycle phase / playback surface.
type ArtifactKind int

const (
	// ArtifactKindVOD is a finalized VOD asset (uploaded file, finished clip,
	// or finalized DVR chapter). Runtime name vod+<internal_name>.
	ArtifactKindVOD ArtifactKind = iota + 1
	// ArtifactKindDVR is the rolling-DVR playback surface for an
	// actively-recording stream. Runtime name dvr+<internal_name>.
	ArtifactKindDVR
	// ArtifactKindClip is a clip artifact. Until it's finalized as a VOD
	// row, the routing path is the source pull; after finalize it routes
	// as vod+<internal_name>. Foghorn currently uses artifact_type='clip'
	// in foghorn.artifacts for in-flight + finalized clip artifacts; the
	// VOD-style runtime name applies once internal_name is set and the
	// row is ready.
	ArtifactKindClip
	// ArtifactKindProcessing is an in-flight processing job. Runtime name
	// processing+<artifact_hash>. Becomes ArtifactKindVOD (or DVR/Clip)
	// after the finalize hook fires.
	ArtifactKindProcessing
)

func (k ArtifactKind) String() string {
	switch k {
	case ArtifactKindVOD:
		return "vod"
	case ArtifactKindDVR:
		return "dvr"
	case ArtifactKindClip:
		return "clip"
	case ArtifactKindProcessing:
		return "processing"
	default:
		return "invalid"
	}
}

// ArtifactEntry is the registry's canonical view of an artifact (VOD, DVR,
// Clip, or in-flight Processing job).
type ArtifactEntry struct {
	Kind            ArtifactKind
	ArtifactHash    string
	InternalName    string // artifact internal_name; empty for processing-only jobs
	StreamID        string // parent source stream
	StreamInternal  string // parent source internal_name
	TenantID        string
	Status          string
	Format          string
	RuntimeName     string // vod+/dvr+/processing+ with appropriate token
	OriginClusterID string
	StorageCluster  string
	HasThumbnails   bool
	HydratedAt      time.Time
	// HydrationSrc is one of "sql_artifact", "sql_processing_job",
	// "federation_ad", or "callback_finalize". Used by diagnostics.
	HydrationSrc string
}

// ErrUnknownArtifact mirrors ErrUnknownStream for artifact lookups.
var ErrUnknownArtifact = errors.New("stream_registry: unknown artifact")

// artifactDB is the minimal SQL surface the registry needs. Kept as an
// interface so tests can substitute an in-memory fake.
type artifactDB interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// artifactStore holds cached artifact entries alongside the source-stream
// cache on StreamRegistry. Kept as a sibling map so source/artifact lookups
// don't collide on keys (artifact_hash and stream_id share the UUID
// namespace).
type artifactStore struct {
	mu              sync.RWMutex
	byHash          map[string]*cachedArtifact // artifact_hash
	byInternal      map[string]*cachedArtifact // artifact internal_name (vod/dvr/clip)
	byProcessingKey map[string]*cachedArtifact // processing+<hash> → entry while in-flight

	// finalizeHooks fire whenever a processing job transitions to a
	// finalized artifact, allowing the registry to evict its processing
	// entry and re-hydrate the artifact row.
	finalizeHooks []func(artifactHash string)
}

type cachedArtifact struct {
	entry  ArtifactEntry
	cached time.Time
}

func newArtifactStore() *artifactStore {
	return &artifactStore{
		byHash:          make(map[string]*cachedArtifact),
		byInternal:      make(map[string]*cachedArtifact),
		byProcessingKey: make(map[string]*cachedArtifact),
	}
}

// ResolveArtifactByHash returns the ArtifactEntry for a given artifact
// hash, consulting the cache first, then SQL. The hash bridges processing
// and finalized artifacts — a job in flight has a foghorn.processing_jobs
// row keyed on this hash; once finalize fires, foghorn.artifacts carries
// the same hash. Both are checked.
func (r *StreamRegistry) ResolveArtifactByHash(ctx context.Context, db artifactDB, artifactHash string) (ArtifactEntry, error) {
	artifactHash = strings.TrimSpace(artifactHash)
	if artifactHash == "" {
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	if e, ok := r.lookupArtifact(r.artifacts.byHash, artifactHash); ok {
		return e, nil
	}
	if db == nil {
		r.recordMiss(ctx, "artifact_hash", artifactHash)
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	entry, err := r.hydrateArtifactFromSQL(ctx, db, "hash", artifactHash)
	if err != nil {
		if errors.Is(err, ErrUnknownArtifact) {
			r.recordMiss(ctx, "artifact_hash", artifactHash)
		}
		return ArtifactEntry{}, err
	}
	return entry, nil
}

// ResolveArtifactByInternalName resolves vod+/dvr+/clip artifacts by their
// artifact internal_name (the token after the prefix in vod+<internal>).
func (r *StreamRegistry) ResolveArtifactByInternalName(ctx context.Context, db artifactDB, internalName string) (ArtifactEntry, error) {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	if e, ok := r.lookupArtifact(r.artifacts.byInternal, internalName); ok {
		return e, nil
	}
	if db == nil {
		r.recordMiss(ctx, "artifact_internal_name", internalName)
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	entry, err := r.hydrateArtifactFromSQL(ctx, db, "internal", internalName)
	if err != nil {
		if errors.Is(err, ErrUnknownArtifact) {
			r.recordMiss(ctx, "artifact_internal_name", internalName)
		}
		return ArtifactEntry{}, err
	}
	return entry, nil
}

// ResolveByProcessingHash bridges the processing→artifact identity
// transition. A processing+<hash> Mist runtime name refers to either an
// in-flight processing_jobs row (Kind=Processing) or, if the job has
// already finalized, the resulting foghorn.artifacts row of the matching
// hash (Kind=VOD/DVR/Clip).
func (r *StreamRegistry) ResolveByProcessingHash(ctx context.Context, db artifactDB, artifactHash string) (ArtifactEntry, error) {
	artifactHash = strings.TrimSpace(artifactHash)
	if artifactHash == "" {
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	// Finalized artifact wins over in-flight processing job — once the
	// VOD/DVR row exists, that's the authoritative entry. ResolveByHash
	// already prefers artifacts.
	if e, ok := r.lookupArtifact(r.artifacts.byHash, artifactHash); ok && e.Kind != ArtifactKindProcessing {
		return e, nil
	}
	if e, ok := r.lookupArtifact(r.artifacts.byProcessingKey, artifactHash); ok {
		return e, nil
	}
	if db == nil {
		r.recordMiss(ctx, "processing_hash", artifactHash)
		return ArtifactEntry{}, ErrUnknownArtifact
	}
	// Prefer artifacts row (finalized). If absent, fall back to job row.
	if entry, err := r.hydrateArtifactFromSQL(ctx, db, "hash", artifactHash); err == nil {
		return entry, nil
	} else if !errors.Is(err, ErrUnknownArtifact) {
		return ArtifactEntry{}, err
	}
	entry, err := r.hydrateProcessingFromSQL(ctx, db, artifactHash)
	if err != nil {
		if errors.Is(err, ErrUnknownArtifact) {
			r.recordMiss(ctx, "processing_hash", artifactHash)
		}
		return ArtifactEntry{}, err
	}
	return entry, nil
}

// OnProcessingFinalize is invoked from the existing finalize-hook code path
// when a processing job transitions to a finalized artifact. It evicts any
// cached processing entry so the next lookup re-hydrates against
// foghorn.artifacts. Finalize hooks registered via RegisterFinalizeHook
// fire as well so downstream consumers (Decklog, Chandler invalidation)
// can react.
func (r *StreamRegistry) OnProcessingFinalize(artifactHash string) {
	r.artifacts.mu.Lock()
	delete(r.artifacts.byProcessingKey, artifactHash)
	delete(r.artifacts.byHash, artifactHash)
	hooks := slices.Clone(r.artifacts.finalizeHooks)
	r.artifacts.mu.Unlock()
	r.publishDeleteArtifact(artifactHash)
	for _, h := range hooks {
		h(artifactHash)
	}
}

// RegisterFinalizeHook adds a callback fired after OnProcessingFinalize
// evicts the cache. Use for cross-system invalidation (Chandler etc.).
func (r *StreamRegistry) RegisterFinalizeHook(fn func(artifactHash string)) {
	r.artifacts.mu.Lock()
	defer r.artifacts.mu.Unlock()
	r.artifacts.finalizeHooks = append(r.artifacts.finalizeHooks, fn)
}

// UpsertLocalSource ensures the registry has an entry for a locally-
// owned source stream, hydrated with identity fields the caller already
// resolved (typically from a Commodore PUSH_REWRITE or ResolveIdentifier
// response). Avoids a redundant ResolveStreamContext round-trip when the
// caller already has the data. Safe to call repeatedly — merges into an
// existing entry's local Location rather than overwriting peer Locations.
//
// IngestMode/RuntimeName are derived from the entry's IngestMode field;
// callers that don't know the ingest mode can pass zero — RuntimeName
// will be empty and routing decisions will fall through to other sources.
func (r *StreamRegistry) UpsertLocalSource(entry StreamEntry) {
	if entry.InternalName == "" {
		return
	}
	if entry.OriginClusterID == "" {
		entry.OriginClusterID = r.clusterID
	}
	if entry.IngestMode != 0 && entry.RuntimeName == "" {
		entry.RuntimeName = RuntimeNameFor(entry.IngestMode, entry.InternalName)
	}
	if entry.HydratedAt.IsZero() {
		entry.HydratedAt = time.Now()
	}

	var snapshot StreamEntry
	r.mu.Lock()
	ce, ok := r.byInt[entry.InternalName]
	if !ok {
		if entry.Locations == nil {
			entry.Locations = make(map[string]Location)
		}
		ce = &cachedEntry{entry: entry, cached: time.Now()}
		r.byInt[entry.InternalName] = ce
		if entry.StreamID != "" {
			r.byID[entry.StreamID] = ce
		}
		if entry.PlaybackID != "" {
			r.byPlay[entry.PlaybackID] = ce
		}
		snapshot = ce.entry
	} else {
		// Merge identity. Caller's data wins when previous fields are
		// empty so a fresh PUSH_REWRITE can fill PlaybackID a prior
		// internal-name-only resolution missed.
		if entry.StreamID != "" && ce.entry.StreamID == "" {
			ce.entry.StreamID = entry.StreamID
			r.byID[entry.StreamID] = ce
		}
		if entry.TenantID != "" {
			ce.entry.TenantID = entry.TenantID
		}
		if entry.PlaybackID != "" && ce.entry.PlaybackID == "" {
			ce.entry.PlaybackID = entry.PlaybackID
			r.byPlay[entry.PlaybackID] = ce
		}
		if entry.IngestMode != 0 {
			ce.entry.IngestMode = entry.IngestMode
			ce.entry.RuntimeName = entry.RuntimeName
		}
		if entry.OriginClusterID != "" {
			ce.entry.OriginClusterID = entry.OriginClusterID
		}
		ce.cached = time.Now()
		snapshot = ce.entry
	}
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// UpsertFederatedSource records or updates a peer-owned source stream
// advertised via StreamAdvertisement. peerClusterID identifies the peer;
// entry carries identity fields (TenantID, PlaybackID, InternalName); and
// location carries per-cluster details (IsLiveNow, EdgeCandidates,
// AdTimestamp). The registry merges into Locations[peerClusterID] so
// multiple peers can advertise the same stream concurrently without
// overwriting each other.
//
// Called from federation/server.go handleStreamAdvertisement on every ad.
func (r *StreamRegistry) UpsertFederatedSource(peerClusterID string, entry StreamEntry, location Location) {
	if peerClusterID == "" || entry.InternalName == "" {
		return
	}
	// Withdrawal: an ad with IsLiveNow=false from this peer means the
	// peer no longer serves the stream. Drop the peer's Location; if no
	// Locations remain, the entry itself goes (clearing PlaybackID +
	// StreamID reverse indexes).
	if !location.IsLiveNow {
		r.withdrawFederatedSource(peerClusterID, entry.InternalName)
		return
	}
	// Default missing origin to the advertising peer. Federation handlers
	// already apply this fallback before calling, but covering it here
	// keeps direct callers and tests consistent.
	if entry.OriginClusterID == "" {
		entry.OriginClusterID = peerClusterID
	}
	location.ClusterID = peerClusterID
	// IsOrigin marks whether this peer IS the stream's origin cluster, not
	// merely a relay. Multi-hop federation (A originates, B replicates and
	// re-advertises, C receives B's ad) needs C to record B as a relay,
	// not an origin, so origin-pull cascades terminate at A.
	location.IsOrigin = entry.OriginClusterID == peerClusterID
	location.UpdatedAt = time.Now()

	var snapshot StreamEntry
	r.mu.Lock()
	ce, ok := r.byInt[entry.InternalName]
	if !ok {
		if entry.HydratedAt.IsZero() {
			entry.HydratedAt = time.Now()
		}
		if entry.Locations == nil {
			entry.Locations = make(map[string]Location)
		}
		entry.Locations[peerClusterID] = location
		ce = &cachedEntry{entry: entry, cached: time.Now()}
		r.byInt[entry.InternalName] = ce
		if entry.StreamID != "" {
			r.byID[entry.StreamID] = ce
		}
		if entry.PlaybackID != "" {
			r.byPlay[entry.PlaybackID] = ce
		}
		snapshot = ce.entry
	} else {
		if entry.PlaybackID != "" && ce.entry.PlaybackID == "" {
			ce.entry.PlaybackID = entry.PlaybackID
			r.byPlay[entry.PlaybackID] = ce
		}
		if entry.TenantID != "" && ce.entry.TenantID == "" {
			ce.entry.TenantID = entry.TenantID
		}
		if ce.entry.OriginClusterID == "" {
			ce.entry.OriginClusterID = entry.OriginClusterID
		}
		if ce.entry.Locations == nil {
			ce.entry.Locations = make(map[string]Location)
		}
		ce.entry.Locations[peerClusterID] = location
		ce.cached = time.Now()
		snapshot = ce.entry
	}
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// withdrawFederatedSource removes a peer's Location from an entry. If
// the entry has no remaining Locations after removal, drop it entirely
// so the PlaybackID/StreamID reverse indexes stop resolving.
func (r *StreamRegistry) withdrawFederatedSource(peerClusterID, internalName string) {
	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	if !ok {
		r.mu.Unlock()
		return
	}
	if ce.entry.Locations != nil {
		delete(ce.entry.Locations, peerClusterID)
	}
	if len(ce.entry.Locations) == 0 {
		// Drop the entry. Capture identity fields before deletion so the
		// pubsub delete carries the keys peer instances need to evict.
		evicted := ce.entry
		delete(r.byInt, internalName)
		if evicted.StreamID != "" {
			delete(r.byID, evicted.StreamID)
		}
		if evicted.PlaybackID != "" {
			delete(r.byPlay, evicted.PlaybackID)
		}
		r.mu.Unlock()
		r.publishDeleteSource(internalName)
		return
	}
	ce.cached = time.Now()
	snapshot := ce.entry
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// InvalidateArtifact drops cached artifact entries by any of (hash,
// internal_name). Called when Foghorn writes a new artifact row so the
// next lookup re-reads the row.
func (r *StreamRegistry) InvalidateArtifact(artifactHash, internalName string) {
	r.artifacts.mu.Lock()
	if artifactHash != "" {
		delete(r.artifacts.byHash, artifactHash)
		delete(r.artifacts.byProcessingKey, artifactHash)
	}
	if internalName != "" {
		delete(r.artifacts.byInternal, internalName)
	}
	r.artifacts.mu.Unlock()
	if artifactHash != "" {
		r.publishDeleteArtifact(artifactHash)
	}
}

// SnapshotArtifacts returns every cached artifact entry, deduplicated.
// Used by the /debug/stream-registry diagnostics endpoint.
func (r *StreamRegistry) SnapshotArtifacts() []ArtifactEntry {
	r.artifacts.mu.RLock()
	defer r.artifacts.mu.RUnlock()
	seen := make(map[string]struct{}, len(r.artifacts.byHash))
	out := make([]ArtifactEntry, 0, len(r.artifacts.byHash))
	for _, ce := range r.artifacts.byHash {
		if _, dup := seen[ce.entry.ArtifactHash]; dup {
			continue
		}
		seen[ce.entry.ArtifactHash] = struct{}{}
		out = append(out, ce.entry)
	}
	return out
}

func (r *StreamRegistry) lookupArtifact(m map[string]*cachedArtifact, key string) (ArtifactEntry, bool) {
	r.artifacts.mu.RLock()
	defer r.artifacts.mu.RUnlock()
	ce, ok := m[key]
	if !ok {
		return ArtifactEntry{}, false
	}
	if time.Since(ce.cached) > r.ttl {
		return ArtifactEntry{}, false
	}
	return ce.entry, true
}

func (r *StreamRegistry) storeArtifact(e ArtifactEntry) {
	r.artifacts.mu.Lock()
	ce := &cachedArtifact{entry: e, cached: time.Now()}
	if e.ArtifactHash != "" {
		r.artifacts.byHash[e.ArtifactHash] = ce
	}
	if e.InternalName != "" {
		r.artifacts.byInternal[e.InternalName] = ce
	}
	if e.Kind == ArtifactKindProcessing && e.ArtifactHash != "" {
		r.artifacts.byProcessingKey[e.ArtifactHash] = ce
	}
	r.artifacts.mu.Unlock()
	r.publishUpsertArtifact(e)
}

func (r *StreamRegistry) hydrateArtifactFromSQL(ctx context.Context, db artifactDB, lookupBy, key string) (ArtifactEntry, error) {
	var (
		artifactHash   string
		artifactType   string
		internalName   sql.NullString
		streamInternal sql.NullString
		streamID       sql.NullString
		tenantID       sql.NullString
		status         sql.NullString
		format         sql.NullString
		originCluster  sql.NullString
		storageCluster sql.NullString
		hasThumbnails  bool
	)
	query := `
		SELECT artifact_hash, artifact_type,
		       COALESCE(internal_name, ''), COALESCE(stream_internal_name, ''),
		       COALESCE(stream_id::text, ''), COALESCE(tenant_id::text, ''),
		       COALESCE(status, ''), COALESCE(format, ''),
		       COALESCE(origin_cluster_id, ''), COALESCE(storage_cluster_id, ''),
		       COALESCE(has_thumbnails, false)
		FROM foghorn.artifacts
		WHERE %s`
	var where string
	switch lookupBy {
	case "hash":
		where = "artifact_hash = $1"
	case "internal":
		where = "internal_name = $1"
	default:
		return ArtifactEntry{}, fmt.Errorf("stream_registry: unsupported artifact lookup %q", lookupBy)
	}
	err := db.QueryRowContext(ctx, fmt.Sprintf(query, where), key).Scan(
		&artifactHash, &artifactType, &internalName, &streamInternal,
		&streamID, &tenantID, &status, &format,
		&originCluster, &storageCluster, &hasThumbnails,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactEntry{}, ErrUnknownArtifact
		}
		return ArtifactEntry{}, fmt.Errorf("stream_registry: artifact sql: %w", err)
	}
	kind := artifactKindFromType(artifactType)
	entry := ArtifactEntry{
		Kind:            kind,
		ArtifactHash:    artifactHash,
		InternalName:    internalName.String,
		StreamID:        streamID.String,
		StreamInternal:  streamInternal.String,
		TenantID:        tenantID.String,
		Status:          status.String,
		Format:          format.String,
		OriginClusterID: originCluster.String,
		StorageCluster:  storageCluster.String,
		HasThumbnails:   hasThumbnails,
		RuntimeName:     artifactRuntimeName(kind, internalName.String, artifactHash),
		HydratedAt:      time.Now(),
		HydrationSrc:    "sql_artifact",
	}
	r.storeArtifact(entry)
	return entry, nil
}

func (r *StreamRegistry) hydrateProcessingFromSQL(ctx context.Context, db artifactDB, artifactHash string) (ArtifactEntry, error) {
	var (
		jobID    string
		tenantID sql.NullString
		status   sql.NullString
	)
	err := db.QueryRowContext(ctx, `
		SELECT job_id::text, COALESCE(tenant_id::text, ''), COALESCE(status, '')
		FROM foghorn.processing_jobs
		WHERE artifact_hash = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, artifactHash).Scan(&jobID, &tenantID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArtifactEntry{}, ErrUnknownArtifact
		}
		return ArtifactEntry{}, fmt.Errorf("stream_registry: processing sql: %w", err)
	}
	entry := ArtifactEntry{
		Kind:         ArtifactKindProcessing,
		ArtifactHash: artifactHash,
		TenantID:     tenantID.String,
		Status:       status.String,
		RuntimeName:  "processing+" + artifactHash,
		HydratedAt:   time.Now(),
		HydrationSrc: "sql_processing_job",
	}
	r.storeArtifact(entry)
	return entry, nil
}

func artifactKindFromType(artifactType string) ArtifactKind {
	switch strings.ToLower(strings.TrimSpace(artifactType)) {
	case "vod":
		return ArtifactKindVOD
	case "dvr":
		return ArtifactKindDVR
	case "clip":
		return ArtifactKindClip
	default:
		return 0
	}
}

func artifactRuntimeName(kind ArtifactKind, internalName, hash string) string {
	switch kind {
	case ArtifactKindVOD, ArtifactKindClip:
		if internalName == "" {
			return ""
		}
		return "vod+" + internalName
	case ArtifactKindDVR:
		if internalName == "" {
			return ""
		}
		return "dvr+" + internalName
	case ArtifactKindProcessing:
		if hash == "" {
			return ""
		}
		return "processing+" + hash
	default:
		return ""
	}
}
