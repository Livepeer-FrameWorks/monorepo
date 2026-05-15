package control

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// defrostTracker counts in-flight defrosts per storage node. Foghorn already
// owns the (defrost_node_id, storage_location='defrosting') state in
// foghorn.artifacts, so this is a fast in-memory mirror — no heartbeat /
// proto change required. Used by PickDefrostNode to spread defrosts across
// healthy storage edges instead of stacking them on the first one.
//
// Bootstrap: on restart, hydrate from foghorn.artifacts so the count reflects
// reality (a defrost started by the previous Foghorn process is still
// counted by Helmsman, so it should count here too).
type defrostTracker struct {
	mu            sync.Mutex
	counts        map[string]int
	lastBootstrap time.Time
}

var globalDefrostTracker = &defrostTracker{counts: make(map[string]int)}

// retryGuard caps INSUFFICIENT_SPACE retries to one per artifactHash per
// window. Non-persistent because StaleDefrostCleanupJob handles longer-tail
// recovery; the guard exists only to prevent a tight retry loop when several
// nodes pile up on the same artifact in quick succession.
type retryGuardEntry struct {
	expiresAt time.Time
}

var (
	retryGuardMu sync.Mutex
	retryGuard   = make(map[string]retryGuardEntry)
)

// TryConsumeRetryGuard records an intent to retry artifactHash. Returns true
// if the caller should proceed with the retry; false if a previous retry is
// still within the window.
func TryConsumeRetryGuard(artifactHash string, ttl time.Duration) bool {
	if artifactHash == "" {
		return false
	}
	retryGuardMu.Lock()
	defer retryGuardMu.Unlock()
	now := time.Now()
	if entry, ok := retryGuard[artifactHash]; ok && now.Before(entry.expiresAt) {
		return false
	}
	// Opportunistic GC: drop expired entries while we hold the lock.
	for k, e := range retryGuard {
		if now.After(e.expiresAt) {
			delete(retryGuard, k)
		}
	}
	retryGuard[artifactHash] = retryGuardEntry{expiresAt: now.Add(ttl)}
	return true
}

// IncrementDefrost records that we just sent a defrost request to nodeID.
// Idempotent on empty nodeID.
func IncrementDefrost(nodeID string) {
	if nodeID == "" {
		return
	}
	globalDefrostTracker.mu.Lock()
	defer globalDefrostTracker.mu.Unlock()
	globalDefrostTracker.counts[nodeID]++
}

// DecrementDefrost records that a defrost ended on nodeID (success or any
// failure reason). Bounded at 0 so spurious decrements (e.g. a complete
// arriving after the in-memory count was reset by a bootstrap) cannot push
// the counter negative.
func DecrementDefrost(nodeID string) {
	if nodeID == "" {
		return
	}
	globalDefrostTracker.mu.Lock()
	defer globalDefrostTracker.mu.Unlock()
	if globalDefrostTracker.counts[nodeID] > 0 {
		globalDefrostTracker.counts[nodeID]--
	}
	if globalDefrostTracker.counts[nodeID] == 0 {
		delete(globalDefrostTracker.counts, nodeID)
	}
}

// ActiveDefrostCount returns the per-node defrost count snapshot. The map is
// a copy so callers can iterate without holding the lock.
func ActiveDefrostCount() map[string]int {
	globalDefrostTracker.mu.Lock()
	defer globalDefrostTracker.mu.Unlock()
	out := make(map[string]int, len(globalDefrostTracker.counts))
	for k, v := range globalDefrostTracker.counts {
		out[k] = v
	}
	return out
}

// BootstrapDefrostTracker hydrates the counts from foghorn.artifacts. Safe
// to call repeatedly; runs the DB read at most once per minute. Call once
// at startup (after db is ready) and optionally on a slow ticker for
// multi-Foghorn deployments.
func BootstrapDefrostTracker(ctx context.Context, logger logging.Logger) {
	globalDefrostTracker.mu.Lock()
	if !globalDefrostTracker.lastBootstrap.IsZero() && time.Since(globalDefrostTracker.lastBootstrap) < time.Minute {
		globalDefrostTracker.mu.Unlock()
		return
	}
	globalDefrostTracker.mu.Unlock()

	if db == nil {
		return
	}

	rows, err := db.QueryContext(ctx, `
		SELECT defrost_node_id, COUNT(*)
		  FROM foghorn.artifacts
		 WHERE storage_location = 'defrosting'
		   AND defrost_node_id IS NOT NULL
		 GROUP BY defrost_node_id`)
	if err != nil {
		if logger != nil {
			logger.WithError(err).Warn("defrost tracker bootstrap: query failed")
		}
		return
	}
	defer rows.Close()

	fresh := make(map[string]int)
	for rows.Next() {
		var nodeID sql.NullString
		var count int
		if err := rows.Scan(&nodeID, &count); err != nil {
			continue
		}
		if nodeID.Valid && nodeID.String != "" {
			fresh[nodeID.String] = count
		}
	}
	if err := rows.Err(); err != nil {
		if logger != nil {
			logger.WithError(err).Warn("defrost tracker bootstrap: rows iteration failed")
		}
		return
	}

	globalDefrostTracker.mu.Lock()
	defer globalDefrostTracker.mu.Unlock()
	globalDefrostTracker.counts = fresh
	globalDefrostTracker.lastBootstrap = time.Now()
}
