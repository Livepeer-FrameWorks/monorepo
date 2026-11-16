package sessionrevocations

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var testSecret = []byte("local-session-revocation-authority")

func TestColdProjectionRefusesInteractiveCredentials(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := Start(context.Background(), Config{Secret: testSecret, List: func(ctx context.Context, _ time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		defer feed.Close()
		synctest.Wait()
		if code := status.Code(feed.CheckSession(context.Background(), testToken(t, "unknown"))); code != codes.Unavailable {
			t.Fatalf("unhydrated projection accepted an unchecked session: got %s, want Unavailable", code)
		}
	})
}

func TestStaleProjectionRefusesUnknownAndRetainsKnownRevocations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var unavailable atomic.Bool
		feed := Start(context.Background(), Config{Secret: testSecret, List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
			if unavailable.Load() {
				return nil, errors.New("authority unavailable")
			}
			return &commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true}, nil
		}})
		defer feed.Close()
		synctest.Wait()
		if err := feed.CheckSession(context.Background(), testToken(t, "other")); err != nil {
			t.Fatal(err)
		}
		feed.Apply(testRevocation("revoked"))
		unavailable.Store(true)
		time.Sleep(MaxSnapshotAge + time.Second)
		synctest.Wait()
		if code := status.Code(feed.CheckSession(context.Background(), testToken(t, "other"))); code != codes.Unavailable {
			t.Fatalf("stale projection accepted unknown session: %s", code)
		}
		if code := status.Code(feed.CheckSession(context.Background(), testToken(t, "revoked"))); code != codes.Unauthenticated {
			t.Fatalf("stale projection forgot known revocation: %s", code)
		}
		unavailable.Store(false)
		feed.Resume()
		synctest.Wait()
		if err := feed.CheckSession(context.Background(), testToken(t, "other")); err != nil {
			t.Fatalf("recovered projection refused valid device: %v", err)
		}
	})
}

func testToken(t *testing.T, sid string) string {
	t.Helper()
	token, err := auth.GenerateNamedSessionJWT("user", "tenant", "user@example.test", "member", nil, sid, time.Now(), time.Now().Add(auth.SessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func testRevocation(sid string) *commodorepb.SessionRevocation {
	return &commodorepb.SessionRevocation{TenantId: "tenant", UserId: "user", SessionId: sid, RevokedAt: timestamppb.Now()}
}

type fakeStream struct {
	ctx    context.Context
	events chan *signalmanpb.SignalmanEvent
}

func (s *fakeStream) Recv() (*signalmanpb.SignalmanEvent, error) {
	select {
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case event := <-s.events:
		if event == nil {
			return nil, io.EOF
		}
		return event, nil
	}
}
func (s *fakeStream) Run(ctx context.Context, apply func(*commodorepb.SessionRevocation)) error {
	for {
		event, err := s.Recv()
		if err != nil {
			return err
		}
		apply(Decode(event.GetTenantId(), event.GetData().GetTenantEvent()))
	}
}
func (s *fakeStream) CatchUp(context.Context) error { return nil }
func (s *fakeStream) Close()                        {}

func eventFor(t *testing.T, sid string) *signalmanpb.SignalmanEvent {
	t.Helper()
	payload, err := anypb.New(&internalv1.AuthSessionRevoked{UserId: "user", SessionId: sid, RevokedAt: timestamppb.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return &signalmanpb.SignalmanEvent{TenantId: proto.String("tenant"), Channel: signalmanpb.Channel_CHANNEL_SESSIONS, EventType: signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT,
		Data: &signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: &signalmanpb.TenantEvent{Type: "auth_session.revoked", Data: payload}}}}
}

func TestColdRequestsRefuseWithoutWaitingForBackfill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		feed := Start(context.Background(), Config{Secret: testSecret, List: func(ctx context.Context, _ time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
			calls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		defer feed.Close()
		synctest.Wait()
		for _, sid := range []string{"cold-one", "cold-two", "cold-three"} {
			for range 10 {
				if err := feed.CheckSession(context.Background(), testToken(t, sid)); status.Code(err) != codes.Unavailable {
					t.Fatalf("unhydrated credential was not refused: %v", err)
				}
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("request fan-out loaded snapshot %d times", calls.Load())
		}
		if err := feed.CheckSession(context.Background(), "not-a-jwt"); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("malformed token accepted during outage: %v", err)
		}
		expired, err := auth.GenerateNamedSessionJWT("user", "tenant", "", "member", nil, "expired", time.Now(), time.Now().Add(-2*time.Minute), testSecret)
		if err != nil {
			t.Fatal(err)
		}
		if err := feed.CheckSession(context.Background(), expired); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("expired token accepted during outage: %v", err)
		}
		feed.Apply(testRevocation("cold-one"))
		if err := feed.CheckSession(context.Background(), testToken(t, "cold-one")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("known revoked sid accepted: %v", err)
		}
		if err := feed.CheckSession(context.Background(), testToken(t, "cold-two")); status.Code(err) != codes.Unavailable {
			t.Fatalf("other device rejected: %v", err)
		}
	})
}

func TestEventsContinueWhileCommodoreUnavailable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var unavailable atomic.Bool
		events := make(chan *signalmanpb.SignalmanEvent, 1)
		feed := Start(context.Background(), Config{Secret: testSecret,
			List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
				if unavailable.Load() {
					return nil, errors.New("Commodore down")
				}
				return &commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true}, nil
			},
			Open: func(ctx context.Context) (Source, error) {
				return &fakeStream{ctx: ctx, events: events}, nil
			},
		})
		defer feed.Close()
		synctest.Wait()
		unavailable.Store(true)
		feed.Resume()
		events <- eventFor(t, "lost")
		synctest.Wait()
		if !feed.connected.Load() {
			t.Fatal("event feed did not connect")
		}
		time.Sleep(time.Second)
		if err := feed.CheckSession(context.Background(), testToken(t, "lost")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("event ignored during authority outage: %v", err)
		}
		if err := feed.CheckSession(context.Background(), testToken(t, "other")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDecodeDoesNotTurnMalformedNamedEventIntoGlobalCutoff(t *testing.T) {
	event := eventFor(t, "")
	if rev := Decode("tenant", event.GetData().GetTenantEvent()); rev != nil {
		t.Fatalf("sid-less named event decoded as a global cutoff: %v", rev)
	}
	event = eventFor(t, "session")
	if rev := Decode("", event.GetData().GetTenantEvent()); rev != nil {
		t.Fatalf("unscoped event accepted: %v", rev)
	}
	event.GetData().GetTenantEvent().Type = "api_token.revoked"
	if rev := Decode("tenant", event.GetData().GetTenantEvent()); rev != nil {
		t.Fatalf("wrong revocation kind accepted: %v", rev)
	}
}

func TestReconnectBackfillRepairsMissedEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan *signalmanpb.SignalmanEvent, 1)
		var snapshot atomic.Pointer[commodorepb.ListRecentSessionRevocationsResponse]
		snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true})
		var opens atomic.Int32
		feed := Start(context.Background(), Config{Secret: testSecret,
			List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
				return snapshot.Load(), nil
			},
			Open: func(ctx context.Context) (Source, error) {
				opens.Add(1)
				return &fakeStream{ctx: ctx, events: events}, nil
			},
		})
		defer feed.Close()
		synctest.Wait()
		if !feed.synchronized.Load() {
			t.Fatal("initial snapshot not applied")
		}
		events <- nil
		synctest.Wait()
		snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true, Revocations: []*commodorepb.SessionRevocation{testRevocation("missed")}})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if opens.Load() < 2 {
			t.Fatal("stream did not reconnect")
		}
		if err := feed.CheckSession(context.Background(), testToken(t, "missed")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("missed revocation not restored: %v", err)
		}
	})
}

