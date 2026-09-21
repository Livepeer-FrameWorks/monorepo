package mediaauthority

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	useReportInterval = time.Minute
	useReportBatch    = 1000
	useReportTimeout  = 10 * time.Second
	// The control plane keeps use to the day, so a second report inside this
	// window would change nothing there.
	useReportEvery = 20 * time.Hour
)

// AuthorityUse is one authority this cell decided on.
type AuthorityUse struct {
	Kind     string
	ID       string
	TenantID string
}

// useRecorder remembers which authorities were decided on and when each was
// last reported. It is memory only. The control plane stops renewing an object
// after thirty days without use, so a process that loses an unsent batch costs
// nothing: the object is decided on again, or it really is unused.
type useRecorder struct {
	mu       sync.Mutex
	reported map[AuthorityUse]time.Time
	pending  map[AuthorityUse]struct{}
}

func newUseRecorder() *useRecorder {
	return &useRecorder{reported: map[AuthorityUse]time.Time{}, pending: map[AuthorityUse]struct{}{}}
}

// NoteUse records that a decision was made on an authority this cell holds. The
// control plane keeps an object in cells, and renews it, only while cells report
// deciding on it. It is called for a decision that admitted something, never for
// a refusal: a refused object is not in use.
func (s *Store) NoteUse(kind, id, tenantID string) {
	if s == nil || s.uses == nil || id == "" || tenantID == "" {
		return
	}
	use, now := AuthorityUse{Kind: kind, ID: id, TenantID: tenantID}, s.now()
	s.uses.mu.Lock()
	if last, ok := s.uses.reported[use]; !ok || now.Sub(last) >= useReportEvery {
		s.uses.pending[use] = struct{}{}
	}
	s.uses.mu.Unlock()
}

// NoteMediaObjectUse is NoteUse for a media-object snapshot.
func (s *Store) NoteMediaObjectUse(object MediaObjectSnapshot) {
	if object.Authority == nil {
		return
	}
	s.NoteUse("media_object", object.AuthorityID, object.Authority.GetTenantId())
}

// RunUseReporter sends what NoteUse collected until ctx ends. A batch that could
// not be sent stays pending and goes out with the next one.
func (s *Store) RunUseReporter(ctx context.Context, report func(context.Context, []AuthorityUse) error, logger logging.Logger) {
	if s == nil || s.uses == nil || report == nil {
		return
	}
	ticker := time.NewTicker(useReportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.flushUses(ctx, report); err != nil && ctx.Err() == nil && logger != nil {
				logger.WithError(err).Warn("Failed to report media authority use; it stays queued")
			}
		}
	}
}

func (s *Store) flushUses(ctx context.Context, report func(context.Context, []AuthorityUse) error) error {
	for {
		s.uses.mu.Lock()
		batch := make([]AuthorityUse, 0, min(len(s.uses.pending), useReportBatch))
		for use := range s.uses.pending {
			if len(batch) == useReportBatch {
				break
			}
			batch = append(batch, use)
		}
		s.uses.mu.Unlock()
		if len(batch) == 0 {
			return nil
		}
		reportCtx, cancel := context.WithTimeout(ctx, useReportTimeout)
		err := report(reportCtx, batch)
		cancel()
		if err != nil {
			return err
		}
		now := s.now()
		s.uses.mu.Lock()
		for _, use := range batch {
			delete(s.uses.pending, use)
			s.uses.reported[use] = now
		}
		// Entries old enough to be reported again carry no information.
		for use, last := range s.uses.reported {
			if now.Sub(last) >= useReportEvery {
				delete(s.uses.reported, use)
			}
		}
		s.uses.mu.Unlock()
	}
}
