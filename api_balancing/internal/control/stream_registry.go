package control

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// IngestMode is a typed source stream ingest mode. The zero value is
// invalid on purpose: callers must pass a real mode, so the compiler
// catches any site that would silently default.
type IngestMode int

const (
	// IngestPush — RTMP/WHIP/SRT encoder pushes into Mist. Runtime name
	// is live+<internal_name>.
	IngestPush IngestMode = iota + 1
	// IngestPull — Mist pulls from a configured upstream URI. Runtime
	// name is pull+<internal_name>.
	IngestPull
	// IngestMistNative — managed Mist-native source applied by Foghorn
	// (file/playlist/exec). Runtime name is the bare <internal_name>.
	IngestMistNative
)

// String returns the wire string used in commodore.ResolveStreamContextResponse.ingest_mode.
func (m IngestMode) String() string {
	switch m {
	case IngestPush:
		return "push"
	case IngestPull:
		return "pull"
	case IngestMistNative:
		return "mist_native"
	default:
		return "invalid"
	}
}

// IngestModeFromWire converts the commodore proto string to a typed
// IngestMode. Returns 0 and an error for unknown / empty input so callers
// fail closed instead of silently defaulting.
func IngestModeFromWire(s string) (IngestMode, error) {
	switch strings.TrimSpace(s) {
	case "push":
		return IngestPush, nil
	case "pull":
		return IngestPull, nil
	case "mist_native":
		return IngestMistNative, nil
	case "":
		return 0, fmt.Errorf("ingest_mode is empty")
	default:
		return 0, fmt.Errorf("unknown ingest_mode %q", s)
	}
}

// RuntimeNameFor returns the Mist runtime stream name for an ingest mode
// plus concrete source-stream internal_name. An invalid (zero) mode
// returns the empty string instead of guessing, so a caller bug surfaces
// as a refused routing decision rather than a mis-routed stream.
func RuntimeNameFor(mode IngestMode, internalName string) string {
	internalName = strings.TrimSpace(internalName)
	switch mode {
	case IngestPush:
		return "live+" + internalName
	case IngestPull:
		return "pull+" + internalName
	case IngestMistNative:
		return internalName
	default:
		return ""
	}
}

// EdgeCandidate is one peer node that can serve a federated source.
// Subset of the federation StreamAdvertisement edge proto, kept registry-
// local so federation callers don't have to import federation types.
type EdgeCandidate struct {
	NodeID      string
	BaseURL     string
	DTSCURL     string
	IsOrigin    bool
	BWAvailable int64
	CPUPercent  float64
	ViewerCount int32
	GeoLat      float64
	GeoLon      float64
	BufferState string
}

