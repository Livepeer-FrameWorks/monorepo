package knowledge

import (
	"sync"
	"time"
)

// SourceHealth tracks crawl health for a single source.
type SourceHealth struct {
	Source              string    `json:"source"`
	LastSuccessAt       time.Time `json:"last_success_at,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	PagesTotal          int       `json:"pages_total"`
	PagesLastCycle      int       `json:"pages_last_cycle"`
}

// HealthTracker tracks per-source crawl health.
type HealthTracker struct {
	mu      sync.RWMutex
	sources map[string]*SourceHealth
}

func NewHealthTracker() *HealthTracker {
	return &HealthTracker{
		sources: make(map[string]*SourceHealth),
	}
}

func (h *HealthTracker) RecordSuccess(source string, pages int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.ensure(source)
	s.LastSuccessAt = time.Now()
	s.ConsecutiveFailures = 0
	s.PagesLastCycle = pages
	s.PagesTotal += pages
}

func (h *HealthTracker) RecordFailure(source string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.ensure(source)
	s.LastFailureAt = time.Now()
	s.ConsecutiveFailures++
	return s.ConsecutiveFailures
}

func (h *HealthTracker) Snapshot() []SourceHealth {
	h.mu.RLock()
	defer h.mu.RUnlock()
	result := make([]SourceHealth, 0, len(h.sources))
	for _, s := range h.sources {
		result = append(result, *s)
	}
	return result
}

func (h *HealthTracker) ensure(source string) *SourceHealth {
	s, ok := h.sources[source]
	if !ok {
		s = &SourceHealth{Source: source}
		h.sources[source] = s
	}
	return s
}
