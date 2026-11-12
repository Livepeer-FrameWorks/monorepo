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
	signalmanclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Lister func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error)
type Opener func(context.Context) (signalmanclient.EventStream, error)

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
	if cfg.List != nil {
		f.wg.Go(func() { f.reconcile(ctx) })
		f.Resume()
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

// Decode admits only internal revocation payloads from the service-only channel.
func Decode(tenant string, event *signalmanpb.TenantEvent) *commodorepb.SessionRevocation {
	if tenant == "" || event == nil {
		return nil
	}
	rev := &commodorepb.SessionRevocation{TenantId: tenant}
	switch event.GetType() {
	case "user.sessions_revoked":
		var payload internalv1.UserSessionsRevoked
		if err := event.GetData().UnmarshalTo(&payload); err != nil {
			return nil
		}
		rev.UserId, rev.RevokedAt = payload.GetUserId(), payload.GetRevokedAt()
		rev.AccountDeactivated = payload.GetReason() == internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_OPERATOR_DEACTIVATION
	case "auth_session.revoked":
		var payload internalv1.AuthSessionRevoked
		if err := event.GetData().UnmarshalTo(&payload); err != nil || payload.GetSessionId() == "" {
			return nil
		}
		rev.UserId, rev.SessionId, rev.RevokedAt = payload.GetUserId(), payload.GetSessionId(), payload.GetRevokedAt()
	default:
		return nil
	}
	if rev.GetUserId() == "" || !rev.GetRevokedAt().IsValid() {
		return nil
	}
	return rev
}

func (f *Feed) follow(ctx context.Context) {
	retry := f.cfg.RetryMin
	for ctx.Err() == nil {
		stream, err := f.cfg.Open(ctx)
		if err == nil {
			f.connected.Store(true)
			f.Resume()
			retry = f.cfg.RetryMin
			for ctx.Err() == nil {
				var event *signalmanpb.SignalmanEvent
				event, err = stream.Recv()
				if err != nil {
					break
				}
				if event.GetChannel() == signalmanpb.Channel_CHANNEL_SESSIONS && event.GetEventType() == signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT {
					f.Apply(Decode(event.GetTenantId(), event.GetData().GetTenantEvent()))
				}
			}
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
			response, err := f.cfg.List(readCtx, time.Now().Add(-auth.SessionTokenTTL-auth.JWTClockSkewLeeway))
			cancel()
			if err == nil {
				for _, rev := range response.GetRevocations() {
					f.Apply(rev)
				}
				if !response.GetAuthSessionsIncluded() {
					err = errors.New("authority does not confirm named revocation snapshot")
				}
			}
			if err == nil {
				f.synchronized.Store(true)
				f.lastSuccess.Store(time.Now().Unix())
				retry = f.cfg.RetryMin
				break
			}
			f.synchronized.Store(false)
			if ctx.Err() != nil {
				return
			}
			f.logFailure("Revocation backfill unavailable", err)
			if !pause(ctx, jitter(retry)) {
				return
			}
			retry = min(retry*2, f.cfg.RetryMax)
		}
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