// Location is a per-cluster view of a stream — local or federated. Each
// stream has at most one Location per cluster ID: exactly one for the
// origin cluster (where it ingests) plus zero or more for clusters that
// are serving or pulling a copy.
type Location struct {
	ClusterID string
	// IsOrigin is true if this cluster is the source/ingest cluster for
	// the stream. Exactly one Location across federation should carry
	// IsOrigin=true.
	IsOrigin bool
	// IsLiveNow reflects current liveness in this specific cluster.
	// For the local cluster, populated by read-through from
	// StreamStateManager. For peer clusters, populated from the most
	// recent StreamAdvertisement.IsLive.
	IsLiveNow bool
	// SourceNodes lists local node IDs holding the source buffer when
	// ClusterID is the local cluster. Empty for peer Locations.
	SourceNodes []string
	// EdgeCandidates lists peer-side nodes that can serve the stream
	// when ClusterID is a remote cluster. Empty for the local Location.
	EdgeCandidates []EdgeCandidate
	// AdTimestamp is the Unix-seconds time the most recent
	// StreamAdvertisement was received from this cluster (peer entries).
	AdTimestamp int64
	// ReplicatingFrom names a peer cluster ID when ClusterID is the
	// local cluster and we are pulling this stream from that peer.
	ReplicatingFrom string
	// PullDTSCURL is the DTSC URL returned by the source cluster's
	// NotifyOriginPull ack, used by /source to direct Mist to the
	// upstream source. Only set when ReplicatingFrom is non-empty.
	PullDTSCURL string
	// DestNodeID is the local edge node performing the inbound pull —
	// only meaningful when this Location is the local cluster and
	// ReplicatingFrom is non-empty.
	DestNodeID string
	// DestNodeBaseURL is the public base URL of DestNodeID, captured at
	// pull-arrange time so /balance can hand viewers a working endpoint
	// without re-querying StreamStateManager.
	DestNodeBaseURL string
	// PullSourceNodeID is the peer-cluster node we are pulling from.
	// Carried so the replication-completion sweeper can broadcast a
	// typed ReplicationEvent without re-resolving.
	PullSourceNodeID string
	// OutboundPullers lists peer clusters currently pulling this stream
	// FROM this Location. Only populated when this Location is the
	// origin (ClusterID == origin cluster).
	OutboundPullers []OutboundPull
	// UpdatedAt is the wall-clock time this Location was last refreshed.
	UpdatedAt time.Time

	// SourceActive is true between an accepted PUSH_REWRITE on this
	// cluster and the matching PUSH_INPUT_CLOSE (or owner-unhealthy
	// short-circuit). When true, AdmitAndReserve rejects new pushes for
	// this stream on any node — only same-session reconnects can land.
	SourceActive bool
	// SourceInactiveAt is the wall-clock time SourceActive flipped to
	// false. Zero when SourceActive is true. Used by the admission rule
	// to bound the resume window for diagnostics.
	SourceInactiveAt time.Time
	// OwnerNodeID is the local cluster's node that currently owns (or
	// last owned) the publisher session for this stream. Retained after
	// SourceActive flips to false so a same-node PUSH_REWRITE can take
	// the resume path. Empty until the first accepted PUSH_REWRITE.
	OwnerNodeID string

	// RecordingNodeID is the node currently writing the active DVR
	// recording (foghorn.artifacts row with artifact_type='dvr' AND
	// status='recording') for this stream. Populated only when this
	// Location belongs to a peer cluster — federation advertisement
	// carries it. Receivers use it to construct
	// dtsc://<recording_node>/dvr+<hash> when arranging a
	// cross-cluster pull from STREAM_SOURCE dvr+. Empty when the
	// stream has no active DVR or when this is the local cluster's
	// Location (local recording is resolved via
	// ResolveDVRArtifactDispatch directly against DB).
	RecordingNodeID string
}

// OutboundPull describes one peer cluster that is pulling the stream
// from this Location.
type OutboundPull struct {
	DestClusterID string
	DestNodeID    string
	SourceNodeID  string
	DTSCURL       string
	CreatedAt     time.Time
}

// StreamEntry is the registry's canonical view of a source stream. One
// entry per stream globally; per-cluster details (where it's live, peer
// edges, replication state) live in Locations.
type StreamEntry struct {
	StreamID        string
	TenantID        string
	PlaybackID      string
	InternalName    string // concrete Mist source name
	IngestMode      IngestMode
	RuntimeName     string // derived from IngestMode + InternalName (bare for native, live+/pull+ for push/pull)
	OriginClusterID string

	// Locations carries per-cluster details for this stream. Keyed by
	// cluster_id. Always has at least one entry once hydrated.
	Locations map[string]Location

	// HydratedAt is the time this entry was first filled from Commodore /
	// federation / sidecar snapshot.
	HydratedAt time.Time
}

// LocalLocation returns the Location for the registry's local cluster
// (the one passed to NewStreamRegistry). Returns zero value + false if no
// local location is registered yet.
func (e StreamEntry) LocalLocation(localClusterID string) (Location, bool) {
	loc, ok := e.Locations[localClusterID]
	return loc, ok
}

// FederatedLocations returns Locations for every cluster other than the
// local one. Useful for "which peers have this stream" queries.
func (e StreamEntry) FederatedLocations(localClusterID string) []Location {
	if len(e.Locations) == 0 {
		return nil
	}
	out := make([]Location, 0, len(e.Locations))
	for cid, loc := range e.Locations {
		if cid == localClusterID {
			continue
		}
		out = append(out, loc)
	}
	return out
}

// IsLocallyOwned returns true if the local cluster is the stream's
// origin/ingest cluster.
func (e StreamEntry) IsLocallyOwned(localClusterID string) bool {
	loc, ok := e.Locations[localClusterID]
	return ok && loc.IsOrigin
}

// IsLiveAnywhere returns true if any cluster reports the stream live.
func (e StreamEntry) IsLiveAnywhere() bool {
	for _, loc := range e.Locations {
		if loc.IsLiveNow {
			return true
		}
	}
	return false
}

