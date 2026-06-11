// Package identity is Foghorn's single front door for "who does this
// stream/artifact belong to, and where does it live" questions. Every
// trigger handler, gRPC surface, and federation path resolves identity
// through this facade instead of hand-rolling its own lookup chain — the
// bug class where one consumer read a cold layer and attributed work to an
// empty tenant/node can then only be fixed once, here.
//
// Resolution layers, in order:
//
//	stream:   in-memory state union (per-instance, fast, no I/O)
//	        → unified stream registry (shared cache; hydrates from
//	          Commodore on miss and mirrors the result back)
//	artifact: unified registry (cache → foghorn SQL)
//	        → Commodore Resolve*Hash (system of record)
//
// Identity fields merge monotonically across layers — a later layer fills
// blanks, never erases — mirroring the state layer's applyIdentity rule.
// Authoritative not-found answers are negative-cached briefly so an unknown
// name arriving with every Mist trigger can't turn into a Commodore RPC
// firehose; transient layer failures are never cached.
package identity

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrUnknown is returned when no layer can attribute the identifier.
var ErrUnknown = errors.New("identity: unknown identifier")

// ErrNotFound is what adapter funcs return to signal an AUTHORITATIVE
// not-found from a layer's system of record — the layer answered, and the
// answer is "this does not exist". Any other error is treated as transient
// (RPC failure, DB outage) and is never negative-cached, so a dependency
// flap can't become a 30s hard ErrUnknown for freeze/mint/thumbnails.
var ErrNotFound = errors.New("identity: not found")

// StreamIdentity is the platform identity of a live/pull/mist-native
// source stream.
type StreamIdentity struct {
	InternalName    string
	StreamID        string
	PlaybackID      string
	TenantID        string
	NodeID          string // serving node — known only to in-memory state
	ServingCluster  string // cluster of the serving node
	OriginClusterID string
	Source          string // layer that attributed the tenant
}

// ArtifactIdentity is the platform identity of a clip/VOD/DVR/processing
// artifact.
type ArtifactIdentity struct {
	ArtifactHash       string
	Kind               string // clip | vod | dvr | processing
	InternalName       string // artifact's own internal_name
	StreamInternalName string // parent source stream
	StreamID           string // parent source stream UUID
	TenantID           string
	OriginClusterID    string
	StorageClusterID   string
	Source             string // layer that attributed the tenant
}

// StreamStateView is the slice of the in-memory state union the resolver
// reads. Kept as a plain struct so the state package isn't imported here.
type StreamStateView struct {
	StreamID   string
	PlaybackID string
	TenantID   string
	NodeID     string
}

// Config wires the resolver to the deployment's layers. Any field may be
// nil — that layer is skipped — so the facade works in every shape:
// single-Foghorn cells, registry-less tests, Commodore-less self-hosts.
//
// Error contract for adapter funcs: return ErrNotFound (or, for
// CommodoreArtifact, a nil error with a zero identity) when the layer's
// system of record authoritatively says the identifier does not exist; any
// other error is treated as transient and is never negative-cached.
type Config struct {
	// StreamState returns the in-memory union for a concrete internal
	// name, or false when unknown.
	StreamState func(internalName string) (StreamStateView, bool)

	// NodeCluster returns the cluster a node belongs to ("" if unknown).
	NodeCluster func(nodeID string) string

	// RegistrySource resolves a source internal name via the unified
	// stream registry. The registry hydrates from Commodore on miss, so
	// this leg is also the system-of-record path for sources.
	RegistrySource func(ctx context.Context, internalName string) (StreamIdentity, error)

	// RegistryArtifact resolves an artifact hash via the unified registry
	// (cache → foghorn SQL, including in-flight processing jobs).
	RegistryArtifact func(ctx context.Context, artifactHash string) (ArtifactIdentity, error)

	// CommodoreArtifact resolves an artifact hash of a specific kind
	// (clip|vod|dvr) from the system of record. Used when the local
	// registry/SQL has no row yet (e.g. freeze before finalize callback).
	CommodoreArtifact func(ctx context.Context, kind, artifactHash string) (ArtifactIdentity, error)

	// ArtifactTenants batch-maps artifact hashes to tenant IDs from the
	// foghorn.artifacts authority (federation ad attribution).
	ArtifactTenants func(ctx context.Context, hashes []string) (map[string]string, error)

	// Observe, when set, receives one call per consulted layer:
	// kind ∈ {stream, artifact}, layer ∈ {negative_cache, state, registry,
	// commodore, db_batch}, outcome ∈ {hit, miss, error}. Wired to a
	// Prometheus counter in main so the next siloing bug shows up on a
	// dashboard instead of via prod archaeology.
	Observe func(kind, layer, outcome string)

	// NegativeTTL bounds how long an authoritative not-found suppresses
	// re-resolution. Defaults to 30s.
	NegativeTTL time.Duration
}

