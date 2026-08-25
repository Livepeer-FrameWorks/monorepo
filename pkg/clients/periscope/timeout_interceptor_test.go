package periscope

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
)

func TestPeriscopeTimeoutInterceptorBoundsDetachedCall(t *testing.T) {
	timeout := 20 * time.Millisecond
	deadlineSeen := make(chan time.Time, 1)
	err := periscopeTimeoutInterceptor(timeout)(context.Background(), "/test.Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				return errors.New("missing deadline")
			}
			deadlineSeen <- deadline
			<-ctx.Done()
			return ctx.Err()
		})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interceptor error = %v, want deadline exceeded", err)
	}
	deadline := <-deadlineSeen
	if time.Until(deadline) > timeout {
		t.Fatalf("deadline %v exceeds configured timeout %s", deadline, timeout)
	}
}

func TestPeriscopeTimeoutInterceptorPreservesShorterCallerDeadline(t *testing.T) {
	callerCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	want, _ := callerCtx.Deadline()
	var got time.Time
	err := periscopeTimeoutInterceptor(time.Second)(callerCtx, "/test.Method", nil, nil, nil,
		func(ctx context.Context, _ string, _, _ interface{}, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			got, _ = ctx.Deadline()
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("deadline = %v, want caller deadline %v", got, want)
	}
}