func TestLiveEventCannotBeOverwrittenByOlderSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		events := make(chan *signalmanpb.SignalmanEvent, 1)
		release := make(chan struct{})
		feed := Start(context.Background(), Config{Secret: testSecret,
			List: func(ctx context.Context, _ time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-release:
				}
				return &commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true}, nil
			},
			Open: func(ctx context.Context) (Source, error) {
				return &fakeStream{ctx: ctx, events: events}, nil
			},
		})
		defer feed.Close()
		events <- eventFor(t, "during-snapshot")
		synctest.Wait()
		close(release)
		synctest.Wait()
		if err := feed.CheckSession(context.Background(), testToken(t, "during-snapshot")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("snapshot lost live event: %v", err)
		}
	})
}

func TestLegacySnapshotDoesNotClaimNamedCoverage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		feed := Start(context.Background(), Config{Secret: testSecret,
			List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
				return &commodorepb.ListRecentSessionRevocationsResponse{Revocations: []*commodorepb.SessionRevocation{testRevocation("")}}, nil
			},
		})
		defer feed.Close()
		synctest.Wait()
		if feed.synchronized.Load() {
			t.Fatal("old authority was treated as named-session coverage")
		}
		if !feed.cache.Revoked("tenant", "user", time.Now()) {
			t.Fatal("legacy cutoff was discarded")
		}
	})
}

func TestPeriodicBackfillWorksWithoutSignalman(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var snapshot atomic.Pointer[commodorepb.ListRecentSessionRevocationsResponse]
		snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true})
		feed := Start(context.Background(), Config{Secret: testSecret, Interval: time.Second,
			List: func(context.Context, time.Time) (*commodorepb.ListRecentSessionRevocationsResponse, error) {
				return snapshot.Load(), nil
			},
			Open: func(context.Context) (Source, error) { return nil, errors.New("Signalman down") },
		})
		defer feed.Close()
		synctest.Wait()
		snapshot.Store(&commodorepb.ListRecentSessionRevocationsResponse{AuthSessionsIncluded: true, Revocations: []*commodorepb.SessionRevocation{testRevocation("missed")}})
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if err := feed.CheckSession(context.Background(), testToken(t, "missed")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("periodic snapshot did not repair state: %v", err)
		}
	})
}