// ErrUnknownStream is returned when the registry has no entry matching the
// requested reference. Callers must NOT translate this into a push-default
// runtime name; the canonical response is to refuse the operation and emit
// a stream_registry.miss log so the missing site is visible.
var ErrUnknownStream = errors.New("stream_registry: unknown stream")

// StreamRegistryInstance is the Foghorn-wide registry, wired at startup
// alongside CommodoreClient. Nil-checks at every call site so unit tests
// that don't install the registry still compile.
var StreamRegistryInstance *StreamRegistry

// SetStreamRegistry installs the package-level registry. Called from the
// Foghorn bootstrap once Commodore client + cluster ID are known.
func SetStreamRegistry(r *StreamRegistry) {
	StreamRegistryInstance = r
}

// streamRegistryCommodore is the minimal Commodore surface the registry
// needs. Keeping it as an interface lets tests substitute a fake without
// pulling in the whole grpc client.
type streamRegistryCommodore interface {
	ResolveStreamContext(ctx context.Context, streamID, playbackID, internalName, clusterID string) (*commodorepb.ResolveStreamContextResponse, error)
}

// LivePresence is the minimal StreamStateManager surface the registry uses
// to fill StreamEntry.IsLiveNow + SourceNodes on read. Implemented by
// state.StreamStateManager via an adapter wired at startup; tests pass
// nil to skip the read-through.
type LivePresence interface {
	LiveSourceNodes(internalName string) (nodes []string, live bool)
}

// StreamRegistry is the authoritative source-runtime resolver inside
// Foghorn. Every site that needs a Mist source stream name (clip, DVR,
// federation DTSC construction, STREAM_SOURCE bare branch, thumbnail
// keying) must go through this registry. Misses fail closed.
type StreamRegistry struct {
	client    streamRegistryCommodore
	clusterID string
	ttl       time.Duration

	mu      sync.RWMutex
	byID    map[string]*cachedEntry // keyed by StreamID
	byInt   map[string]*cachedEntry // keyed by InternalName
	byPlay  map[string]*cachedEntry // keyed by PlaybackID
	missLog func(ctx context.Context, refKind, key string)

	// live is consulted on every Resolve to populate IsLiveNow +
	// SourceNodes from the existing StreamStateManager. Nil-safe: when
	// unset, the fields stay zero (registry caches still answer routing
	// questions; live presence is just unknown).
	live LivePresence

	// artifacts holds the artifact half of the inventory (VOD, DVR, Clip,
	// in-flight Processing). Kept as a sibling store so artifact_hash and
	// stream_id can both be UUIDs without map-key collisions.
	artifacts *artifactStore

	// managed holds the reconciler-side bookkeeping for managed
	// (mist-native) streams: per-(cluster, node, stream) Apply snapshots
	// plus the sidecar's heartbeat-reported applied set.
	managed *managedState

	// redisStore + instanceID + redisCancel + redisWg + redisLogger are
	// populated by EnableRedisSync. Mirrors the state.StreamStateManager
	// fields so the persistence story stays consistent across Foghorn
	// caches.
	redisStore  *RedisRegistryStore
	instanceID  string
	redisCancel context.CancelFunc
	redisWg     sync.WaitGroup
	redisLogger logging.Logger
}

type cachedEntry struct {
	entry  StreamEntry
	cached time.Time
}

// NewStreamRegistry creates a registry backed by the supplied Commodore
// client. ttl bounds how long resolutions are cached. If client is nil,
// every Resolve returns ErrUnknownStream — useful for tests that exercise
// the fail-closed path.
func NewStreamRegistry(client streamRegistryCommodore, clusterID string, ttl time.Duration) *StreamRegistry {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &StreamRegistry{
		client:    client,
		clusterID: clusterID,
		ttl:       ttl,
		byID:      make(map[string]*cachedEntry),
		byInt:     make(map[string]*cachedEntry),
		byPlay:    make(map[string]*cachedEntry),
		artifacts: newArtifactStore(),
		managed:   newManagedState(),
	}
}

