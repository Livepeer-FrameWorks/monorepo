package handlers

import (
	"context"
	"frameworks/api_analytics_ingest/internal/database/periscopeingestdb"
	"time"
)

type networkScope struct{ Tenant, Network, Environment string }

// StartNetworkRollups rebuilds changed buckets within the retained source window.
// Ownership comes from successfully ingested events, never from a caller-supplied tenant.
func (h *AnalyticsHandler) StartNetworkRollups(ctx context.Context, lease LedgerLease) {
	go func() {
		cursors := map[networkScope]time.Time{}
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			h.networkScopes.Range(func(key, _ any) bool {
				scope, ok := key.(networkScope)
				if !ok {
					return true
				}
				passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				release, acquired, err := lease.TryAcquire(passCtx, "network-observations:"+scope.Tenant+":"+scope.Network+":"+scope.Environment)
				if err != nil {
					h.logger.WithError(err).Warn("Network rollup lease unavailable")
					return true
				}
				if !acquired {
					return true
				}
				defer func() {
					unlock, stop := context.WithTimeout(context.Background(), 5*time.Second)
					defer stop()
					if releaseErr := release(unlock); releaseErr != nil {
						h.logger.WithError(releaseErr).Warn("Network rollup lease release failed")
					}
				}()
				through := time.Now().UTC()
				since := cursors[scope]
				if since.IsZero() {
					since = through.Add(-30 * 24 * time.Hour)
				}
				// Overlap tolerates millisecond timestamp precision and in-flight inserts.
				since = since.Add(-time.Minute)
				for _, interval := range []int{300, 3600} {
					if err = periscopeingestdb.RebuildNetworkRollups(passCtx, h.clickhouse, scope.Tenant, scope.Network, scope.Environment, since, through, interval); err != nil {
						h.logger.WithError(err).Warn("Network rollup rebuild failed; retaining cursor")
						return true
					}
				}
				cursors[scope] = through
				return ctx.Err() == nil
			})
		}
	}()
}
