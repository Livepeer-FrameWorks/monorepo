package handlers

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/triggers"

	"github.com/gin-gonic/gin"
)

var triggerProcessor *triggers.Processor
var remoteEdgeCache *federation.RemoteEdgeCache
var federationClient *federation.FederationClient

// peerAddrResolver is satisfied by *federation.PeerManager.
type peerAddrResolver interface {
	GetPeerAddr(clusterID string) string
}

var peerManager peerAddrResolver

// SetTriggerProcessor wires the running trigger processor into HTTP debug handlers.
// This is intended for local/dev dashboard introspection.
func SetTriggerProcessor(p *triggers.Processor) {
	triggerProcessor = p
	if clusterID != "" {
		triggerProcessor.SetClusterID(clusterID)
	}
	if ownerTenantID != "" {
		triggerProcessor.SetOwnerTenantID(ownerTenantID)
	}
}

// SetRemoteEdgeCache enables remote edge scoring for cross-cluster viewer routing in HTTP handlers.
func SetRemoteEdgeCache(cache *federation.RemoteEdgeCache) {
	remoteEdgeCache = cache
}

// SetFederationClient wires the federation client for cross-cluster QueryStream/NotifyOriginPull RPCs.
func SetFederationClient(c *federation.FederationClient) {
	federationClient = c
}

// SetPeerManager wires the peer manager for peer address lookups.
func SetPeerManager(pm *federation.PeerManager) {
	peerManager = pm
}

// httpRemoteArtifactAdapter wraps RemoteEdgeCache to satisfy control.RemoteArtifactLookup
// for the HTTP handler path.
type httpRemoteArtifactAdapter struct {
	cache *federation.RemoteEdgeCache
}

func (a *httpRemoteArtifactAdapter) GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*control.RemoteArtifactInfo, error) {
	entries, err := a.cache.GetRemoteArtifacts(ctx, artifactHash)
	if err != nil {
		return nil, err
	}
	infos := make([]*control.RemoteArtifactInfo, 0, len(entries))
	for _, e := range entries {
		infos = append(infos, &control.RemoteArtifactInfo{
			PeerCluster:  e.PeerCluster,
			NodeID:       e.NodeID,
			BaseURL:      e.BaseURL,
			SizeBytes:    e.SizeBytes,
			AccessCount:  e.AccessCount,
			LastAccessed: e.LastAccessed,
			GeoLat:       e.GeoLat,
			GeoLon:       e.GeoLon,
		})
	}
	return infos, nil
}

type streamContextCacheEntryView struct {
	Key       string `json:"key"`
	TenantID  string `json:"tenant_id"`
	UserID    string `json:"user_id"`
	Source    string `json:"source"`
	UpdatedAt string `json:"updated_at"`
	AgeSec    int64  `json:"age_sec"`
	LastError string `json:"last_error,omitempty"`
}

type streamContextCacheResponse struct {
	GeneratedAt string                        `json:"generated_at"`
	Summary     streamContextCacheSummary     `json:"summary"`
	Entries     []streamContextCacheEntryView `json:"entries"`
}

type streamContextCacheSummary struct {
	GeneratedAt string `json:"generated_at"`
	Size        int    `json:"size"`
	Hits        uint64 `json:"hits"`
	Misses      uint64 `json:"misses"`
	ResInternal uint64 `json:"resolves_internal_name"`
	ResPlayback uint64 `json:"resolves_playback_id"`
	ResErrors   uint64 `json:"resolve_errors"`
	LastResolve string `json:"last_resolve_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// HandleStreamContextCache exposes Foghorn's stream context cache (tenant/user enrichment) for debugging.
// Route: GET /debug/cache/stream-context
// Query params:
// - q: substring filter on key
// - limit: max entries (default 200, max 2000)
func HandleStreamContextCache(c *gin.Context) {
	if triggerProcessor == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "trigger processor not configured (stream context cache unavailable)",
		})
		return
	}

	limit := 200
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 2000 {
		limit = 2000
	}

	q := strings.TrimSpace(c.Query("q"))
	qLower := strings.ToLower(q)

	snap := triggerProcessor.StreamContextCacheSnapshot()

	entries := make([]streamContextCacheEntryView, 0, len(snap.Entries))
	for _, e := range snap.Entries {
		if qLower != "" && !strings.Contains(strings.ToLower(e.Key), qLower) {
			continue
		}
		age := time.Since(e.UpdatedAt)
		if e.UpdatedAt.IsZero() {
			age = 0
		}
		entries = append(entries, streamContextCacheEntryView{
			Key:       e.Key,
			TenantID:  e.TenantID,
			UserID:    e.UserID,
			Source:    e.Source,
			UpdatedAt: e.UpdatedAt.UTC().Format(time.RFC3339Nano),
			AgeSec:    int64(age.Seconds()),
			LastError: e.LastError,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].UpdatedAt > entries[j].UpdatedAt
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	c.JSON(http.StatusOK, streamContextCacheResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Summary: streamContextCacheSummary{
			GeneratedAt: snap.GeneratedAt.UTC().Format(time.RFC3339Nano),
			Size:        snap.Size,
			Hits:        snap.Hits,
			Misses:      snap.Misses,
			ResInternal: snap.ResInternal,
			ResPlayback: snap.ResPlayback,
			ResErrors:   snap.ResErrors,
			LastResolve: func() string {
				if snap.LastResolve.IsZero() {
					return ""
				}
				return snap.LastResolve.UTC().Format(time.RFC3339Nano)
			}(),
			LastError: snap.LastError,
		},
		Entries: entries,
	})
}
