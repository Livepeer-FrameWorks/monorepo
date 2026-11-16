// Package sessionrevocations maintains local interactive-token revocation authority.
package sessionrevocations

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Lister func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error)

// Source delivers to this replica independently of other replicas' consumers.
type Source interface {
	Run(context.Context, func(*commodorepb.SessionRevocation)) error
	CatchUp(context.Context) error
	Close()
}

type Opener func(context.Context) (Source, error)

// MaxSnapshotAge is the interactive-auth authority freshness contract. Events
// accelerate revocation, while complete snapshots bound unseen publication loss.
const MaxSnapshotAge = 2 * time.Minute

type Config struct {
	Secret          []byte
	Cache           *auth.SessionRevocations
	List            Lister
	Open            Opener
	Logger          logging.Logger
	Applied         func(*commodorepb.SessionRevocation)
	Interval        time.Duration
	SnapshotTimeout time.Duration
	RetryMin        time.Duration
	RetryMax        time.Duration
}

// Feed receives events and reconciles independently of requests. A failed refresh
// preserves learned revocations; a cold miss never initiates network traffic.
type Feed struct {
	cfg          Config
	cache        *auth.SessionRevocations
	trigger      chan struct{}
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	connected    atomic.Bool
	synchronized atomic.Bool
	lastSuccess  atomic.Int64
	failures     atomic.Uint64
	sourceMu     sync.Mutex
	source       Source
	generation   uint64
}

func Start(ctx context.Context, cfg Config) *Feed {
	if cfg.Cache == nil {
		cfg.Cache = auth.NewSessionRevocations()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.SnapshotTimeout <= 0 {
		cfg.SnapshotTimeout = 10 * time.Second
	}
	if cfg.RetryMin <= 0 {
		cfg.RetryMin = time.Second
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 30 * time.Second
	}
	ctx, cancel := context.WithCancel(ctx)
	f := &Feed{cfg: cfg, cache: cfg.Cache, trigger: make(chan struct{}, 1), cancel: cancel}
	f.cache.RequireSnapshot()
	if cfg.List != nil {
		f.wg.Go(func() { f.reconcile(ctx) })
		if cfg.Open == nil {
			f.Resume()
		}
	}
	if cfg.Open != nil {
		f.wg.Go(func() { f.follow(ctx) })
	}
	return f
}

func (f *Feed) Cache() *auth.SessionRevocations { return f.cache }

func (f *Feed) Close() { f.cancel(); f.wg.Wait() }

// Resume requests an overlapping snapshot after confirmed stream subscription.
func (f *Feed) Resume() {
	select {
	case f.trigger <- struct{}{}:
	default:
	}
}

func (f *Feed) CheckSession(_ context.Context, token string) error {
	_, err := auth.ValidateRevocableSessionJWT(token, f.cfg.Secret, f.cache)
	if err != nil {
		if errors.Is(err, auth.ErrSessionStateUnavailable) {
			return status.Error(codes.Unavailable, "session state unavailable")
		}
		return fmt.Errorf("%w: %w", err, status.Error(codes.Unauthenticated, "invalid or signed-out session"))
	}
	return nil
}

func (f *Feed) Apply(rev *commodorepb.SessionRevocation) bool {
	if rev.GetTenantId() == "" || rev.GetUserId() == "" || !rev.GetRevokedAt().IsValid() {
		return false
	}
	if rev.GetSessionId() != "" {
		f.cache.RecordSession(rev.GetTenantId(), rev.GetUserId(), rev.GetSessionId(), rev.GetRevokedAt().AsTime())
	} else {
		f.cache.Record(rev.GetTenantId(), rev.GetUserId(), rev.GetRevokedAt().AsTime())
	}
	if f.cfg.Applied != nil {
		f.cfg.Applied(rev)
	}
	return true
}

func (f *Feed) follow(ctx context.Context) {
	retry := f.cfg.RetryMin
	for ctx.Err() == nil {
		stream, err := f.cfg.Open(ctx)
		if err == nil {
			f.sourceMu.Lock()
			f.source, f.generation = stream, f.generation+1
			f.sourceMu.Unlock()
			f.connected.Store(true)
			f.Resume()
			retry = f.cfg.RetryMin
			err = stream.Run(ctx, func(rev *commodorepb.SessionRevocation) { f.Apply(rev) })
			f.sourceMu.Lock()
			f.source = nil
			f.sourceMu.Unlock()
			stream.Close()
		}
		f.connected.Store(false)
		if ctx.Err() != nil {
			return
		}
		f.logFailure("Revocation event stream unavailable", err)
		if !pause(ctx, jitter(retry)) {
			return
		}
		retry = min(retry*2, f.cfg.RetryMax)
	}
}

func (f *Feed) reconcile(ctx context.Context) {
	ticker := time.NewTicker(jitter(f.cfg.Interval))
	defer ticker.Stop()
	retry := f.cfg.RetryMin
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.trigger:
		case <-ticker.C:
		}
		f.cache.Prune()
		for {
			readCtx, cancel := context.WithTimeout(ctx, f.cfg.SnapshotTimeout)
			observedAt := time.Now()
			f.sourceMu.Lock()
			source, generation := f.source, f.generation
			f.sourceMu.Unlock()
			response, err := f.cfg.List(readCtx, time.Now().Add(-auth.SessionTokenTTL-auth.JWTClockSkewLeeway))
			if err == nil {
				for _, rev := range response.GetRevocations() {
					f.Apply(rev)
				}
				if !response.GetAuthSessionsIncluded() {
					err = errors.New("authority does not confirm named revocation snapshot")
				}
			}
			if err == nil && f.cfg.Open != nil {
				if source == nil {
					err = errors.New("revocation source not established")
				} else {
					err = source.CatchUp(readCtx)
				}
			}
			cancel()
			f.sourceMu.Lock()
			if err == nil && generation != f.generation {
				err = errors.New("revocation source changed during snapshot")
			}
			if err == nil {
				f.cache.ConfirmSnapshot(observedAt, MaxSnapshotAge)
				f.synchronized.Store(true)
				f.lastSuccess.Store(observedAt.Unix())
				retry = f.cfg.RetryMin
				f.sourceMu.Unlock()
				break
			}
			f.sourceMu.Unlock()
			f.synchronized.Store(false)
			if ctx.Err() != nil {
				return
			}
			f.logFailure("Revocation backfill unavailable", err)
			if !f.waitRecovery(ctx, jitter(retry)) {
				return
			}
			retry = min(retry*2, f.cfg.RetryMax)
		}
	}
}

// Reconnection can repair a failed snapshot immediately instead of waiting for
// its retry delay. Ordinary failures remain bounded by the recovery backoff.
func (f *Feed) waitRecovery(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-f.trigger:
		return true
	case <-timer.C:
		return true
	}
}

func (f *Feed) logFailure(message string, err error) {
	f.failures.Add(1)
	if f.cfg.Logger != nil {
		f.cfg.Logger.WithError(err).Warn(message)
	}
}

func jitter(d time.Duration) time.Duration {
	return d*4/5 + time.Duration(rand.Int64N(max(1, int64(d*2/5))))
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