// Resolver implements the layered lookups. Safe for concurrent use.
type Resolver struct {
	cfg Config

	negMu  sync.Mutex
	negTTL time.Duration
	neg    map[string]time.Time
}

// negCacheMaxEntries bounds the negative cache; on overflow it resets
// wholesale (entries are 30s-lived throwaways, not state).
const negCacheMaxEntries = 4096

// NewResolver creates a resolver over the given layers.
func NewResolver(cfg Config) *Resolver {
	ttl := cfg.NegativeTTL
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Resolver{cfg: cfg, negTTL: ttl, neg: make(map[string]time.Time)}
}

// defaultResolver is the process-wide instance, wired in cmd/foghorn.
// Package-level handler functions (control triggers, gRPC surfaces) reach
// the facade through Default(); constructor-injected components may hold
// the *Resolver directly.
var (
	defaultMu       sync.RWMutex
	defaultResolver *Resolver
)

// SetDefault installs the process-wide resolver.
func SetDefault(r *Resolver) {
	defaultMu.Lock()
	defaultResolver = r
	defaultMu.Unlock()
}

// Default returns the process-wide resolver, or nil before wiring (tests,
// early boot). Callers must treat nil as "no facade" and fail their
// operation the same way they would on ErrUnknown.
func Default() *Resolver {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultResolver
}

func (r *Resolver) observe(kind, layer, outcome string) {
	if r.cfg.Observe != nil {
		r.cfg.Observe(kind, layer, outcome)
	}
}

func (r *Resolver) negativeHit(key string) bool {
	r.negMu.Lock()
	defer r.negMu.Unlock()
	at, ok := r.neg[key]
	if !ok {
		return false
	}
	if time.Since(at) > r.negTTL {
		delete(r.neg, key)
		return false
	}
	return true
}

func (r *Resolver) negativeStore(key string) {
	r.negMu.Lock()
	if len(r.neg) >= negCacheMaxEntries {
		r.neg = make(map[string]time.Time)
	}
	r.neg[key] = time.Now()
	r.negMu.Unlock()
}

// fill sets dst when it is empty and src is not — the same monotonic-merge
// rule the state layer applies, so no layer can erase what an earlier
// layer knew.
func fill(dst *string, src string) {
	if *dst == "" && src != "" {
		*dst = src
	}
}

