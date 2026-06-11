package balancer

import (
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// SharedFanOut deduplicates concurrent expensive candidate lookups per key
// (singleflight) and memoizes results, including empty ones, for a short
// TTL, so N viewer requests hitting the same cold stream produce one fan-out
// instead of N, and a dead-peer window costs one fan-out per TTL instead of
// one per request. Used by both viewer-resolution surfaces (HTTP /play in
// internal/handlers and the gRPC ViewerControlService).
//
// The supplied fn must NOT be bound to a single caller's cancellation: its
// result is shared with concurrent waiters and memoized for everyone, so an
// abandoned first request would otherwise poison the whole window with an
// empty set. Callers detach via context.WithoutCancel + their own timeout.
type SharedFanOut struct {
	ttl   time.Duration
	group singleflight.Group

	mu   sync.Mutex
	memo map[string]sharedFanOutEntry
}

type sharedFanOutEntry struct {
	at         time.Time
	candidates []RemoteEdgeCandidate
}

func NewSharedFanOut(ttl time.Duration) *SharedFanOut {
	return &SharedFanOut{ttl: ttl, memo: make(map[string]sharedFanOutEntry)}
}

func (s *SharedFanOut) Do(key string, fn func() []RemoteEdgeCandidate) []RemoteEdgeCandidate {
	s.mu.Lock()
	if e, ok := s.memo[key]; ok && time.Since(e.at) <= s.ttl {
		s.mu.Unlock()
		return e.candidates
	}
	s.mu.Unlock()

	v, err, _ := s.group.Do(key, func() (any, error) {
		cands := fn()
		s.mu.Lock()
		// Opportunistic prune keeps the map bounded by recently-active
		// distinct keys without a sweeper goroutine.
		for k, e := range s.memo {
			if time.Since(e.at) > s.ttl {
				delete(s.memo, k)
			}
		}
		s.memo[key] = sharedFanOutEntry{at: time.Now(), candidates: cands}
		s.mu.Unlock()
		return cands, nil
	})
	if err != nil {
		return nil
	}
	cands, ok := v.([]RemoteEdgeCandidate)
	if !ok {
		return nil
	}
	return cands
}

// Memoized reports whether key currently has a live memo entry (test seam).
func (s *SharedFanOut) Memoized(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.memo[key]
	return ok && time.Since(e.at) <= s.ttl
}
