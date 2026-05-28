package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/control"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// ResolveContext is what the relay knows about an inbound request at resolve
// time. Identity-only — the relay does not assume size, source, or refs.
type ResolveContext struct {
	Ctx       context.Context
	AssetKind string // "vod" | "clip" | "dvr" | "upload"
	AssetHash string
	Ext       string // ".mkv" / ".mp4" / ".m3u8" / ...
	Hint      pb.RelayResolveRequest_RelayHint
}

// ResolveResult is the relay's working copy of a RelayResolveResponse.
// Lowercases proto-typed fields so handlers don't need to import pb.
type ResolveResult struct {
	State              pb.AssetState
	MediaPresignedURL  string
	DtshPresignedGet   string
	DtshPresignedPut   string
	ExpectedSizeBytes  uint64
	ContentType        string
	URLTTLSeconds      int64
	PolicyHint         pb.RelayResolveResponse_CacheDecisionHint
	Error              string
	StreamInternalName string // for DVR: top dir in storage/dvr/<stream>/<dvr_hash>/
	// Peer-relay fallback: when the origin cluster holds the canonical
	// full file but it isn't synced to S3 yet, Foghorn returns a URL
	// pointing at the origin node's Helmsman in place of
	// MediaPresignedURL. PeerRelayAuthToken is a short-lived JWT
	// validated by the origin Helmsman as Authorization: Bearer.
	PeerRelayURL       string
	PeerRelayAuthToken string
	cachedAt           time.Time
}

// UpstreamURL returns the URL the block-cache fetcher should GET.
// Peer-relay takes precedence when set; otherwise the S3 presigned URL.
func (r *ResolveResult) UpstreamURL() string {
	if r == nil {
		return ""
	}
	if r.PeerRelayURL != "" {
		return r.PeerRelayURL
	}
	return r.MediaPresignedURL
}

// IntentFromHint maps a Foghorn-provided CacheDecisionHint to the local
// admission intent. The relay's own intent for playback fills is always
// PlaybackCache; processing inputs are always ProcessingInput. The hint is
// only consulted to break ties — local pressure/intent still wins.
func IntentFromHint(kind string) admission.StorageIntent {
	switch kind {
	case "upload":
		return admission.IntentProcessingInput
	default:
		return admission.IntentPlaybackCache
	}
}

// controlResolver is the production Resolver: it issues RelayResolveRequest
// over the Helmsman↔Foghorn control stream. Tests substitute their own
// Resolver via Options.
type controlResolver struct{}

// NewControlResolver returns a Resolver that talks to Foghorn over the
// established control stream. Returns ErrNotConnected if no stream is up.
func NewControlResolver() Resolver { return &controlResolver{} }

// Resolve sends RelayResolveRequest and waits for the response. Caller
// context is honored; control client also applies a default 10s timeout.
func (r *controlResolver) Resolve(rc ResolveContext) (*ResolveResult, error) {
	id, err := newRequestID()
	if err != nil {
		return nil, err
	}
	req := &pb.RelayResolveRequest{
		RequestId: id,
		AssetKind: rc.AssetKind,
		AssetHash: rc.AssetHash,
		Ext:       rc.Ext,
		Hint:      rc.Hint,
	}
	resp, err := control.RequestRelayResolve(rc.Ctx, req)
	if err != nil {
		return nil, err
	}
	return &ResolveResult{
		State:              resp.GetState(),
		MediaPresignedURL:  resp.GetMediaPresignedUrl(),
		DtshPresignedGet:   resp.GetDtshPresignedGet(),
		DtshPresignedPut:   resp.GetDtshPresignedPut(),
		ExpectedSizeBytes:  resp.GetExpectedSizeBytes(),
		ContentType:        resp.GetContentType(),
		URLTTLSeconds:      resp.GetUrlTtlSeconds(),
		PolicyHint:         resp.GetPolicyHint(),
		Error:              resp.GetError(),
		StreamInternalName: resp.GetStreamInternalName(),
		PeerRelayURL:       resp.GetPeerRelayUrl(),
		PeerRelayAuthToken: resp.GetPeerRelayAuthToken(),
		cachedAt:           time.Now(),
	}, nil
}

// resolveCache is a small in-memory cache of recent RelayResolve responses,
// keyed by (kind, hash). Entries expire at url_ttl_seconds * 0.8 so the
// relay refreshes before the presigned URL goes stale.
type resolveCache struct {
	mu      sync.RWMutex
	entries map[string]*ResolveResult
}

func newResolveCache() *resolveCache {
	return &resolveCache{entries: make(map[string]*ResolveResult)}
}

func cacheKey(kind, hash string) string {
	return kind + "/" + hash
}

func (rc *resolveCache) Get(kind, hash string) (*ResolveResult, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	r, ok := rc.entries[cacheKey(kind, hash)]
	if !ok {
		return nil, false
	}
	// Expire at 80% of declared TTL. 0 TTL means never cache.
	if r.URLTTLSeconds <= 0 {
		return nil, false
	}
	if time.Since(r.cachedAt) > time.Duration(float64(r.URLTTLSeconds)*0.8)*time.Second {
		return nil, false
	}
	return r, true
}

func (rc *resolveCache) Put(kind, hash string, r *ResolveResult) {
	if r == nil {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.entries[cacheKey(kind, hash)] = r
}

func (rc *resolveCache) Delete(kind, hash string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.entries, cacheKey(kind, hash))
}

// resolveCached returns a cached resolve when fresh, otherwise issues a
// fresh resolve via s.resolver and stores the result in the cache.
func (s *Server) resolveCached(rc ResolveContext) (*ResolveResult, error) {
	if r, ok := s.cache.Get(rc.AssetKind, rc.AssetHash); ok {
		return r, nil
	}
	res, err := s.resolver.Resolve(rc)
	if err != nil {
		return nil, err
	}
	s.cache.Put(rc.AssetKind, rc.AssetHash, res)
	return res, nil
}

func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