// SetMissLogger registers a callback the registry invokes on every cache
// miss + Commodore miss. Used by the diagnostics layer to emit
// stream_registry.miss with parsed kind + raw input.
func (r *StreamRegistry) SetMissLogger(fn func(ctx context.Context, refKind, key string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.missLog = fn
}

// SetCommodoreClient swaps the Commodore client. Called from the
// foghorn-side Commodore reconnect path so a registry constructed with
// a nil client at startup (Commodore unreachable) starts resolving
// once Commodore comes back.
func (r *StreamRegistry) SetCommodoreClient(client streamRegistryCommodore) {
	r.mu.Lock()
	r.client = client
	r.mu.Unlock()
}

// SetLivePresence wires the read-through to StreamStateManager. Call at
// startup once the state manager is constructed. Nil unsets.
func (r *StreamRegistry) SetLivePresence(p LivePresence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.live = p
}

// MarkReplicating records that this cluster is currently pulling the
// source stream from a peer cluster, with the peer-provided DTSC URL.
// Updates the local Location's ReplicatingFrom + PullDTSCURL fields.
// Called from origin-pull dispatch paths after the source cluster acks.
// Idempotent.
func (r *StreamRegistry) MarkReplicating(internalName, peerClusterID, pullDTSCURL, destNodeID, destNodeBaseURL, pullSourceNodeID string) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return
	}
	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	if !ok {
		// Replication can be marked before any resolver populates the
		// stream's identity (the dest cluster pulls from a peer before
		// the stream becomes locally visible). Create a minimal entry so
		// /balance and /source can still find the DTSC URL while the
		// resolver catches up.
		ce = &cachedEntry{
			entry: StreamEntry{
				InternalName: internalName,
				Locations:    make(map[string]Location),
				HydratedAt:   time.Now(),
			},
			cached: time.Now(),
		}
		r.byInt[internalName] = ce
	}
	if ce.entry.Locations == nil {
		ce.entry.Locations = make(map[string]Location)
	}
	loc := ce.entry.Locations[r.clusterID]
	loc.ClusterID = r.clusterID
	loc.ReplicatingFrom = peerClusterID
	loc.PullDTSCURL = pullDTSCURL
	loc.DestNodeID = destNodeID
	loc.DestNodeBaseURL = destNodeBaseURL
	loc.PullSourceNodeID = pullSourceNodeID
	loc.IsLiveNow = true
	if destNodeID != "" {
		loc.SourceNodes = []string{destNodeID}
	}
	loc.UpdatedAt = time.Now()
	ce.entry.Locations[r.clusterID] = loc
	ce.cached = time.Now()
	snapshot := ce.entry
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// RecordOutboundPull records that a peer cluster is pulling this stream
// from the local cluster (we are the source). Idempotent on
// (DestClusterID, DestNodeID). Creates a minimal entry when none
// exists so a NotifyOriginPull racing ahead of identity hydration
// still lands durably — mirrors MarkReplicating's behavior on the
// dest side. Without the create, source clusters could silently ack
// a peer pull they have no record of.
func (r *StreamRegistry) RecordOutboundPull(internalName string, pull OutboundPull) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" || pull.DestClusterID == "" {
		return
	}
	if pull.CreatedAt.IsZero() {
		pull.CreatedAt = time.Now()
	}
	var snapshot StreamEntry
	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	if !ok {
		ce = &cachedEntry{
			entry: StreamEntry{
				InternalName: internalName,
				Locations:    make(map[string]Location),
				HydratedAt:   time.Now(),
			},
			cached: time.Now(),
		}
		r.byInt[internalName] = ce
	}
	if ce.entry.Locations == nil {
		ce.entry.Locations = make(map[string]Location)
	}
	loc := ce.entry.Locations[r.clusterID]
	loc.ClusterID = r.clusterID
	loc.IsOrigin = true
	replaced := false
	for i, existing := range loc.OutboundPullers {
		if existing.DestClusterID == pull.DestClusterID && existing.DestNodeID == pull.DestNodeID {
			loc.OutboundPullers[i] = pull
			replaced = true
			break
		}
	}
	if !replaced {
		loc.OutboundPullers = append(loc.OutboundPullers, pull)
	}
	loc.UpdatedAt = time.Now()
	ce.entry.Locations[r.clusterID] = loc
	ce.cached = time.Now()
	snapshot = ce.entry
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// ClearOutboundPull drops one outbound-pull record by (destCluster, destNode).
func (r *StreamRegistry) ClearOutboundPull(internalName, destClusterID, destNodeID string) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return
	}
	var snapshot StreamEntry
	var changed bool
	r.mu.Lock()
	if ce, ok := r.byInt[internalName]; ok {
		if loc, present := ce.entry.Locations[r.clusterID]; present && len(loc.OutboundPullers) > 0 {
			filtered := loc.OutboundPullers[:0]
			for _, p := range loc.OutboundPullers {
				if p.DestClusterID == destClusterID && p.DestNodeID == destNodeID {
					continue
				}
				filtered = append(filtered, p)
			}
			loc.OutboundPullers = filtered
			loc.UpdatedAt = time.Now()
			ce.entry.Locations[r.clusterID] = loc
			ce.cached = time.Now()
			snapshot = ce.entry
			changed = true
		}
	}
	r.mu.Unlock()
	if changed {
		r.publishUpsertSource(snapshot)
	}
}

