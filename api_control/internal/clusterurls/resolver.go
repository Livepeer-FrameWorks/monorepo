// Package clusterurls is an in-process snapshot of cluster_id → Chandler
// base URL, populated from Quartermaster on a fixed interval. Read paths
// (ListStreams/GetClips/etc.) hit it via ChandlerBase without any network
// round-trip per row. Mirrors the URL shape used by Foghorn at
// api_balancing/internal/control/server.go:6267 so live and artifact
// paths can never disagree on a Chandler URL.
package clusterurls

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

const (
	defaultRefreshInterval = 60 * time.Second
	listPageLimit          = 100
)

// Resolver holds an atomic snapshot of cluster_id → Chandler base URL.
// Read paths call ChandlerBase; writes happen only from the refresh
// goroutine via Refresh.
type Resolver struct {
	qm     *qmclient.GRPCClient
	logger logging.Logger

	snapshot atomic.Pointer[map[string]string]

	startOnce sync.Once
}

// NewResolver returns a resolver wired to the given Quartermaster client.
// Call Start to begin the background refresh loop.
func NewResolver(qm *qmclient.GRPCClient, logger logging.Logger) *Resolver {
	r := &Resolver{qm: qm, logger: logger}
	empty := map[string]string{}
	r.snapshot.Store(&empty)
	return r
}

// Start kicks off the background refresh loop. interval defaults to 60s
// when zero. Start returns immediately; the first refresh runs synchronously
// before the goroutine is spawned so callers see a populated snapshot once
// Start returns (or an empty snapshot if Quartermaster is unavailable).
func (r *Resolver) Start(ctx context.Context, interval time.Duration) {
	r.startOnce.Do(func() {
		if interval <= 0 {
			interval = defaultRefreshInterval
		}
		if err := r.refresh(ctx); err != nil {
			r.logger.WithError(err).Warn("clusterurls: initial refresh failed; ChandlerBase will return empty until next tick")
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := r.refresh(ctx); err != nil {
						r.logger.WithError(err).Warn("clusterurls: refresh failed")
					}
				}
			}
		}()
	})
}

// ChandlerBase returns the Chandler base URL for the named cluster, or ""
// when the cluster is unknown. Pure map lookup; no I/O, no cache miss
// fallback.
func (r *Resolver) ChandlerBase(clusterID string) string {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return ""
	}
	snap := r.snapshot.Load()
	if snap == nil {
		return ""
	}
	return (*snap)[clusterID]
}

func (r *Resolver) refresh(ctx context.Context) error {
	if r.qm == nil {
		return nil
	}
	next := map[string]string{}
	var after *string
	for {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resp, err := r.qm.ListClusters(ctx, &pb.CursorPaginationRequest{
			First: listPageLimit,
			After: after,
		})
		cancel()
		if err != nil {
			return err
		}
		for _, c := range resp.GetClusters() {
			id := strings.TrimSpace(c.GetClusterId())
			if id == "" {
				continue
			}
			base := chandlerBaseFor(c)
			if base == "" {
				continue
			}
			next[id] = base
		}
		page := resp.GetPagination()
		if page == nil || !page.GetHasNextPage() {
			break
		}
		cursor := page.GetEndCursor()
		if cursor == "" {
			break
		}
		after = &cursor
	}
	r.snapshot.Store(&next)
	return nil
}

// BuildThumbnailAssets composes the shared.ThumbnailAssets URLs from the
// resolved Chandler base for the given cluster and the artifact's asset key.
// Returns nil when the cluster has no Chandler base in the snapshot or the
// asset key is empty. URL shape is single-sourced with Foghorn's
// buildThumbnailAssets in api_balancing/internal/control/playback.go.
func (r *Resolver) BuildThumbnailAssets(clusterID, assetKey string) *pb.ThumbnailAssets {
	if assetKey == "" {
		return nil
	}
	chandlerBase := r.ChandlerBase(clusterID)
	if chandlerBase == "" {
		return nil
	}
	prefix := strings.TrimRight(chandlerBase, "/") + "/assets/" + assetKey
	return &pb.ThumbnailAssets{
		PosterUrl:    prefix + "/poster.jpg",
		SpriteVttUrl: prefix + "/sprite.vtt",
		SpriteJpgUrl: prefix + "/sprite.jpg",
		AssetKey:     assetKey,
	}
}

// chandlerBaseFor mirrors getChandlerBaseURLForCluster in
// api_balancing/internal/control/server.go: `https://<chandler-subdomain>.<cluster-slug>.<base-domain>`.
func chandlerBaseFor(c *pb.InfrastructureCluster) string {
	baseDomain := strings.TrimSpace(c.GetBaseUrl())
	if baseDomain == "" {
		return ""
	}
	slug := pkgdns.ClusterSlug(c.GetClusterId(), c.GetClusterName())
	if slug == "" {
		return ""
	}
	fqdn, ok := pkgdns.ServiceFQDN("chandler", slug+"."+baseDomain)
	if !ok || fqdn == "" {
		return ""
	}
	return "https://" + fqdn
}