// ResolveStream resolves a concrete source internal name (no live+/pull+
// prefix — callers parse first) to its platform identity. Returns
// ErrUnknown when no layer can attribute a tenant.
func (r *Resolver) ResolveStream(ctx context.Context, internalName string) (StreamIdentity, error) {
	internalName = strings.TrimSpace(internalName)
	if internalName == "" {
		return StreamIdentity{}, ErrUnknown
	}
	negKey := "s:" + internalName
	if r.negativeHit(negKey) {
		r.observe("stream", "negative_cache", "hit")
		return StreamIdentity{}, ErrUnknown
	}

	id := StreamIdentity{InternalName: internalName}

	if r.cfg.StreamState != nil {
		if ss, ok := r.cfg.StreamState(internalName); ok {
			fill(&id.StreamID, ss.StreamID)
			fill(&id.PlaybackID, ss.PlaybackID)
			fill(&id.TenantID, ss.TenantID)
			fill(&id.NodeID, ss.NodeID)
			if id.NodeID != "" && r.cfg.NodeCluster != nil {
				fill(&id.ServingCluster, r.cfg.NodeCluster(id.NodeID))
			}
			if ss.TenantID != "" {
				id.Source = "state"
				r.observe("stream", "state", "hit")
			} else {
				r.observe("stream", "state", "miss")
			}
		} else {
			r.observe("stream", "state", "miss")
		}
	}

	// The state union never carries origin-cluster context, so the
	// registry leg runs unless state already answered everything it can.
	authoritative := false
	transient := false
	if r.cfg.RegistrySource != nil && (id.TenantID == "" || id.StreamID == "" || id.OriginClusterID == "") {
		reg, err := r.cfg.RegistrySource(ctx, internalName)
		switch {
		case err == nil:
			fill(&id.StreamID, reg.StreamID)
			fill(&id.PlaybackID, reg.PlaybackID)
			fill(&id.TenantID, reg.TenantID)
			fill(&id.OriginClusterID, reg.OriginClusterID)
			if id.Source == "" && reg.TenantID != "" {
				id.Source = "registry"
			}
			authoritative = true
			r.observe("stream", "registry", "hit")
		case errors.Is(err, ErrNotFound):
			authoritative = true
			r.observe("stream", "registry", "miss")
		default:
			transient = true
			r.observe("stream", "registry", "error")
		}
	}

	if id.TenantID == "" {
		// Negative-cache only when an authoritative layer answered
		// not-found and nothing failed transiently. A state-only miss
		// (no registry wired) is not cached either: there is no RPC
		// firehose to protect, and state can change on the next trigger.
		if authoritative && !transient && ctx.Err() == nil {
			r.negativeStore(negKey)
		}
		return StreamIdentity{}, ErrUnknown
	}
	return id, nil
}

// artifactKinds is the fallback probe order when the caller has no kind
// hint. Clip first: it is by far the most common hash to arrive unannounced
// (freeze and thumbnail flows fire before finalize lands a row).
var artifactKinds = []string{"clip", "vod", "dvr"}