// ClearReplicating unmarks an in-flight replication when the upstream
// pull terminates or expires.
func (r *StreamRegistry) ClearReplicating(internalName string) {
	_ = r.clearReplicating(internalName, "")
}

// ClearReplicatingForNode unmarks a replicated pull only when it belongs
// to the node that just reported the stream absent.
func (r *StreamRegistry) ClearReplicatingForNode(internalName, nodeID string) bool {
	return r.clearReplicating(internalName, strings.TrimSpace(nodeID))
}

func (r *StreamRegistry) clearReplicating(internalName, nodeID string) bool {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return false
	}
	var snapshot StreamEntry
	var changed bool
	r.mu.Lock()
	ce, ok := r.byInt[internalName]
	if ok {
		if loc, present := ce.entry.Locations[r.clusterID]; present {
			if nodeID != "" && loc.DestNodeID != "" && loc.DestNodeID != nodeID {
				r.mu.Unlock()
				return false
			}
			loc.ReplicatingFrom = ""
			loc.PullDTSCURL = ""
			loc.DestNodeID = ""
			loc.DestNodeBaseURL = ""
			loc.PullSourceNodeID = ""
			loc.IsLiveNow = false
			loc.SourceNodes = nil
			loc.UpdatedAt = time.Now()
			ce.entry.Locations[r.clusterID] = loc
			ce.cached = time.Now()
			snapshot = ce.entry
			changed = true
		}
	}
	r.mu.Unlock()
	if changed {
		r.publishUpsertSource(snapshot)
	}
	return changed
}

