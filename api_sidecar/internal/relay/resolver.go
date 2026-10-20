package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/admission"
	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/presignurl"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// ResolveContext is what the relay knows about an inbound request at resolve
// time. Identity-only — the relay does not assume size, source, or refs.
type ResolveContext struct {
	Ctx       context.Context
	AssetKind string // "vod" | "clip" | "dvr" | "upload"
	AssetHash string
	Ext       string // ".mkv" / ".mp4" / ".m3u8" / ...
	Hint      ipcpb.RelayResolveRequest_RelayHint
}

// ResolveResult is the relay's working copy of a RelayResolveResponse.
// Lowercases proto-typed fields so handlers don't need to import pb.
type ResolveResult struct {
	State              ipcpb.AssetState
	MediaPresignedURL  string
	DtshPresignedGet   string
	ExpectedSizeBytes  uint64
	ContentType        string
	URLTTLSeconds      int64
	PolicyHint         ipcpb.RelayResolveResponse_CacheDecisionHint
	Error              string
	StreamInternalName string // for DVR: top dir in storage/dvr/<stream>/<dvr_hash>/
	// Peer-relay fallback: when the origin cluster holds the canonical full
	// file but it isn't synced to S3 yet, Foghorn returns peer URLs pointing
	// at the origin node's Helmsman in place of MediaPresignedURL.
	// PeerRelayDtshURL is the sidecar URL (serveSidecarGetWithStream uses it
	// after local disk and DtshPresignedGet, before 404→regenerate).
	PeerRelayURL     string
	PeerRelayDtshURL string
	// PeerRelayGrantID is the opaque capability presented as Authorization:
	// Bearer on peer fetches; the origin edge authorizes it online with its
	// Foghorn (no signing key on this edge). Covers media + .dtsh.
	PeerRelayGrantID string
	// TenantSourceURL is a VOD import's tenant-supplied source, set instead of
	// MediaPresignedURL. It is fetched only with the relay's public-destination
	// client (Server.upstreamClient).
	TenantSourceURL string
	cachedAt        time.Time
}

// UpstreamURL returns the URL the block-cache fetcher should GET: the
// peer-relay URL when set, else the S3 presigned URL, else a VOD import's
// tenant source.
func (r *ResolveResult) UpstreamURL() string {
	if r == nil {
		return ""
	}
	if r.PeerRelayURL != "" {
		return r.PeerRelayURL
	}
	if r.MediaPresignedURL != "" {
		return r.MediaPresignedURL
	}
	return r.TenantSourceURL
}

// fromTenantSource reports whether the upstream is a tenant-supplied URL
// rather than platform storage or a peer.
func (r *ResolveResult) fromTenantSource() bool {
	return r != nil && r.PeerRelayURL == "" && r.MediaPresignedURL == "" && r.TenantSourceURL != ""
}