// ResolveArtifact resolves an artifact hash to its platform identity.
// kindHint ("clip"|"vod"|"dvr"), when known, pins the Commodore fallback
// to one RPC; when empty, the known kinds are probed in order. Returns
// ErrUnknown when no layer can attribute a tenant.
func (r *Resolver) ResolveArtifact(ctx context.Context, artifactHash, kindHint string) (ArtifactIdentity, error) {
	artifactHash = strings.TrimSpace(artifactHash)
	if artifactHash == "" {
		return ArtifactIdentity{}, ErrUnknown
	}
	negKey := "a:" + kindHint + ":" + artifactHash
	if r.negativeHit(negKey) {
		r.observe("artifact", "negative_cache", "hit")
		return ArtifactIdentity{}, ErrUnknown
	}

	// Kind is NOT pre-seeded from the hint: it reports what the artifact
	// actually is (consumers compare it against what the caller asked
	// for); the hint only pins the Commodore probe below.
	id := ArtifactIdentity{ArtifactHash: artifactHash}

	authoritative := false
	transient := false
	if r.cfg.RegistryArtifact != nil {
		reg, err := r.cfg.RegistryArtifact(ctx, artifactHash)
		switch {
		case err == nil:
			fill(&id.Kind, reg.Kind)
			fill(&id.InternalName, reg.InternalName)
			fill(&id.StreamInternalName, reg.StreamInternalName)
			fill(&id.StreamID, reg.StreamID)
			fill(&id.TenantID, reg.TenantID)
			fill(&id.OriginClusterID, reg.OriginClusterID)
			fill(&id.StorageClusterID, reg.StorageClusterID)
			authoritative = true
			if reg.TenantID != "" {
				id.Source = "registry"
				r.observe("artifact", "registry", "hit")
			} else {
				r.observe("artifact", "registry", "miss")
			}
		case errors.Is(err, ErrNotFound):
			authoritative = true
			r.observe("artifact", "registry", "miss")
		default:
			transient = true
			r.observe("artifact", "registry", "error")
		}
	}

	// The Commodore leg runs while either attribution (tenant) or the
	// parent stream name is missing — a healed local row can carry the
	// tenant without the stream name clip/DVR S3 keys embed.
	if (id.TenantID == "" || id.StreamInternalName == "") && r.cfg.CommodoreArtifact != nil {
		kinds := artifactKinds
		if kindHint != "" {
			kinds = []string{kindHint}
		}
		hit := false
		for _, kind := range kinds {
			// Per-kind probe outcomes are negative-cached in their own
			// keyspace ("ak:"), shared by hinted and hintless calls, so
			// partial knowledge survives: a hash known to not be a clip
			// skips that RPC while the vod/dvr probes are still retried.
			// Deliberately NOT the whole-call "a:" key — that one gates the
			// entire resolution (registry layer included) and may only be
			// written by the authoritative-and-not-transient verdict below.
			kindKey := "ak:" + kind + ":" + artifactHash
			if r.negativeHit(kindKey) {
				authoritative = true
				r.observe("artifact", "negative_cache", "kind_skip")
				continue
			}
			com, err := r.cfg.CommodoreArtifact(ctx, kind, artifactHash)
			switch {
			case errors.Is(err, ErrNotFound):
				authoritative = true
				if ctx.Err() == nil {
					r.negativeStore(kindKey)
				}
				continue
			case err != nil:
				transient = true
				continue
			case com.TenantID == "":
				// nil error + zero identity is the adapters' "found
				// nothing" answer — authoritative, same as ErrNotFound.
				authoritative = true
				if ctx.Err() == nil {
					r.negativeStore(kindKey)
				}
				continue
			}
			// A kind probe that resolves to a DIFFERENT tenant is a hash
			// collision across kinds, not a fill source.
			if id.TenantID != "" && com.TenantID != id.TenantID {
				continue
			}
			fill(&id.Kind, kind)
			fill(&id.InternalName, com.InternalName)
			fill(&id.StreamInternalName, com.StreamInternalName)
			fill(&id.StreamID, com.StreamID)
			fill(&id.TenantID, com.TenantID)
			fill(&id.OriginClusterID, com.OriginClusterID)
			fill(&id.StorageClusterID, com.StorageClusterID)
			if id.Source == "" {
				id.Source = "commodore"
			}
			hit = true
			r.observe("artifact", "commodore", "hit")
			break
		}
		if !hit {
			r.observe("artifact", "commodore", "miss")
		}
	}

	if id.TenantID == "" {
		// Same rule as ResolveStream: cache only authoritative not-found,
		// never a transient layer failure.
		if authoritative && !transient && ctx.Err() == nil {
			r.negativeStore(negKey)
		}
		return ArtifactIdentity{}, ErrUnknown
	}
	return id, nil
}

// ResolveArtifactTenants batch-maps artifact hashes to tenant IDs from the
// foghorn.artifacts authority. Hashes with no attribution are absent from
// the result (callers skip them); a nil map means the layer is unavailable.
func (r *Resolver) ResolveArtifactTenants(ctx context.Context, hashes []string) (map[string]string, error) {
	if len(hashes) == 0 || r.cfg.ArtifactTenants == nil {
		return nil, nil
	}
	tenants, err := r.cfg.ArtifactTenants(ctx, hashes)
	if err != nil {
		r.observe("artifact", "db_batch", "error")
		return nil, err
	}
	r.observe("artifact", "db_batch", "hit")
	return tenants, nil
}