// LocalReplication returns the local cluster's replication state for the
// given stream, or zero+false if not currently replicating. Callers use
// this for the "are we already pulling this stream from a peer" check.
//
// Same invariant as lookup(): all reads of *cachedEntry must happen
// under the lock and the returned Location must be a copy. Reading
// ce.entry.Locations after RUnlock would race with writers
// (MarkReplicating, ClearReplicating, sweeper) mutating the map in
// place.
func (r *StreamRegistry) LocalReplication(_ context.Context, internalName string) (Location, bool) {
	internalName = sourceInternalKey(internalName)
	if internalName == "" {
		return Location{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce, ok := r.byInt[internalName]
	if !ok {
		return Location{}, false
	}
	loc, ok := ce.entry.Locations[r.clusterID]
	if !ok || loc.ReplicatingFrom == "" {
		return Location{}, false
	}
	// Copy OutboundPullers slice so the returned Location's mutation
	// can't corrupt the cached entry. Other slices/maps inside Location
	// are not currently mutated by callers; if that changes, deep-copy
	// here.
	if len(loc.OutboundPullers) > 0 {
		copied := make([]OutboundPull, len(loc.OutboundPullers))
		copy(copied, loc.OutboundPullers)
		loc.OutboundPullers = copied
	}
	return loc, true
}

func sourceInternalKey(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"live+", "pull+"} {
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

// AllLocalReplications returns every stream this cluster is currently
// pulling from a peer, keyed by source-stream internal_name.
func (r *StreamRegistry) AllLocalReplications() map[string]Location {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Location)
	for internalName, ce := range r.byInt {
		if loc, ok := ce.entry.Locations[r.clusterID]; ok && loc.ReplicatingFrom != "" {
			out[internalName] = loc
		}
	}
	return out
}

// SweepStaleLocations removes Locations whose UpdatedAt is older than
// maxAge. Returns the number of Locations dropped + entries deleted
// (when an entry's last Location ages out).
//
// Run by an internal ticker started in StartSweeper; also safe to invoke
// from tests with a small maxAge to force expiry deterministically.
func (r *StreamRegistry) SweepStaleLocations(maxAge time.Duration) (locationsRemoved, entriesEvicted int) {
	if maxAge <= 0 {
		return 0, 0
	}
	cutoff := time.Now().Add(-maxAge)
	var deletedInternalNames []string
	var publishUpserts []StreamEntry
	var publishDeletes []string

	r.mu.Lock()
	for internalName, ce := range r.byInt {
		if len(ce.entry.Locations) == 0 {
			continue
		}
		anyChanged := false
		for cid, loc := range ce.entry.Locations {
			// Prune individual OutboundPull entries older than maxAge. The
			// parent Location's UpdatedAt is refreshed by NotifyOriginPull
			// so the Location itself stays "fresh" while peers keep
			// pulling — but per-pull entries need their own expiry or
			// stale records (peer crashed, never sent stream_lifecycle
			// gone) accumulate forever in OutboundPullers.
			if len(loc.OutboundPullers) > 0 {
				kept := loc.OutboundPullers[:0]
				for _, p := range loc.OutboundPullers {
					if p.CreatedAt.IsZero() || p.CreatedAt.After(cutoff) {
						kept = append(kept, p)
					}
				}
				if len(kept) != len(loc.OutboundPullers) {
					loc.OutboundPullers = kept
					ce.entry.Locations[cid] = loc
					anyChanged = true
				}
			}
			// UpdatedAt is zero for entries that were hydrated but have
			// never had a Location-level update; treat zero as "fresh".
			if loc.UpdatedAt.IsZero() {
				continue
			}
			// Locations carrying live HA / runtime state are never evictable
			// regardless of UpdatedAt staleness. A stable long-running
			// publisher only touches UpdatedAt at admission time, so the
			// sweeper would otherwise erase SourceActive / OwnerNodeID and
			// reopen duplicate-ingest decisions. The clearing edges for
			// these fields are explicit events (PUSH_INPUT_CLOSE,
			// ClearReplicating, ClearOutboundPull), not time-based.
			if loc.SourceActive ||
				strings.TrimSpace(loc.OwnerNodeID) != "" ||
				strings.TrimSpace(loc.ReplicatingFrom) != "" ||
				len(loc.OutboundPullers) > 0 {
				continue
			}
			if loc.UpdatedAt.Before(cutoff) {
				delete(ce.entry.Locations, cid)
				locationsRemoved++
				anyChanged = true
			}
		}
		if len(ce.entry.Locations) == 0 && anyChanged {
			deletedInternalNames = append(deletedInternalNames, internalName)
			entriesEvicted++
			evicted := ce.entry
			delete(r.byInt, internalName)
			if evicted.StreamID != "" {
				delete(r.byID, evicted.StreamID)
			}
			if evicted.PlaybackID != "" {
				delete(r.byPlay, evicted.PlaybackID)
			}
			publishDeletes = append(publishDeletes, internalName)
		} else if anyChanged {
			ce.cached = time.Now()
			publishUpserts = append(publishUpserts, ce.entry)
		}
	}
	r.mu.Unlock()

	for _, e := range publishUpserts {
		r.publishUpsertSource(e)
	}
	for _, name := range publishDeletes {
		r.publishDeleteSource(name)
	}
	_ = deletedInternalNames
	return locationsRemoved, entriesEvicted
}

// StartSweeper launches a goroutine that periodically calls
// SweepStaleLocations(maxAge) every interval. Cancel via ctx. Returns
// immediately; the sweeper runs until ctx is done. Default tuning is
// 30s tick / 5m maxAge.
func (r *StreamRegistry) StartSweeper(ctx context.Context, interval, maxAge time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if maxAge <= 0 {
		maxAge = 5 * time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				r.SweepStaleLocations(maxAge)
			}
		}
	}()
}

// ResolveSourceByInternalName resolves a concrete Mist source internal_name
// to its canonical StreamEntry. Bare names from Mist triggers (STREAM_SOURCE,
// USER_NEW, thumbnails) feed in here.
func (r *StreamRegistry) ResolveSourceByInternalName(ctx context.Context, internalName string) (StreamEntry, error) {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return StreamEntry{}, ErrUnknownStream
	}
	if e, ok := r.lookup(r.byInt, internalName); ok {
		return e, nil
	}
	return r.hydrate(ctx, "internal_name", "", "", internalName)
}

// ResolveSourceByPlaybackID resolves a public playback ID to the canonical
// StreamEntry. Used by playback/PLAY_REWRITE paths that already have the
// public ID and need to discover the routing target.
func (r *StreamRegistry) ResolveSourceByPlaybackID(ctx context.Context, playbackID string) (StreamEntry, error) {
	playbackID = strings.TrimSpace(playbackID)
	if playbackID == "" {
		return StreamEntry{}, ErrUnknownStream
	}
	if e, ok := r.lookup(r.byPlay, playbackID); ok {
		return e, nil
	}
	return r.hydrate(ctx, "playback_id", "", playbackID, "")
}

