package mediaauthority

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const localAuthorityRefreshCooldown = time.Minute

type refreshCoordinator struct {
	mu          sync.Mutex
	lastAttempt time.Time
	request     func(context.Context) error
	group       singleflight.Group
}

func (s *Store) SetRefreshRequester(request func(context.Context) error) {
	if s == nil || s.refresh == nil {
		return
	}
	s.refresh.mu.Lock()
	s.refresh.request = request
	s.refresh.mu.Unlock()
}

func (s *Store) observeFreshness(freshness Freshness) {
	if s == nil || s.refresh == nil || freshness != FreshnessSoftExpired {
		return
	}
	now := s.now().UTC()
	s.refresh.mu.Lock()
	request := s.refresh.request
	if request == nil || now.Sub(s.refresh.lastAttempt) < localAuthorityRefreshCooldown {
		s.refresh.mu.Unlock()
		return
	}
	s.refresh.lastAttempt = now
	s.refresh.mu.Unlock()
	go func() {
		_, _, _ = s.refresh.group.Do("cell-replay", func() (any, error) { //nolint:errcheck // a replay request is advisory; the durable authority remains usable and the cooldown schedules another attempt
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return nil, request(ctx)
		})
	}()
}