// objectIdentity names the object r's upstream bytes come from, or "" when r
// does not say which object that is.
//
// A presigned storage URL without its query names the object key, and Foghorn
// never rewrites the bytes under a key: an upload keeps its key and every
// frozen version is published under a fresh per-attempt key
// (FreezePublishKey). Renewing the signature for the same object therefore
// keeps the identity, while a resolution that moved to another object (the
// processed output after the upload) changes it. A peer-relay URL names the
// artifact on an origin node, not the version its file holds, so it has no
// identity; neither has a tenant import source, whose bytes the tenant owns.
func (r *ResolveResult) objectIdentity() string {
	if r == nil || r.PeerRelayURL != "" || r.MediaPresignedURL == "" {
		return ""
	}
	u, err := url.Parse(r.MediaPresignedURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// sameObject reports whether two object identities can name the same object:
// equal, or at least one of them unknown.
func sameObject(a, b string) bool {
	return a == "" || b == "" || a == b
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
	req := &ipcpb.RelayResolveRequest{
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
		ExpectedSizeBytes:  resp.GetExpectedSizeBytes(),
		ContentType:        resp.GetContentType(),
		URLTTLSeconds:      resp.GetUrlTtlSeconds(),
		PolicyHint:         resp.GetPolicyHint(),
		Error:              resp.GetError(),
		StreamInternalName: resp.GetStreamInternalName(),
		PeerRelayURL:       resp.GetPeerRelayUrl(),
		PeerRelayDtshURL:   resp.GetPeerRelayDtshUrl(),
		PeerRelayGrantID:   resp.GetPeerRelayGrantId(),
		TenantSourceURL:    resp.GetTenantSourceUrl(),
		cachedAt:           time.Now(),
	}, nil
}

// presignRefreshMargin is how long before the expiry signed into a cached
// storage URL the cache stops serving that resolution.
const presignRefreshMargin = 30 * time.Second

// renewable reports whether a fresh resolve can replace r's upstream
// credential: a presigned storage URL or a peer-relay grant. A tenant-supplied
// import source resolves to the same URL every time.
func (r *ResolveResult) renewable() bool {
	return r != nil && (r.PeerRelayGrantID != "" || r.MediaPresignedURL != "" || r.DtshPresignedGet != "")
}

// fresh reports whether r may still be served from the cache: within 80% of
// its declared TTL, and short of the earliest expiry signed into its storage
// URLs by presignRefreshMargin. A TTL of 0 means never cache.
func (r *ResolveResult) fresh(now time.Time) bool {
	if r.URLTTLSeconds <= 0 {
		return false
	}
	if now.Sub(r.cachedAt) > time.Duration(float64(r.URLTTLSeconds)*0.8)*time.Second {
		return false
	}
	if exp, ok := presignurl.EarliestExpiry(r.MediaPresignedURL, r.DtshPresignedGet); ok && !now.Before(exp.Add(-presignRefreshMargin)) {
		return false
	}
	return true
}

// resolveCache is a small in-memory cache of recent RelayResolve responses,
// keyed by (kind, hash, ext). The extension is part of the identity: an
// artifact whose container changed (an upload replaced by its processed
// output) resolves to a different object, or to none, for each extension.
type resolveCache struct {
	mu      sync.RWMutex
	entries map[string]*ResolveResult
}

func newResolveCache() *resolveCache {
	return &resolveCache{entries: make(map[string]*ResolveResult)}
}

func cacheKey(kind, hash, ext string) string {
	return kind + "/" + hash + "/" + strings.ToLower(ext)
}

func (rc *resolveCache) Get(kind, hash, ext string) (*ResolveResult, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	r, ok := rc.entries[cacheKey(kind, hash, ext)]
	if !ok || !r.fresh(time.Now()) {
		return nil, false
	}
	return r, true
}

// replacement returns the cached resolution that already replaced stale, when
// a concurrent request renewed it.
func (rc *resolveCache) replacement(kind, hash, ext string, stale *ResolveResult) (*ResolveResult, bool) {
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	cur, ok := rc.entries[cacheKey(kind, hash, ext)]
	if ok && cur != stale && cur.fresh(time.Now()) {
		return cur, true
	}
	return nil, false
}

// drop removes stale from the cache unless another resolution replaced it.
func (rc *resolveCache) drop(kind, hash, ext string, stale *ResolveResult) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	key := cacheKey(kind, hash, ext)
	if cur, ok := rc.entries[key]; ok && cur == stale {
		delete(rc.entries, key)
	}
}

func (rc *resolveCache) Put(kind, hash, ext string, r *ResolveResult) {
	if r == nil {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.entries[cacheKey(kind, hash, ext)] = r
}

func (rc *resolveCache) Delete(kind, hash, ext string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	delete(rc.entries, cacheKey(kind, hash, ext))
}

// resolveCached returns a cached resolve when fresh, otherwise issues a
// fresh resolve via s.resolver and stores the result in the cache.
func (s *Server) resolveCached(rc ResolveContext) (*ResolveResult, error) {
	if r, ok := s.cache.Get(rc.AssetKind, rc.AssetHash, rc.Ext); ok {
		return r, nil
	}
	res, err := s.resolver.Resolve(rc)
	if err != nil {
		return nil, err
	}
	s.cache.Put(rc.AssetKind, rc.AssetHash, rc.Ext, res)
	return res, nil
}

// resolveRenewer returns the function a request calls when its upstream
// refused the resolution it holds (an expired presigned URL, a dead peer
// grant) or no longer has its object (a superseded upload deleted from
// storage). The function yields a playable replacement: the one a concurrent
// request already fetched, else a fresh resolve through Foghorn. Concurrent
// renewals of one (kind, hash, ext) share a single resolve. ok is false when
// stale cannot be renewed or the fresh resolve is not playable; stale is out
// of the cache either way.
func (s *Server) resolveRenewer(ctx context.Context, kind, hash, ext string) func(stale *ResolveResult) (*ResolveResult, bool) {
	return func(stale *ResolveResult) (*ResolveResult, bool) {
		if !stale.renewable() {
			return nil, false
		}
		// The shared resolve outlives a caller that leaves, so the callers
		// still waiting on it get its answer; the control stream bounds each
		// resolve on its own.
		resolveCtx := context.WithoutCancel(ctx)
		v, err, _ := s.renewals.Do(cacheKey(kind, hash, ext), func() (any, error) {
			if cur, ok := s.cache.replacement(kind, hash, ext, stale); ok {
				return cur, nil
			}
			// stale stays cached while the resolve runs, so a request that
			// reads it meanwhile fails over to this shared resolve instead of
			// resolving on its own; the answer overwrites it.
			res, err := s.resolver.Resolve(ResolveContext{
				Ctx:       resolveCtx,
				AssetKind: kind,
				AssetHash: hash,
				Ext:       ext,
				Hint:      ipcpb.RelayResolveRequest_RELAY_HINT_RANDOM_ACCESS,
			})
			if err != nil || res == nil || res.State != ipcpb.AssetState_ASSET_STATE_PLAYABLE || res.UpstreamURL() == "" {
				if s.logger != nil {
					s.logger.WithError(err).WithField("asset_hash", hash).WithField("ext", ext).Warn("relay: upstream refused or lost the resolved source and re-resolve gave no playable source")
				}
				s.cache.drop(kind, hash, ext, stale)
				return nil, errNoPlayableRenewal
			}
			s.cache.Put(kind, hash, ext, res)
			return res, nil
		})
		if err != nil {
			return nil, false
		}
		res, ok := v.(*ResolveResult)
		return res, ok && res != nil
	}
}

// errNoPlayableRenewal is a shared re-resolve that produced no playable
// resolution.
var errNoPlayableRenewal = errors.New("relay re-resolve gave no playable source")

func newRequestID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