// ResolveSourceByStreamID resolves a Commodore stream_id UUID. Used by API
// flows (clip dispatch, DVR start) that already hold the stream UUID.
func (r *StreamRegistry) ResolveSourceByStreamID(ctx context.Context, streamID string) (StreamEntry, error) {
	streamID = strings.TrimSpace(streamID)
	if streamID == "" {
		return StreamEntry{}, ErrUnknownStream
	}
	if e, ok := r.lookup(r.byID, streamID); ok {
		return e, nil
	}
	return r.hydrate(ctx, "stream_id", streamID, "", "")
}

func (r *StreamRegistry) lookup(m map[string]*cachedEntry, key string) (StreamEntry, bool) {
	// All reads of *cachedEntry must happen under the lock — writers
	// (AdmitAndReserve, UpsertLocalSource, UpsertFederatedSource, sweeper)
	// mutate ce.entry struct fields and ce.cached in place. Deep-copy
	// the Locations map here too so the live-presence enrichment below
	// runs against a private copy and doesn't race with future writes.
	r.mu.RLock()
	ce, ok := m[key]
	if !ok {
		r.mu.RUnlock()
		return StreamEntry{}, false
	}
	if time.Since(ce.cached) > r.ttl {
		r.mu.RUnlock()
		return StreamEntry{}, false
	}
	entry := ce.entry
	if entry.Locations != nil {
		locs := make(map[string]Location, len(entry.Locations))
		for k, v := range entry.Locations {
			locs[k] = v
		}
		entry.Locations = locs
	}
	live := r.live
	r.mu.RUnlock()

	// Live presence is read-through on every lookup so callers always see
	// fresh state without the registry having to subscribe to every Mist
	// trigger. StreamStateManager remains the source of truth for the
	// local Location; peer Locations carry IsLiveNow from the most recent
	// StreamAdvertisement (the peer cluster's observation).
	if live != nil && entry.InternalName != "" {
		nodes, isLive := live.LiveSourceNodes(entry.InternalName)
		local := entry.Locations[r.clusterID]
		local.ClusterID = r.clusterID
		local.IsLiveNow = isLive
		local.SourceNodes = nodes
		local.UpdatedAt = time.Now()
		if entry.Locations == nil {
			entry.Locations = make(map[string]Location)
		}
		entry.Locations[r.clusterID] = local
	}
	return entry, true
}

func (r *StreamRegistry) hydrate(ctx context.Context, refKind, streamID, playbackID, internalName string) (StreamEntry, error) {
	// Read client under the lock — SetCommodoreClient can swap it from
	// the reconnect goroutine.
	r.mu.RLock()
	client := r.client
	r.mu.RUnlock()
	if client == nil {
		r.recordMiss(ctx, refKind, firstNonEmpty(streamID, playbackID, internalName))
		return StreamEntry{}, ErrUnknownStream
	}
	resp, err := client.ResolveStreamContext(ctx, streamID, playbackID, internalName, r.clusterID)
	if err != nil {
		r.recordMiss(ctx, refKind, firstNonEmpty(streamID, playbackID, internalName))
		return StreamEntry{}, fmt.Errorf("stream_registry: commodore lookup: %w", err)
	}
	if resp == nil || !resp.GetAdmitted() {
		r.recordMiss(ctx, refKind, firstNonEmpty(streamID, playbackID, internalName))
		return StreamEntry{}, ErrUnknownStream
	}

	mode, err := IngestModeFromWire(resp.GetIngestMode())
	if err != nil {
		// Commodore returned an admitted row but no usable ingest_mode.
		// This is a Commodore-side bug; fail closed rather than guess.
		r.recordMiss(ctx, refKind, firstNonEmpty(streamID, playbackID, internalName))
		return StreamEntry{}, fmt.Errorf("stream_registry: %w (stream_id=%s): %w",
			ErrUnknownStream, resp.GetStreamId(), err)
	}

	entry := StreamEntry{
		StreamID:        resp.GetStreamId(),
		TenantID:        resp.GetTenantId(),
		PlaybackID:      resp.GetPlaybackId(),
		InternalName:    resp.GetInternalName(),
		IngestMode:      mode,
		RuntimeName:     RuntimeNameFor(mode, resp.GetInternalName()),
		OriginClusterID: resp.GetOriginClusterId(),
		HydratedAt:      time.Now(),
	}
	r.store(entry)
	return entry, nil
}

