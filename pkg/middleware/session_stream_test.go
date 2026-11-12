package middleware

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestInteractiveStreamStaysRevocable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state int32
		wait  time.Duration
		want  codes.Code
	}{
		{"expiry", 0, 16 * time.Minute, codes.Unauthenticated},
		{"revocation", 1, 31 * time.Second, codes.Unauthenticated},
		{"validation outage", 2, 31 * time.Second, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				secret := []byte("stream-session-test")
				token, err := auth.GenerateJWT("user", "tenant", "user@example.test", "owner", secret)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token)))
				defer cancel()
				var state atomic.Int32
				cfg := GRPCAuthConfig{JWTSecret: secret, ValidateSession: func(context.Context, string) error {
					switch state.Load() {
					case 1:
						return status.Error(codes.Unauthenticated, "session signed out")
					case 2:
						return status.Error(codes.Unavailable, "session state unavailable")
					default:
						return nil
					}
				}}
				started, result := make(chan struct{}), make(chan error, 1)
				go func() {
					result <- GRPCStreamAuthInterceptor(cfg)(nil, &fakeServerStream{ctx: ctx},
						&grpc.StreamServerInfo{FullMethod: "/signalman.SignalmanService/Subscribe"},
						func(_ any, stream grpc.ServerStream) error {
							close(started)
							<-stream.Context().Done()
							return stream.Context().Err()
						})
				}()
				<-started
				state.Store(tc.state)
				time.Sleep(tc.wait)
				synctest.Wait()
				select {
				case err := <-result:
					if status.Code(err) != tc.want {
						t.Fatalf("stream ended with %v, want %v: %v", status.Code(err), tc.want, err)
					}
				default:
					t.Fatal("authenticated stream remained open after " + tc.name)
				}
			})
		})
	}
}