// store persists a hydrated StreamEntry. When an entry already exists
// for the same stream (TTL refresh case), identity fields are merged
// into the existing cachedEntry rather than replacing it — this
// preserves runtime Locations (SourceActive, OwnerNodeID,
// ReplicatingFrom, PullDTSCURL, OutboundPullers) across cache
// refresh. Replacing the entry on every hydrate would silently drop
// duplicate-ingest protection and origin-pull state every TTL window.
func (r *StreamRegistry) store(e StreamEntry) {
	if e.InternalName == "" && e.StreamID == "" && e.PlaybackID == "" {
		return
	}
	var snapshot StreamEntry
	r.mu.Lock()
	// Find any existing cached entry that this hydration corresponds to.
	var ce *cachedEntry
	if e.InternalName != "" {
		ce = r.byInt[e.InternalName]
	}
	if ce == nil && e.StreamID != "" {
		ce = r.byID[e.StreamID]
	}
	if ce == nil && e.PlaybackID != "" {
		ce = r.byPlay[e.PlaybackID]
	}
	if ce == nil {
		ce = &cachedEntry{entry: e, cached: time.Now()}
	} else {
		// Merge identity fields; preserve Locations + HydratedAt.
		if e.StreamID != "" {
			ce.entry.StreamID = e.StreamID
		}
		if e.TenantID != "" {
			ce.entry.TenantID = e.TenantID
		}
		if e.PlaybackID != "" {
			ce.entry.PlaybackID = e.PlaybackID
		}
		if e.InternalName != "" {
			ce.entry.InternalName = e.InternalName
		}
		if e.IngestMode != 0 {
			ce.entry.IngestMode = e.IngestMode
			ce.entry.RuntimeName = e.RuntimeName
		}
		if e.OriginClusterID != "" {
			ce.entry.OriginClusterID = e.OriginClusterID
		}
		ce.cached = time.Now()
	}
	if ce.entry.StreamID != "" {
		r.byID[ce.entry.StreamID] = ce
	}
	if ce.entry.InternalName != "" {
		r.byInt[ce.entry.InternalName] = ce
	}
	if ce.entry.PlaybackID != "" {
		r.byPlay[ce.entry.PlaybackID] = ce
	}
	snapshot = ce.entry
	r.mu.Unlock()
	r.publishUpsertSource(snapshot)
}

// Invalidate drops every cached entry for a stream. Called when Commodore
// signals a config change (managed stream apply/retract) so subsequent
// lookups re-hydrate.
func (r *StreamRegistry) Invalidate(streamID, internalName, playbackID string) {
	r.mu.Lock()
	if streamID != "" {
		delete(r.byID, streamID)
	}
	if internalName != "" {
		delete(r.byInt, internalName)
	}
	if playbackID != "" {
		delete(r.byPlay, playbackID)
	}
	r.mu.Unlock()
	if internalName != "" {
		r.publishDeleteSource(internalName)
	}
}

// Snapshot returns a copy of every currently-cached entry, used by the
// /debug/stream-registry diagnostics endpoint.
func (r *StreamRegistry) Snapshot() []StreamEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]struct{}, len(r.byInt)+len(r.byID))
	out := make([]StreamEntry, 0, len(r.byInt)+len(r.byID))
	appendEntry := func(ce *cachedEntry) {
		if ce == nil {
			return
		}
		key := ce.entry.StreamID
		if key == "" {
			key = ce.entry.InternalName
		}
		if key == "" {
			return
		}
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		entry := ce.entry
		if entry.Locations != nil {
			locs := make(map[string]Location, len(entry.Locations))
			for k, v := range entry.Locations {
				locs[k] = v
			}
			entry.Locations = locs
		}
		out = append(out, entry)
	}
	for _, ce := range r.byID {
		appendEntry(ce)
	}
	for _, ce := range r.byInt {
		appendEntry(ce)
	}
	return out
}

func (r *StreamRegistry) recordMiss(ctx context.Context, refKind, key string) {
	r.mu.RLock()
	fn := r.missLog
	r.mu.RUnlock()
	if fn != nil {
		fn(ctx, refKind, key)
	}
}

func firstNonEmpty(a, b, c string) string {
	if a != "" {
		return a
	}
	if b != "" {
		return b
	}
	return c
}
